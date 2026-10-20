// Package cubrid implements the CUBRID CAS broker protocol.
// Packet structures mirror pycubrid's protocol.py.
package cubrid

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"time"
)

// ColumnMetaData holds metadata for a single result-set column.
type ColumnMetaData struct {
	ColumnType      int
	Scale           int
	Precision       int
	Name            string
	RealName        string
	TableName       string
	IsNullable      bool
	DefaultValue    string
	IsAutoIncrement bool
	IsUniqueKey     bool
	IsPrimaryKey    bool
	IsForeignKey    bool
}

// ResultInfo holds per-statement result metadata.
type ResultInfo struct {
	StmtType    int
	ResultCount int
	OID         []byte
}

// raiseError parses an error response body and returns an error value.
func raiseError(reader *PacketReader, responseLength int) error {
	code, msg := reader.readError(responseLength)
	return newError(code, msg)
}

// parseColumnMetadata parses count column-metadata entries from reader.
func parseColumnMetadata(reader *PacketReader, count int) []ColumnMetaData {
	cols := make([]ColumnMetaData, 0, count)
	for i := 0; i < count; i++ {
		legacyType := int(reader.parseByte())
		var colType int
		if legacyType&0x80 != 0 {
			colType = int(reader.parseByte())
		} else {
			colType = legacyType
		}
		scale := int(reader.parseShort())
		precision := int(reader.parseInt())

		nameLen := int(reader.parseInt())
		name := reader.parseNullTermString(nameLen)
		realNameLen := int(reader.parseInt())
		realName := reader.parseNullTermString(realNameLen)
		tableNameLen := int(reader.parseInt())
		tableName := reader.parseNullTermString(tableNameLen)

		isNullable := reader.parseByte() == 1
		defaultLen := int(reader.parseInt())
		defaultValue := reader.parseNullTermString(defaultLen)
		isAutoIncrement := reader.parseByte() == 1
		isUniqueKey := reader.parseByte() == 1
		isPrimaryKey := reader.parseByte() == 1
		reader.parseByte() // is_reverse_index
		reader.parseByte() // is_reverse_unique
		isForeignKey := reader.parseByte() == 1
		reader.parseByte() // is_shared

		cols = append(cols, ColumnMetaData{
			ColumnType:      colType,
			Scale:           scale,
			Precision:       precision,
			Name:            name,
			RealName:        realName,
			TableName:       tableName,
			IsNullable:      isNullable,
			DefaultValue:    defaultValue,
			IsAutoIncrement: isAutoIncrement,
			IsUniqueKey:     isUniqueKey,
			IsPrimaryKey:    isPrimaryKey,
			IsForeignKey:    isForeignKey,
		})
	}
	return cols
}

// parseResultInfos parses count result-info entries from reader.
func parseResultInfos(reader *PacketReader, count int) []ResultInfo {
	infos := make([]ResultInfo, 0, count)
	for i := 0; i < count; i++ {
		stmtType := int(reader.parseByte())
		resultCount := int(reader.parseInt())
		oid := reader.parseRawBytes(SizeOID)
		reader.parseInt() // cache_time_sec
		reader.parseInt() // cache_time_usec
		infos = append(infos, ResultInfo{
			StmtType:    stmtType,
			ResultCount: resultCount,
			OID:         oid,
		})
	}
	return infos
}

// readValue reads and returns a single typed column value.
func readValue(reader *PacketReader, colType int, size int) interface{} {
	switch colType {
	case TypeChar, TypeString, TypeNChar, TypeVarNChar, TypeEnum:
		return reader.parseNullTermString(size)
	case TypeShort:
		return int64(reader.parseShort())
	case TypeInt:
		return int64(reader.parseInt())
	case TypeBigInt:
		return reader.parseLong()
	case TypeFloat:
		return float64(reader.parseFloat())
	case TypeDouble, TypeMonetary:
		return reader.parseDouble()
	case TypeNumeric:
		// Stored as null-terminated string; caller may parse to decimal.
		return reader.parseNullTermString(size)
	case TypeDate:
		return reader.parseDate()
	case TypeTime:
		return reader.parseTime()
	case TypeDatetime:
		return reader.parseDatetime()
	case TypeTimestamp:
		return reader.parseTimestamp()
	case TypeBit, TypeVarBit:
		return reader.parseRawBytes(size)
	case TypeBlob, TypeClob:
		return reader.parseRawBytes(size) // raw LOB handle
	case TypeNull:
		return nil
	default:
		return reader.parseRawBytes(size)
	}
}

