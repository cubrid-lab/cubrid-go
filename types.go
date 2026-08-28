package cubrid

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StringEscapeMode selects how string parameters are escaped when they are
// interpolated into SQL literal text. CUBRID's escaping rules depend on the
// server system parameter no_backslash_escapes, so the correct mode must be
// negotiated per connection (see conn.ensureStringEscapeModeLocked).
type StringEscapeMode int

const (
	// stringEscapeModeUnknown means the server mode has not been negotiated yet.
	// It is never used for actual execution: callers must resolve a concrete
	// mode first, because guessing wrong silently corrupts every string param.
	stringEscapeModeUnknown StringEscapeMode = iota
	// stringEscapeModeLiteralBackslash is the CUBRID default
	// (no_backslash_escapes=yes): a backslash is an ordinary character and only
	// the single quote is doubled. This is the ANSI-SQL behaviour.
	stringEscapeModeLiteralBackslash
	// stringEscapeModeBackslashEscapes means backslash-escape processing is
	// active: the backslash is doubled and CR/LF are backslash-escaped, in
	// addition to doubling the single quote.
	stringEscapeModeBackslashEscapes
)

// InterpolateArgs replaces `?` placeholders with formatted argument literals
// using literal-backslash (ANSI-SQL) escaping, the CUBRID default. It is kept
// for conn-less callers such as the GORM dialector's Explain method, which
// produce debug output rather than executed SQL. Actual query execution goes
// through InterpolateArgsWithMode with the connection's negotiated mode.
func InterpolateArgs(sql string, args []driver.Value) (string, error) {
	return InterpolateArgsWithMode(sql, args, stringEscapeModeLiteralBackslash)
}

// InterpolateArgsWithMode is InterpolateArgs with an explicit escaping mode.
// The mode must be a concrete (non-unknown) value; execution call sites resolve
// it from the connection's negotiated no_backslash_escapes setting.
func InterpolateArgsWithMode(sql string, args []driver.Value, mode StringEscapeMode) (string, error) {
	placeholders := findBindPlaceholders(sql)
	if len(placeholders) != len(args) {
		return "", fmt.Errorf(
			"cubrid: expected %d bind args, got %d",
			len(placeholders), len(args),
		)
	}
	if len(placeholders) == 0 {
		return sql, nil
	}

	var sb strings.Builder
	prev := 0
	for i, pos := range placeholders {
		sb.WriteString(sql[prev:pos])
		formatted, err := FormatValueWithMode(args[i], mode)
		if err != nil {
			return "", err
		}
		sb.WriteString(formatted)
		prev = pos + 1
	}
	sb.WriteString(sql[prev:])
	return sb.String(), nil
}

func findBindPlaceholders(sql string) []int {
	const (
		scanNormal = iota
		scanSingleQuote
		scanBlockComment
		scanLineComment
	)

	state := scanNormal
	positions := make([]int, 0, 8)

	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		switch state {
		case scanNormal:
			switch ch {
			case '\'':
				state = scanSingleQuote
			case '/':
				if i+1 < len(sql) && sql[i+1] == '*' {
					state = scanBlockComment
					i++
				}
			case '-':
				if i+1 < len(sql) && sql[i+1] == '-' {
					state = scanLineComment
					i++
				}
			case '?':
				positions = append(positions, i)
			}
		case scanSingleQuote:
			if ch == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					continue
				}
				state = scanNormal
			}
		case scanBlockComment:
			if ch == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				state = scanNormal
				i++
			}
		case scanLineComment:
			if ch == '\n' {
				state = scanNormal
			}
		}
	}

	return positions
}

// FormatValue converts a driver.Value to a CUBRID SQL literal string using
// literal-backslash (ANSI-SQL) escaping, the CUBRID default. Kept for conn-less
// callers such as the GORM dialector's Explain method.
func FormatValue(v driver.Value) (string, error) {
	return FormatValueWithMode(v, stringEscapeModeLiteralBackslash)
}

// FormatValueWithMode is FormatValue with an explicit string-escaping mode.
func FormatValueWithMode(v driver.Value, mode StringEscapeMode) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch val := v.(type) {
	case bool:
		if val {
			return "1", nil
		}
		return "0", nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'g', -1, 64), nil
	case string:
		escaped, err := escapeString(val, mode)
		if err != nil {
			return "", err
		}
		return "'" + escaped + "'", nil
	case []byte:
		return "X'" + hexEncode(val) + "'", nil
	case time.Time:
		val = val.UTC()
		ms := val.Nanosecond() / 1e6
		return fmt.Sprintf("DATETIME'%s.%03d'", val.Format("2006-01-02 15:04:05"), ms), nil
	default:
		return "", fmt.Errorf("cubrid: unsupported value type %T", v)
	}
}

// namedValueToValue converts []driver.NamedValue to []driver.Value.
func namedValueToValue(named []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(named))
	for i, n := range named {
		out[i] = n.Value
	}
	return out
}

// escapeString escapes the inner content of a CUBRID string literal (WITHOUT
// the surrounding single quotes) according to the given escaping mode.
//
// NUL (0x00) and Ctrl-Z (0x1A) are ALWAYS rejected: CUBRID cannot store a NUL
// inside a string literal, and its SQL grammar defines no safe literal escape
// for 0x1A (there is no MySQL-style \Z). Emitting either as a raw control byte
// would corrupt the statement, so callers must use a []byte (X'..') parameter
// for values that may contain these bytes.
func escapeString(s string, mode StringEscapeMode) (string, error) {
	if strings.IndexByte(s, 0x00) >= 0 {
		return "", &ProgrammingError{CubridError{Code: -1,
			Message: "string parameter contains a null byte (0x00), which CUBRID cannot store in a string literal; use a []byte parameter for binary data"}}
	}
	if strings.IndexByte(s, 0x1A) >= 0 {
		return "", &ProgrammingError{CubridError{Code: -1,
			Message: "string parameter contains a Ctrl-Z byte (0x1A), which has no safe CUBRID string-literal escape; use a []byte parameter for binary data"}}
	}

	switch mode {
	case stringEscapeModeBackslashEscapes:
		// Escape mode: backslash introduces escape sequences.
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `''`)
		s = strings.ReplaceAll(s, "\r", "\\\r")
		s = strings.ReplaceAll(s, "\n", "\\\n")
		return s, nil
	case stringEscapeModeLiteralBackslash:
		// Literal mode (CUBRID default): only the single quote is special.
		return strings.ReplaceAll(s, `'`, `''`), nil
	default:
		return "", &ProgrammingError{CubridError{Code: -1,
			Message: "cubrid: string escaping mode not negotiated; cannot safely escape string parameter"}}
	}
}

// hexEncode returns the lowercase hex encoding of b.
func hexEncode(b []byte) string {
	const hx = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hx[c>>4]
		out[i*2+1] = hx[c&0x0f]
	}
	return string(out)
}
