package cubrid

import (
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"testing"
)

// ─── Pure escaping logic ────────────────────────────────────────────────────

func TestEscapeStringLiteralModeLeavesBackslashUntouched(t *testing.T) {
	// no_backslash_escapes=yes (CUBRID default): a backslash is an ordinary
	// character; only the single quote is doubled.
	got, err := escapeString(`C:\temp\a'b`, stringEscapeModeLiteralBackslash)
	if err != nil {
		t.Fatalf("escapeString returned error: %v", err)
	}
	want := `C:\temp\a''b`
	if got != want {
		t.Fatalf("literal-mode escaping\nwant: %q\n got: %q", want, got)
	}
}

func TestEscapeStringBackslashModeDoublesBackslash(t *testing.T) {
	// Backslash-escape mode: backslash and quote are both special, and CR/LF
	// are backslash-escaped.
	got, err := escapeString("a\\b'c\r\n", stringEscapeModeBackslashEscapes)
	if err != nil {
		t.Fatalf("escapeString returned error: %v", err)
	}
	want := "a\\\\b''c\\\r\\\n"
	if got != want {
		t.Fatalf("escape-mode escaping\nwant: %q\n got: %q", want, got)
	}
}

func TestEscapeStringRejectsNullByte(t *testing.T) {
	_, err := escapeString("a\x00b", stringEscapeModeLiteralBackslash)
	if err == nil {
		t.Fatalf("expected error for null byte")
	}
	var progErr *ProgrammingError
	if !errors.As(err, &progErr) {
		t.Fatalf("expected *ProgrammingError, got %T: %v", err, err)
	}
}

func TestEscapeStringRejectsCtrlZ(t *testing.T) {
	_, err := escapeString("a\x1ab", stringEscapeModeBackslashEscapes)
	if err == nil {
		t.Fatalf("expected error for Ctrl-Z byte")
	}
	var progErr *ProgrammingError
	if !errors.As(err, &progErr) {
		t.Fatalf("expected *ProgrammingError, got %T: %v", err, err)
	}
}

func TestEscapeStringUnknownModeIsRejected(t *testing.T) {
	_, err := escapeString("safe", stringEscapeModeUnknown)
	if err == nil {
		t.Fatalf("expected error when escaping with unknown mode")
	}
}

func TestFormatValueWithModeQuotesString(t *testing.T) {
	got, err := FormatValueWithMode(`a\b`, stringEscapeModeLiteralBackslash)
	if err != nil {
		t.Fatalf("FormatValueWithMode returned error: %v", err)
	}
	if got != `'a\b'` {
		t.Fatalf("unexpected literal: %q", got)
	}

	got, err = FormatValueWithMode(`a\b`, stringEscapeModeBackslashEscapes)
	if err != nil {
		t.Fatalf("FormatValueWithMode returned error: %v", err)
	}
	if got != `'a\\b'` {
		t.Fatalf("unexpected literal: %q", got)
	}
}

func TestInterpolateArgsDefaultsToLiteralBackslashMode(t *testing.T) {
	// The conn-less InterpolateArgs (used by GORM Explain) must default to the
	// CUBRID-default literal-backslash mode, i.e. NOT double backslashes.
	got, err := InterpolateArgs("UPDATE t SET p = ?", []driver.Value{`C:\x`})
	if err != nil {
		t.Fatalf("InterpolateArgs returned error: %v", err)
	}
	if got != `UPDATE t SET p = 'C:\x'` {
		t.Fatalf("unexpected SQL: %q", got)
	}
}

// ─── Negotiation over the wire ──────────────────────────────────────────────

func scriptProbe(t *testing.T, server net.Conn, charLength int32) {
	t.Helper()
	consumeOneRequest(t, server)
	_, _ = server.Write(probeResponsePacket(charLength))
	// The probe closes its server-side query handle.
	consumeOneRequest(t, server)
	_, _ = server.Write(simpleResponsePacket(0))
}

func TestEnsureStringEscapeModeNegotiatesLiteral(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	c := &conn{socket: client, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering

	go scriptProbe(t, server, 2)

	if err := c.ensureStringEscapeModeLocked(); err != nil {
		t.Fatalf("ensureStringEscapeModeLocked error: %v", err)
	}
	if c.stringEscapeMode != stringEscapeModeLiteralBackslash {
		t.Fatalf("expected literal-backslash mode, got %v", c.stringEscapeMode)
	}

	// A second call must be a no-op (cached) and issue no further requests.
	if err := c.ensureStringEscapeModeLocked(); err != nil {
		t.Fatalf("second ensureStringEscapeModeLocked error: %v", err)
	}
}

func TestEnsureStringEscapeModeNegotiatesBackslashEscapes(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	c := &conn{socket: client, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1

	go scriptProbe(t, server, 1)

	if err := c.ensureStringEscapeModeLocked(); err != nil {
		t.Fatalf("ensureStringEscapeModeLocked error: %v", err)
	}
	if c.stringEscapeMode != stringEscapeModeBackslashEscapes {
		t.Fatalf("expected backslash-escape mode, got %v", c.stringEscapeMode)
	}
}

func TestEnsureStringEscapeModeRejectsUnexpectedProbeResult(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	c := &conn{socket: client, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1

	go scriptProbe(t, server, 3)

	err := c.ensureStringEscapeModeLocked()
	if err == nil {
		t.Fatalf("expected error for unexpected probe result")
	}
	if !strings.Contains(err.Error(), "no_backslash_escapes") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if c.stringEscapeMode != stringEscapeModeUnknown {
		t.Fatalf("mode must stay unknown after a failed probe, got %v", c.stringEscapeMode)
	}
}
