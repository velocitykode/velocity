package sanitize

import (
	"strings"
	"sync"
	"testing"
)

// TestString_PassthroughPrintable verifies that strings containing only
// printable ASCII (and TAB) are returned without rewriting. This is the
// allocation-free fast path drivers depend on for hot-loop calls.
func TestString_PassthroughPrintable(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"GET /users/123",
		"col1\tcol2\tcol3",
		"key=value foo=bar",
		"unicode-passthrough: café résumé",
		"unicode-passthrough: Zoë 🚀",
	}
	for _, in := range cases {
		got := String(in)
		if got != in {
			t.Errorf("String(%q) = %q, want passthrough", in, got)
		}
	}
}

// TestString_EscapesCRLF is the core CRLF-injection defence: literal
// \r and \n in the input must become \x0d / \x0a so a single record
// can never break across log lines.
func TestString_EscapesCRLF(t *testing.T) {
	in := "before\r\nafter"
	got := String(in)
	want := `before\x0d\x0aafter`
	if got != want {
		t.Errorf("String(%q) = %q, want %q", in, got, want)
	}
}

// TestString_EscapesUnicodeLineBreaks covers UTF-8 encoded separators
// that some log viewers, JSON-line tooling, SIEM normalisers, and
// JavaScript-based UIs treat as line breaks even though they are not
// ASCII CR/LF. They must be escaped to keep one input record to one
// emitted log line.
func TestString_EscapesUnicodeLineBreaks(t *testing.T) {
	cases := []struct {
		name string
		sep  string
		want string
	}{
		{"nel", "\u0085", `before\u0085after`},
		{"line-separator", "\u2028", `before\u2028after`},
		{"paragraph-separator", "\u2029", `before\u2029after`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := "before" + tc.sep + "after"
			got := String(in)
			if got != tc.want {
				t.Fatalf("String(%q) = %q, want %q", in, got, tc.want)
			}
			if strings.Contains(got, tc.sep) {
				t.Errorf("String(%q) retained raw Unicode line break: %q", in, got)
			}
		})
	}
}

// TestString_EscapesANSI verifies ESC (0x1B) and CSI single-byte
// (0x9B) are both escaped. Terminal viewers tailing a log file
// interpret either to drive cursor / colour / window-title control
// sequences, so both must be neutralised.
func TestString_EscapesANSI(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1b[2J", `\x1b[2J`},
		{"\x9b[2J", `\x9b[2J`},
		{"\x1b]0;evil\x07", `\x1b]0;evil\x07`},
	}
	for _, tc := range cases {
		got := String(tc.in)
		if got != tc.want {
			t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestString_PreservesTab confirms that TAB (0x09) is the lone
// sub-0x20 byte that passes through, so structured columnar logs
// remain aligned.
func TestString_PreservesTab(t *testing.T) {
	in := "a\tb\tc"
	got := String(in)
	if got != in {
		t.Errorf("String(%q) = %q, want passthrough (TAB must survive)", in, got)
	}
}

// TestString_EscapesAllOtherControls walks every sub-0x20 byte plus
// 0x7F and confirms that, except for TAB, every one is rewritten to
// a \xHH form. Belt-and-braces against a future bug that exempts a
// new byte.
func TestString_EscapesAllOtherControls(t *testing.T) {
	for c := 0; c < 0x20; c++ {
		if c == '\t' {
			continue
		}
		in := string([]byte{byte(c)})
		got := String(in)
		if !strings.HasPrefix(got, `\x`) {
			t.Errorf("String(0x%02x) = %q, want \\xHH escape", c, got)
		}
	}
	if got := String("\x7f"); got != `\x7f` {
		t.Errorf("String(DEL) = %q, want %q", got, `\x7f`)
	}
}

// TestString_PreservesHighBytes confirms that non-ASCII bytes 0x80
// through 0xFF (UTF-8 continuation bytes, plus 0xA0-0xFF printables
// in legacy encodings) pass through except for 0x9B. This is what
// keeps "café" intact in the logged URL.
func TestString_PreservesHighBytes(t *testing.T) {
	for c := 0x80; c <= 0xff; c++ {
		if c == 0x9b {
			continue
		}
		in := string([]byte{byte(c)})
		got := String(in)
		if got != in {
			t.Errorf("String(0x%02x) = %q, want passthrough", c, got)
		}
	}
}

// TestString_RuneAwareCSI covers the 0x9B handling regression: the byte
// 0x9B is a legal UTF-8 continuation byte, so it must only be escaped
// when it is a bare/invalid byte or the real Unicode CSI control
// (U+009B), never when it is part of an unrelated valid multi-byte rune.
func TestString_RuneAwareCSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// U+011B 'ě' encodes as 0xC4 0x9B. The 0x9B is a continuation
		// byte of a valid rune and must survive byte-for-byte.
		{"continuation-byte-passthrough", "\xc4\x9b", "\xc4\x9b"},
		{"continuation-byte-in-word", "d\xc4\x9bkuji", "d\xc4\x9bkuji"},
		// Bare 0x9B is an invalid lone byte: keep the single-byte escape.
		{"bare-9b", "\x9b", `\x9b`},
		// U+009B (0xC2 0x9B) is the real Unicode CSI control.
		{"unicode-csi", "\xc2\x9b", "\\u009b"},
		{"unicode-csi-in-context", "x\xc2\x9by", "x\\u009by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValue_DelegatesToString documents that Value is an alias
// today; the test fails immediately if a future change diverges them
// without updating drivers that import Value.
func TestValue_DelegatesToString(t *testing.T) {
	for _, in := range []string{"plain", "with\nnewline", "\x1bansi", ""} {
		if String(in) != Value(in) {
			t.Errorf("Value(%q) diverged from String", in)
		}
	}
}

// TestKV_BothHalvesSanitised guards against a driver that sanitises
// the value but trusts the key. A CRLF in the key forges a log line
// just as effectively, so KV must sanitise both.
func TestKV_BothHalvesSanitised(t *testing.T) {
	k, v := KV("user\nname", "value\rcrlf")
	if k != `user\x0aname` {
		t.Errorf("KV key = %q, want %q", k, `user\x0aname`)
	}
	if v != `value\x0dcrlf` {
		t.Errorf("KV value = %q, want %q", v, `value\x0dcrlf`)
	}
}

// TestString_DecodedURLPath replays the exact threat from the audit:
// /vulnpath%0aINJECT decodes via net/url to /vulnpath\nINJECT in
// r.URL.Path. The sanitiser must collapse the embedded newline so
// the log record cannot be forged.
func TestString_DecodedURLPath(t *testing.T) {
	in := "/vulnpath\n[2026-01-01] FATAL: Database deleted"
	got := String(in)
	if strings.Contains(got, "\n") {
		t.Errorf("String(%q) retained a literal LF: %q", in, got)
	}
	if !strings.Contains(got, `\x0a`) {
		t.Errorf("String(%q) = %q, want \\x0a escape", in, got)
	}
}

// TestString_ConcurrentSafe sanity-checks that String has no shared
// mutable state. It is a pure function over its input, but calling
// it from many goroutines while the race detector is on documents
// that fact for future maintainers (drivers fan out under -race).
func TestString_ConcurrentSafe(t *testing.T) {
	in := "hello\nworld\x1bansi"
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got := String(in); !strings.Contains(got, `\x0a`) {
					t.Errorf("String(%q) lost escape under concurrency: %q", in, got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
