package cubrid

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// buildProtocolHeader creates an 8-byte protocol header:
// 4 bytes DATA_LENGTH (big-endian) + 4 bytes CAS_INFO.
func buildProtocolHeader(dataLength int, casInfo [SizeCASInfo]byte) []byte {
	buf := make([]byte, SizeDataLength+SizeCASInfo)
	binary.BigEndian.PutUint32(buf[:SizeDataLength], uint32(dataLength)) // #nosec G115 -- dataLength is a non-negative len() that conn.roundTrip caps at MaxInt32 (checkRequestLength) before anything is sent
	copy(buf[SizeDataLength:], casInfo[:])
	return buf
}

// maxRequestPayload is the largest request payload the 4-byte CAS
// DATA_LENGTH header field (and the int32 length prefixes inside it) can carry.
// It is a variable only so tests can lower it; production never changes it.
var maxRequestPayload int64 = math.MaxInt32

// checkRequestLength rejects a framed request whose payload (everything after
// DATA_LENGTH and CAS_INFO) does not fit maxRequestPayload. Every int32 length
// field in a request describes a part of that payload, so this one check also
// bounds them.
func checkRequestLength(packetLen int64) error {
	if payload := packetLen - SizeDataLength - SizeCASInfo; payload > maxRequestPayload {
		return &ProgrammingError{CubridError{Code: -1,
			Message: fmt.Sprintf("request payload of %d bytes exceeds the CAS protocol limit of %d bytes", payload, maxRequestPayload)}}
	}
	return nil
}

// parseProtocolHeader parses an 8-byte protocol header.
func parseProtocolHeader(data []byte) (int, [SizeCASInfo]byte) {
	length := int(binary.BigEndian.Uint32(data[:SizeDataLength]))
	var cas [SizeCASInfo]byte
	copy(cas[:], data[SizeDataLength:SizeDataLength+SizeCASInfo])
	return length, cas
}

// PacketWriter serializes values into a big-endian byte buffer,
// mirroring pycubrid's PacketWriter class.
type PacketWriter struct {
	buf []byte
}

func newPacketWriter() *PacketWriter {
	return &PacketWriter{buf: make([]byte, 0, 64)}
}

// Low-level write helpers
func (w *PacketWriter) writeByte(v byte) {
	w.buf = append(w.buf, v)
}

func (w *PacketWriter) writeShort(v int16) {
	b := make([]byte, SizeShort)
	binary.BigEndian.PutUint16(b, uint16(v)) // #nosec G115 -- bit-preserving two's-complement reinterpretation; CAS SHORT is a signed big-endian 16-bit field
	w.buf = append(w.buf, b...)
}

func (w *PacketWriter) writeInt(v int32) {
	b := make([]byte, SizeInt)
	binary.BigEndian.PutUint32(b, uint32(v)) // #nosec G115 -- bit-preserving two's-complement reinterpretation; CAS INT is a signed big-endian 32-bit field
	w.buf = append(w.buf, b...)
}

func (w *PacketWriter) writeLong(v int64) {
	b := make([]byte, SizeLong)
	binary.BigEndian.PutUint64(b, uint64(v)) // #nosec G115 -- bit-preserving two's-complement reinterpretation; CAS BIGINT is a signed big-endian 64-bit field
	w.buf = append(w.buf, b...)
}

func (w *PacketWriter) writeFloat(v float32) {
	b := make([]byte, SizeFloat)
	binary.BigEndian.PutUint32(b, math.Float32bits(v))
	w.buf = append(w.buf, b...)
}

func (w *PacketWriter) writeDouble(v float64) {
	b := make([]byte, SizeDouble)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	w.buf = append(w.buf, b...)
}

func (w *PacketWriter) writeRawBytes(v []byte) {
	w.buf = append(w.buf, v...)
}

func (w *PacketWriter) writeFiller(count int) {
	for i := 0; i < count; i++ {
		w.buf = append(w.buf, 0x00)
	}
}

// writeFixedString writes a UTF-8 string padded/truncated to exactly length bytes.
func (w *PacketWriter) writeFixedString(s string, length int) {
	encoded := []byte(s)
	if len(encoded) > length {
		encoded = encoded[:length]
	}
	w.buf = append(w.buf, encoded...)
	if len(encoded) < length {
		w.writeFiller(length - len(encoded))
	}
}

// writeNullTermString writes a length-prefixed null-terminated UTF-8 string.
// Format: int32(len+1) + bytes + 0x00
func (w *PacketWriter) writeNullTermString(s string) {
	encoded := []byte(s)
	w.writeInt(int32(len(encoded) + 1)) // #nosec G115 -- the string is part of the request payload, which conn.roundTrip caps at MaxInt32 bytes (checkRequestLength) before anything is sent
	w.buf = append(w.buf, encoded...)
	w.buf = append(w.buf, 0x00)
}