// parseRowData parses tupleCount rows from reader.
func parseRowData(
	reader *PacketReader,
	tupleCount int,
	columns []ColumnMetaData,
	stmtType int,
) [][]interface{} {
	isCallType := stmtType == StmtCallSP
	rows := make([][]interface{}, 0, tupleCount)
	for i := 0; i < tupleCount; i++ {
		reader.parseInt()             // row index
		reader.parseRawBytes(SizeOID) // OID

		row := make([]interface{}, len(columns))
		for j, col := range columns {
			size := int(reader.parseInt())
			if size <= 0 {
				row[j] = nil
				continue
			}
			colType := col.ColumnType
			if isCallType || colType == TypeNull {
				colType = int(reader.parseByte())
				size--
				if size <= 0 {
					row[j] = nil
					continue
				}
			}
			row[j] = readValue(reader, colType, size)
		}
		rows = append(rows, row)
	}
	return rows
}

// ─── Handshake ────────────────────────────────────────────────────────────────

// WriteClientInfoExchange returns the 10-byte handshake request (no header).
func WriteClientInfoExchange() []byte {
	buf := make([]byte, 0, 10)
	buf = append(buf, []byte(MagicString)...)
	buf = append(buf, ClientJDBC)
	buf = append(buf, CASVersion)
	buf = append(buf, 0x00, 0x00, 0x00) // padding
	return buf
}

// ParseClientInfoExchange parses the 4-byte handshake response.
func ParseClientInfoExchange(data []byte) int {
	return int(int32(binary.BigEndian.Uint32(data[:4])))
}

// ─── Open / Close Database ────────────────────────────────────────────────────

// WriteOpenDatabase returns the 628-byte open-database request (no header).
func WriteOpenDatabase(database, user, password string) []byte {
	w := newPacketWriter()
	w.writeFixedString(database, 32)
	w.writeFixedString(user, 32)
	w.writeFixedString(password, 32)
	w.writeFiller(512) // extended info
	w.writeFiller(20)  // reserved
	return w.toBytes()
}

// OpenDatabaseResult holds the parsed open-database response.
type OpenDatabaseResult struct {
	CASInfo         [SizeCASInfo]byte
	ProtocolVersion int
	SessionID       int
}

// ParseOpenDatabase parses the open-database response.
// data must start at byte 0 of the response (including CAS_INFO).
func ParseOpenDatabase(data []byte) (*OpenDatabaseResult, error) {
	reader := newPacketReader(data)

	var casInfo [SizeCASInfo]byte
	copy(casInfo[:], reader.parseRawBytes(SizeCASInfo))

	responseCode := reader.parseInt()
	if responseCode < 0 {
		remaining := len(data) - SizeCASInfo - SizeInt
		return nil, raiseError(reader, remaining)
	}

	brokerBytes := reader.parseRawBytes(SizeBrokerInfo)
	protoVersion := int(brokerBytes[4]) & 0x3F
	sessionID := int(reader.parseInt())

	return &OpenDatabaseResult{
		CASInfo:         casInfo,
		ProtocolVersion: protoVersion,
		SessionID:       sessionID,
	}, nil
}

// WriteConClose returns the CON_CLOSE request packet.
func WriteConClose(casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncConClose)
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ─── Transaction ─────────────────────────────────────────────────────────────

// WriteEndTran returns a COMMIT or ROLLBACK request packet.
func WriteEndTran(txType byte, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncEndTran)
	w.addByte(txType)
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ParseSimpleResponse parses a response that only contains a result code.
func ParseSimpleResponse(data []byte) error {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)
	code := reader.parseInt()
	if code < 0 {
		return raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}
	return nil
}

// ─── PrepareAndExecute ───────────────────────────────────────────────────────

