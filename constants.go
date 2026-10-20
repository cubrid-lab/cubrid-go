package cubrid

// CAS function codes (pycubrid CASFunctionCode)
const (
	FuncEndTran           = 1
	FuncPrepare           = 2
	FuncExecute           = 3
	FuncGetDbParameter    = 4
	FuncSetDbParameter    = 5
	FuncCloseReqHandle    = 6
	FuncFetch             = 8
	FuncSchemaInfo        = 9
	FuncGetDbVersion      = 15
	FuncExecuteBatch      = 20
	FuncConClose          = 31
	FuncCheckCas          = 32
	FuncGetLastInsertId   = 40
	FuncPrepareAndExecute = 41
)

// CUBRID data types (pycubrid CUBRIDDataType)
const (
	TypeNull      = 0
	TypeChar      = 1
	TypeString    = 2
	TypeNChar     = 3
	TypeVarNChar  = 4
	TypeBit       = 5
	TypeVarBit    = 6
	TypeNumeric   = 7
	TypeInt       = 8
	TypeShort     = 9
	TypeMonetary  = 10
	TypeFloat     = 11
	TypeDouble    = 12
	TypeDate      = 13
	TypeTime      = 14
	TypeTimestamp = 15
	TypeSet       = 16
	TypeMultiset  = 17
	TypeSequence  = 18
	TypeObject    = 19
	TypeBigInt    = 21
	TypeDatetime  = 22
	TypeBlob      = 23
	TypeClob      = 24
	TypeEnum      = 25
)

// Transaction types (pycubrid CCITransactionType)
const (
	TxCommit   = 1
	TxRollback = 2
)

// Prepare flags (pycubrid CCIPrepareOption)
const (
	PrepareNormal   = 0x00
	PrepareHoldable = 0x08
)

// Execute flags (pycubrid CCIExecutionOption)
const (
	ExecuteNormal   = 0x00
	ExecuteQueryAll = 0x02
)

// Statement types (pycubrid CUBRIDStatementType)
const (
	StmtAlterClass    = 0
	StmtAlterSerial   = 1
	StmtCommitWork    = 2
	StmtRegisterDB    = 3
	StmtCreateClass   = 4
	StmtCreateIndex   = 5
	StmtCreateTrigger = 6
	StmtCreateSerial  = 7
	StmtDropDatabase  = 8
	StmtDropClass     = 9
	StmtDropIndex     = 10
	StmtDropLabel     = 11
	StmtDropTrigger   = 12
	StmtDropSerial    = 13
	StmtEvaluate      = 14
	StmtRenameClass   = 15
	StmtRollbackWork  = 16
	StmtGrant         = 17
	StmtRevoke        = 18
	StmtStatistics    = 19
	StmtInsert        = 20
	StmtSelect        = 21
	StmtUpdate        = 22
	StmtDelete        = 23
	StmtCall          = 24
	StmtCallSP        = 0x7E
	StmtUnknown       = 0x7F
)

// Protocol constants (pycubrid CASProtocol)
const (
	MagicString  = "CUBRK"
	ClientJDBC   = 3
	CASVersion   = 0x47
	ProtoVersion = 7
)

// Wire-level byte sizes (pycubrid DataSize)
const (
	SizeByte       = 1
	SizeShort      = 2
	SizeInt        = 4
	SizeLong       = 8
	SizeFloat      = 4
	SizeDouble     = 8
	SizeDatetime   = 14 // 7 x int16
	SizeOID        = 8
	SizeCASInfo    = 4
	SizeDataLength = 4
	SizeBrokerInfo = 16
)