// High-level add methods (length-prefixed scalars)

// addByte writes int32(1) + byte.
func (w *PacketWriter) addByte(v byte) {
	w.writeInt(SizeByte)
	w.writeByte(v)
}

// addShort writes int32(2) + int16.
func (w *PacketWriter) addShort(v int16) {
	w.writeInt(SizeShort)
	w.writeShort(v)
}

// addInt writes int32(4) + int32.
func (w *PacketWriter) addInt(v int32) {
	w.writeInt(SizeInt)
	w.writeInt(v)
}

// addLong writes int32(8) + int64.
func (w *PacketWriter) addLong(v int64) {
	w.writeInt(SizeLong)
	w.writeLong(v)
}

// addFloat writes int32(4) + float32.
func (w *PacketWriter) addFloat(v float32) {
	w.writeInt(SizeFloat)
	w.writeFloat(v)
}

// addDouble writes int32(8) + float64.
func (w *PacketWriter) addDouble(v float64) {
	w.writeInt(SizeDouble)
	w.writeDouble(v)
}

// addBytes writes int32(len) + raw bytes.
func (w *PacketWriter) addBytes(v []byte) {
	w.writeInt(int32(len(v))) // #nosec G115 -- v is part of the request payload, which conn.roundTrip caps at MaxInt32 bytes (checkRequestLength) before anything is sent
	w.buf = append(w.buf, v...)
}

// addNull writes a zero-length marker.
func (w *PacketWriter) addNull() {
	w.writeInt(0)
}

// addDatetime writes a length-prefixed datetime value (7 x int16 = 14 bytes).
func (w *PacketWriter) addDatetime(t time.Time) {
	w.writeInt(SizeDatetime)
	w.writeShort(int16(t.Year()))             // #nosec G115 -- CUBRID DATE/DATETIME years are 1..9999 and fit int16; internal callers never pass caller-supplied times (the driver interpolates time.Time args into SQL client-side); this writer is unexported
	w.writeShort(int16(t.Month()))            // #nosec G115 -- time.Time.Month() is 1..12
	w.writeShort(int16(t.Day()))              // #nosec G115 -- time.Time.Day() is 1..31
	w.writeShort(int16(t.Hour()))             // #nosec G115 -- time.Time.Hour() is 0..23
	w.writeShort(int16(t.Minute()))           // #nosec G115 -- time.Time.Minute() is 0..59
	w.writeShort(int16(t.Second()))           // #nosec G115 -- time.Time.Second() is 0..59
	w.writeShort(int16(t.Nanosecond() / 1e6)) // #nosec G115 -- time.Time.Nanosecond() is 0..999999999, so milliseconds are 0..999
}

// addDate writes a datetime with zeroed time fields.
func (w *PacketWriter) addDate(t time.Time) {
	w.writeInt(SizeDatetime)
	w.writeShort(int16(t.Year()))  // #nosec G115 -- CUBRID DATE/DATETIME years are 1..9999 and fit int16; internal callers never pass caller-supplied times (the driver interpolates time.Time args into SQL client-side); this writer is unexported
	w.writeShort(int16(t.Month())) // #nosec G115 -- time.Time.Month() is 1..12
	w.writeShort(int16(t.Day()))   // #nosec G115 -- time.Time.Day() is 1..31
	w.writeShort(0)                // hour
	w.writeShort(0)                // minute
	w.writeShort(0)                // second
	w.writeShort(0)                // millisecond
}

// addCacheTime writes a length-prefixed cache time (two zero int32s).
func (w *PacketWriter) addCacheTime() {
	w.writeInt(SizeLong)
	w.writeInt(0) // sec
	w.writeInt(0) // usec
}

func (w *PacketWriter) toBytes() []byte { return w.buf }
func (w *PacketWriter) Len() int        { return len(w.buf) }

// errMalformedReply is wrapped by every error that reports a CAS reply whose
// lengths or counts do not fit the reply (see PacketReader). Such a reply
// leaves the session in an unknown state, so the conn that received it is
// retired (see conn.replyErr). errors.As still finds *OperationalError.
var errMalformedReply error = &OperationalError{CubridError{Code: -1, Message: "malformed CAS reply"}}

// PacketReader deserializes big-endian binary data,
// mirroring pycubrid's PacketReader class.
//
// Every read is checked against the bytes remaining in the reply. The first
// read that does not fit records err (wrapping errMalformedReply) and every
// later read returns a zero value, so parsers check err once before they use
// what they read, instead of after each field. Nothing is allocated for a
// length or count before it has been checked.
type PacketReader struct {
	buf    []byte
	offset int
	err    error
}