// WritePrepareAndExecute returns a PREPARE_AND_EXECUTE request packet.
func WritePrepareAndExecute(sql string, autoCommit bool, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncPrepareAndExecute)
	w.addInt(3) // arg count
	w.writeNullTermString(sql)
	w.addByte(PrepareNormal)
	if autoCommit {
		w.addByte(1)
	} else {
		w.addByte(0)
	}
	w.addByte(ExecuteQueryAll)
	w.addInt(0)          // max_col_size
	w.addInt(0)          // max_row_size
	w.writeInt(0)        // NULL (bind params)
	w.writeInt(SizeLong) // cache time length
	w.writeInt(0)        // cache time sec
	w.writeInt(0)        // cache time usec
	w.addInt(0)          // query timeout
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// PrepareAndExecuteResult holds the result of a PREPARE_AND_EXECUTE response.
type PrepareAndExecuteResult struct {
	QueryHandle     int
	StatementType   int
	BindCount       int
	Columns         []ColumnMetaData
	TotalTupleCount int
	ResultInfos     []ResultInfo
	TupleCount      int
	Rows            [][]interface{}
}

// ParsePrepareAndExecute parses a PREPARE_AND_EXECUTE response.
func ParsePrepareAndExecute(data []byte, protoVersion int) (*PrepareAndExecuteResult, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)

	responseCode := reader.parseInt()
	if responseCode < 0 {
		return nil, raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}

	res := &PrepareAndExecuteResult{QueryHandle: int(responseCode)}
	reader.parseInt()                           // result cache lifetime
	res.StatementType = int(reader.parseByte()) // statement type
	res.BindCount = int(reader.parseInt())      // bind count
	reader.parseByte()                          // is_updatable
	colCount := int(reader.parseInt())
	res.Columns = parseColumnMetadata(reader, colCount)

	res.TotalTupleCount = int(reader.parseInt())
	reader.parseByte() // cache_reusable
	resultCount := int(reader.parseInt())
	res.ResultInfos = parseResultInfos(reader, resultCount)

	if protoVersion > 1 {
		reader.parseByte() // includes_column_info
	}
	if protoVersion > 4 {
		reader.parseInt() // shard_id
	}

	if res.StatementType == StmtSelect && reader.bytesRemaining() >= SizeInt*2 {
		reader.parseInt() // fetch_code
		res.TupleCount = int(reader.parseInt())
		if res.TupleCount > 0 {
			res.Rows = parseRowData(reader, res.TupleCount, res.Columns, res.StatementType)
		}
	}

	return res, nil
}

// ─── Fetch ────────────────────────────────────────────────────────────────────

// WriteFetch returns a FETCH request packet.
func WriteFetch(queryHandle, currentTupleCount, fetchSize int, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncFetch)
	w.addInt(int32(queryHandle))
	w.addInt(int32(currentTupleCount + 1))
	w.addInt(int32(fetchSize))
	w.addByte(0) // case_sensitive
	w.addInt(0)  // resultset_index
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// FetchResult holds fetched rows.
type FetchResult struct {
	TupleCount int
	Rows       [][]interface{}
}

// ParseFetch parses a FETCH response.
func ParseFetch(data []byte, columns []ColumnMetaData, stmtType int) (*FetchResult, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)
	responseCode := reader.parseInt()
	if responseCode < 0 {
		return nil, raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}
	res := &FetchResult{}
	res.TupleCount = int(reader.parseInt())
	if res.TupleCount > 0 && len(columns) > 0 {
		res.Rows = parseRowData(reader, res.TupleCount, columns, stmtType)
	}
	return res, nil
}

// ─── Close Query Handle ───────────────────────────────────────────────────────

// WriteCloseReqHandle returns a CLOSE_REQ_HANDLE request packet.
func WriteCloseReqHandle(queryHandle int, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncCloseReqHandle)
	w.addInt(int32(queryHandle))
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ─── DB Version ──────────────────────────────────────────────────────────────

// WriteGetDbVersion returns a GET_DB_VERSION request packet.
func WriteGetDbVersion(autoCommit bool, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncGetDbVersion)
	if autoCommit {
		w.addByte(1)
	} else {
		w.addByte(0)
	}
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ParseGetDbVersion parses a GET_DB_VERSION response.
func ParseGetDbVersion(data []byte) (string, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)
	code := reader.parseInt()
	if code < 0 {
		return "", raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}
	versionLen := len(data) - SizeCASInfo - SizeInt
	return reader.parseNullTermString(versionLen), nil
}

// ─── Check CAS (FC=32) ───────────────────────────────────────────────────────

// WriteCheckCas returns a CHECK_CAS request packet (FC=32, no arguments).
func WriteCheckCas(casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncCheckCas)
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ParseCheckCas parses a CHECK_CAS response. A successful reply may carry
// an empty body or a non-negative code; a negative code is an error.
func ParseCheckCas(data []byte) error {
	if len(data) < SizeCASInfo+SizeInt {
		return nil
	}
	return ParseSimpleResponse(data)
}

// ─── Last Insert ID ───────────────────────────────────────────────────────────

// WriteGetLastInsertId returns a GET_LAST_INSERT_ID request packet.
func WriteGetLastInsertId(casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncGetLastInsertId)
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ParseGetLastInsertId parses a GET_LAST_INSERT_ID response.
func ParseGetLastInsertId(data []byte) (string, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)
	code := reader.parseInt()
	if code < 0 {
		return "", raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}
	if code > 0 {
		return reader.parseNullTermString(int(code)), nil
	}
	return "", nil
}

// ─── Prepare (FC=2) ──────────────────────────────────────────────────────────

// WritePrepare returns a PREPARE request packet (FC=2).
// The server parses the SQL, validates it, and returns a reusable query handle.
func WritePrepare(sql string, autoCommit bool, casInfo [SizeCASInfo]byte) []byte {
	w := newPacketWriter()
	w.writeByte(FuncPrepare)
	w.writeNullTermString(sql)
	w.addByte(PrepareNormal)
	if autoCommit {
		w.addByte(1)
	} else {
		w.addByte(0)
	}
	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// PrepareResult holds the parsed PREPARE response.
type PrepareResult struct {
	QueryHandle   int
	StatementType int
	BindCount     int
	Columns       []ColumnMetaData
}

// ParsePrepare parses a PREPARE response.
func ParsePrepare(data []byte) (*PrepareResult, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)

	responseCode := reader.parseInt()
	if responseCode < 0 {
		return nil, raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}

	res := &PrepareResult{QueryHandle: int(responseCode)}
	reader.parseInt()                           // result cache lifetime
	res.StatementType = int(reader.parseByte()) // statement type
	res.BindCount = int(reader.parseInt())      // number of ? placeholders
	reader.parseByte()                          // is_updatable
	colCount := int(reader.parseInt())
	res.Columns = parseColumnMetadata(reader, colCount)
	return res, nil
}

// ─── Execute (FC=3) ───────────────────────────────────────────────────────────

// WriteExecute returns an EXECUTE request packet (FC=3).
// Typed bind parameters are encoded and sent over the wire — no SQL interpolation.
func WriteExecute(
	queryHandle int,
	stmtType int,
	args []driver.Value,
	autoCommit bool,
	casInfo [SizeCASInfo]byte,
) []byte {
	fetchFlag := byte(0)
	if stmtType == StmtSelect {
		fetchFlag = 1
	}
	w := newPacketWriter()
	w.writeByte(FuncExecute)
	w.addInt(int32(queryHandle))
	w.addByte(ExecuteNormal)
	w.addInt(0) // max_col_size
	w.addInt(0) // max_row_size

	// Bind-parameter block: int32(size) + packed params, or int32(0) for none.
	bindData := encodeBindParams(args)
	if len(bindData) > 0 {
		w.addBytes(bindData)
	} else {
		w.writeInt(0) // NULL — no bind params
	}

	// Fetch / commit / cursor options (raw, not length-prefixed).
	w.writeInt(1)          // bind_mode_count
	w.writeByte(fetchFlag) // fetch inline rows for SELECT
	w.addByte(func() byte {
		if autoCommit {
			return 1
		}
		return 0
	}())
	w.addByte(1) // forward_only
	w.addCacheTime()
	w.addInt(0) // query timeout

	payload := w.toBytes()
	return append(buildProtocolHeader(len(payload), casInfo), payload...)
}