func newPacketReader(data []byte) *PacketReader {
	return &PacketReader{buf: data}
}

// fail records the first malformed-reply error.
func (r *PacketReader) fail(format string, args ...interface{}) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: %s", errMalformedReply, fmt.Sprintf(format, args...))
	}
}

// take returns the next n bytes of the reply, without copying, or nil when n
// is negative or exceeds the bytes remaining (and records the error).
func (r *PacketReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 {
		r.fail("negative length %d at offset %d", n, r.offset)
		return nil
	}
	if n > r.bytesRemaining() {
		r.fail("length %d at offset %d exceeds the %d bytes remaining", n, r.offset, r.bytesRemaining())
		return nil
	}
	v := r.buf[r.offset : r.offset+n : r.offset+n]
	r.offset += n
	return v
}

// checkCount reports whether count entries of at least minSize bytes each
// can fit in the bytes remaining, and records the error when they cannot. A
// count that passes cannot make a parser allocate more than a small multiple
// of the reply's own size.
func (r *PacketReader) checkCount(count, minSize int, what string) bool {
	if r.err != nil {
		return false
	}
	if count < 0 || count > r.bytesRemaining()/minSize {
		r.fail("%s %d at offset %d does not fit the %d bytes remaining (each needs at least %d bytes)",
			what, count, r.offset, r.bytesRemaining(), minSize)
		return false
	}
	return true
}

func (r *PacketReader) parseByte() byte {
	b := r.take(SizeByte)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *PacketReader) parseShort() int16 {
	b := r.take(SizeShort)
	if b == nil {
		return 0
	}
	return int16(binary.BigEndian.Uint16(b)) // #nosec G115 -- bit-preserving decode of a signed CAS SHORT (two's complement)
}

func (r *PacketReader) parseInt() int32 {
	b := r.take(SizeInt)
	if b == nil {
		return 0
	}
	return int32(binary.BigEndian.Uint32(b)) // #nosec G115 -- bit-preserving decode of a signed CAS INT (two's complement)
}

func (r *PacketReader) parseLong() int64 {
	b := r.take(SizeLong)
	if b == nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b)) // #nosec G115 -- bit-preserving decode of a signed CAS BIGINT (two's complement)
}

func (r *PacketReader) parseFloat() float32 {
	b := r.take(SizeFloat)
	if b == nil {
		return 0
	}
	return math.Float32frombits(binary.BigEndian.Uint32(b))
}

func (r *PacketReader) parseDouble() float64 {
	b := r.take(SizeDouble)
	if b == nil {
		return 0
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b))
}

// parseRawBytes returns a copy of the next count bytes, or nil when count is
// negative or exceeds the bytes remaining (see take).
func (r *PacketReader) parseRawBytes(count int) []byte {
	b := r.take(count)
	if b == nil {
		return nil
	}
	v := make([]byte, len(b))
	copy(v, b)
	return v
}

// parseNullTermString reads length bytes and strips the trailing null byte.
func (r *PacketReader) parseNullTermString(length int) string {
	if length <= 0 {
		return ""
	}
	data := r.parseRawBytes(length)
	if len(data) > 0 && data[len(data)-1] == 0x00 {
		data = data[:len(data)-1]
	}
	return string(data)
}

func (r *PacketReader) parseDate() time.Time {
	year := int(r.parseShort())
	month := int(r.parseShort())
	day := int(r.parseShort())
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func (r *PacketReader) parseTime() time.Time {
	hour := int(r.parseShort())
	min := int(r.parseShort())
	sec := int(r.parseShort())
	return time.Date(0, 1, 1, hour, min, sec, 0, time.UTC)
}

func (r *PacketReader) parseTimestamp() time.Time {
	year := int(r.parseShort())
	month := int(r.parseShort())
	day := int(r.parseShort())
	hour := int(r.parseShort())
	min := int(r.parseShort())
	sec := int(r.parseShort())
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
}

func (r *PacketReader) parseDatetime() time.Time {
	year := int(r.parseShort())
	month := int(r.parseShort())
	day := int(r.parseShort())
	hour := int(r.parseShort())
	min := int(r.parseShort())
	sec := int(r.parseShort())
	ms := int(r.parseShort())
	return time.Date(year, time.Month(month), day, hour, min, sec, ms*1e6, time.UTC)
}

func (r *PacketReader) bytesRemaining() int {
	return len(r.buf) - r.offset
}

// readError reads an error response body: int32 code + null-term message.
func (r *PacketReader) readError(responseLength int) (int32, string) {
	code := r.parseInt()
	msgSize := responseLength - SizeInt
	msg := r.parseNullTermString(msgSize)
	return code, msg
}