// ExecuteResult holds the parsed EXECUTE response.
type ExecuteResult struct {
	TotalTupleCount int
	ResultInfos     []ResultInfo
	TupleCount      int
	Rows            [][]interface{}
}

// ParseExecute parses an EXECUTE response.
// columns must be the column metadata returned by ParsePrepare.
func ParseExecute(
	data []byte,
	columns []ColumnMetaData,
	stmtType int,
	protoVersion int,
) (*ExecuteResult, error) {
	reader := newPacketReader(data)
	reader.parseRawBytes(SizeCASInfo)

	responseCode := reader.parseInt()
	if responseCode < 0 {
		return nil, raiseError(reader, len(data)-SizeCASInfo-SizeInt)
	}

	res := &ExecuteResult{TotalTupleCount: int(responseCode)}
	reader.parseByte() // cache_reusable
	resultCount := int(reader.parseInt())
	res.ResultInfos = parseResultInfos(reader, resultCount)

	if protoVersion > 1 {
		reader.parseByte() // includes_column_info
	}
	if protoVersion > 4 {
		reader.parseInt() // shard_id
	}

	if stmtType == StmtSelect && reader.bytesRemaining() >= SizeInt*2 {
		reader.parseInt() // fetch_code
		res.TupleCount = int(reader.parseInt())
		if res.TupleCount > 0 && len(columns) > 0 {
			res.Rows = parseRowData(reader, res.TupleCount, columns, stmtType)
		}
	}

	return res, nil
}

// ─── Bind-parameter encoding ──────────────────────────────────────────────────

// encodeBindParams packs []driver.Value into the CUBRID CAS wire format.
//
// Each parameter is encoded as:
//
//	int32  size   — number of bytes to follow; 0 means SQL NULL
//	byte   type   — CUBRIDDataType (omitted when size == 0)
//	bytes  value  — type-specific raw bytes (size-1 bytes)
//
// The returned slice is the concatenation of all parameter encodings.
// The caller prepends the total length via addBytes().
func encodeBindParams(args []driver.Value) []byte {
	if len(args) == 0 {
		return nil
	}
	w := newPacketWriter()
	for _, arg := range args {
		encodeOneParam(w, arg)
	}
	return w.toBytes()
}

func encodeOneParam(w *PacketWriter, arg driver.Value) {
	if arg == nil {
		w.writeInt(0) // SQL NULL
		return
	}
	switch v := arg.(type) {
	case bool:
		w.writeInt(1 + SizeShort) // type + int16
		w.writeByte(TypeShort)
		if v {
			w.writeShort(1)
		} else {
			w.writeShort(0)
		}
	case int64:
		w.writeInt(1 + SizeLong) // type + int64
		w.writeByte(TypeBigInt)
		w.writeLong(v)
	case float64:
		w.writeInt(1 + SizeDouble) // type + float64
		w.writeByte(TypeDouble)
		w.writeDouble(v)
	case string:
		encoded := []byte(v)
		w.writeInt(int32(1 + len(encoded) + 1)) // type + utf8 + null byte
		w.writeByte(TypeString)
		w.writeRawBytes(encoded)
		w.writeByte(0x00)
	case []byte:
		w.writeInt(int32(1 + len(v))) // type + raw bytes
		w.writeByte(TypeVarBit)
		w.writeRawBytes(v)
	case time.Time:
		v = v.UTC()
		w.writeInt(1 + SizeDatetime) // type + 7×int16
		w.writeByte(TypeDatetime)
		w.writeShort(int16(v.Year()))
		w.writeShort(int16(v.Month()))
		w.writeShort(int16(v.Day()))
		w.writeShort(int16(v.Hour()))
		w.writeShort(int16(v.Minute()))
		w.writeShort(int16(v.Second()))
		w.writeShort(int16(v.Nanosecond() / 1e6))
	default:
		// Fallback: stringify unknown types.
		s := fmt.Sprintf("%v", v)
		encoded := []byte(s)
		w.writeInt(int32(1 + len(encoded) + 1))
		w.writeByte(TypeString)
		w.writeRawBytes(encoded)
		w.writeByte(0x00)
	}
}

// Ensure imports are used.
var _ = fmt.Sprintf
var _ time.Time
