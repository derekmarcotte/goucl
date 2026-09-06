package core

import (
	"errors"
	"strings"
	"testing"
)

// The number grammar is the single most quirk-dense part of libucl, and the
// quirks are load-bearing: "10m" is ten million, not ten minutes, and a
// malformed number silently stays a string. Every row here is derived from
// docs/reference/object-model.md section 5.3-5.4 and checked against src/ucl_parser.c.
func TestMaybeParseNumber(t *testing.T) {
	const (
		dbl   = true
		nodbl = false
		byts  = true
		nobyt = false
		tim   = true
		notim = false
	)

	tests := []struct {
		name        string
		in          string
		allowDouble bool
		numberBytes bool
		allowTime   bool
		wantType    Type
		wantInt     int64
		wantFloat   float64
		wantPos     int
		wantErr     error
	}{
		// Plain integers.
		{"int", "10", dbl, nobyt, tim, TypeInt, 10, 0, 2, nil},
		{"negative", "-10", dbl, nobyt, tim, TypeInt, -10, 0, 3, nil},
		{"zero", "0", dbl, nobyt, tim, TypeInt, 0, 0, 1, nil},
		// libucl never reads a leading zero as octal: strtoimax is called with
		// an explicit base of 10.
		{"leading zero is decimal", "0755", dbl, nobyt, tim, TypeInt, 755, 0, 4, nil},

		// Hex. Detecting 'x' clears allowDouble, so hex is always an integer.
		{"hex", "0x1f", dbl, nobyt, tim, TypeInt, 31, 0, 4, nil},
		{"hex upper", "0X1F", dbl, nobyt, tim, TypeInt, 31, 0, 4, nil},
		{"hex negative", "-0x1f", dbl, nobyt, tim, TypeInt, -31, 0, 5, nil},
		// A decimal digit is what sets validStart, so a bare "xFF" is not a
		// number even though every byte after the x is a hex digit.
		{"bare hex rejected", "xFF", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"empty hex rejected", "0x", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		// The digit run restarts after the 'x', so the leading digits are lost.
		{"digits before x are discarded", "12x34", dbl, nobyt, tim, TypeInt, 0x34, 0, 5, nil},

		// Floats.
		{"float", "10.5", dbl, nobyt, tim, TypeFloat, 0, 10.5, 4, nil},
		{"exponent", "1e3", dbl, nobyt, tim, TypeFloat, 0, 1000, 3, nil},
		{"exponent sign", "1e-3", dbl, nobyt, tim, TypeFloat, 0, 0.001, 4, nil},
		{"double dot rejected", "1.2.3", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"double exponent rejected", "1e3e4", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"truncated exponent rejected", "1e", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"dangling exponent sign rejected", "1e+", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"dot without allowDouble rejected", "1.5", nodbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},

		// Plain k/m/g multipliers (decimal).
		{"k", "10k", dbl, nobyt, tim, TypeInt, 10000, 0, 3, nil},
		{"m is mega, never minutes", "10m", dbl, nobyt, tim, TypeInt, 10000000, 0, 3, nil},
		{"g", "10g", dbl, nobyt, tim, TypeInt, 10000000000, 0, 3, nil},
		{"K uppercase", "10K", dbl, nobyt, tim, TypeInt, 10000, 0, 3, nil},
		{"multiplier on a float scales the float", "10.5k", dbl, nobyt, tim, TypeFloat, 0, 10500, 5, nil},

		// Byte multipliers (binary).
		{"k as bytes", "10k", dbl, byts, tim, TypeInt, 10240, 0, 3, nil},
		{"kb", "10kb", dbl, nobyt, tim, TypeInt, 10240, 0, 4, nil},
		{"mb", "10mb", dbl, nobyt, tim, TypeInt, 10485760, 0, 4, nil},
		{"gb", "1gb", dbl, nobyt, tim, TypeInt, 1073741824, 0, 3, nil},
		{"float demoted by a byte suffix", "10.5kb", dbl, nobyt, tim, TypeInt, 10240, 0, 6, nil},
		// Quirk: with numberBytes set the byte after k/m/g is swallowed
		// whatever it is.
		{"numberBytes swallows the next byte", "10k;", dbl, byts, tim, TypeInt, 10240, 0, 4, nil},

		// Time.
		{"seconds", "10s", dbl, nobyt, tim, TypeTime, 0, 10, 3, nil},
		{"milliseconds", "10ms", dbl, nobyt, tim, TypeTime, 0, 0.01, 4, nil},
		{"minutes need the min spelling", "10min", dbl, nobyt, tim, TypeTime, 0, 600, 5, nil},
		{"hours", "10h", dbl, nobyt, tim, TypeTime, 0, 36000, 3, nil},
		{"days", "10d", dbl, nobyt, tim, TypeTime, 0, 864000, 3, nil},
		{"weeks", "10w", dbl, nobyt, tim, TypeTime, 0, 6048000, 3, nil},
		{"years are 365 days", "10y", dbl, nobyt, tim, TypeTime, 0, 315360000, 3, nil},
		{"uppercase time suffix", "10H", dbl, nobyt, tim, TypeTime, 0, 36000, 3, nil},
		// The ms/ks/gs branch never consults allowTime, so "ks" is a time even
		// when times are disabled.
		{"ks is kiloseconds", "10ks", dbl, nobyt, notim, TypeTime, 0, 10000, 4, nil},
		// Without allowDouble a time collapses to the unscaled integer.
		{"time without allowDouble is a bare int", "10s", nodbl, nobyt, tim, TypeInt, 10, 0, 3, nil},
		{"time suffix without allowTime rejected", "10h", dbl, nobyt, notim, TypeNull, 0, 0, 0, errNotNumber},

		// Atom ends and trailing junk.
		{"prefix up to an atom end", "10;xyz", dbl, nobyt, tim, TypeInt, 10, 0, 2, nil},
		{"prefix up to a comma", "10,", dbl, nobyt, tim, TypeInt, 10, 0, 2, nil},
		{"trailing whitespace", "10 ", dbl, nobyt, tim, TypeInt, 10, 0, 3, nil},
		{"trailing whitespace then junk", "10 x", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"trailing letters rejected", "10abc", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"suffix followed by junk rejected", "10kbx", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},

		// Degenerate inputs.
		{"empty", "", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"minus only", "-", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},
		{"letters", "abc", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNotNumber},

		// Overflow.
		{"int overflow", "99999999999999999999", dbl, nobyt, tim, TypeNull, 0, 0, 0, errNumberRange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := NewObject()
			pos, err := maybeParseNumber(obj, []byte(tc.in), tc.allowDouble, tc.numberBytes, tc.allowTime)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("maybeParseNumber(%q) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			if pos != tc.wantPos {
				t.Errorf("maybeParseNumber(%q) pos = %d, want %d", tc.in, pos, tc.wantPos)
			}
			if tc.wantErr != nil {
				if obj.Type() != TypeNull {
					t.Errorf("a failed parse must leave the object alone, got type %v", obj.Type())
				}
				return
			}
			if obj.Type() != tc.wantType {
				t.Fatalf("maybeParseNumber(%q) type = %v, want %v", tc.in, obj.Type(), tc.wantType)
			}
			switch tc.wantType {
			case TypeInt:
				if got := ToInt(obj); got != tc.wantInt {
					t.Errorf("maybeParseNumber(%q) = %d, want %d", tc.in, got, tc.wantInt)
				}
			case TypeFloat, TypeTime:
				if got := ToDouble(obj); got != tc.wantFloat {
					t.Errorf("maybeParseNumber(%q) = %v, want %v", tc.in, got, tc.wantFloat)
				}
			}
		})
	}
}

// The C copies the literal into a 128-byte stack buffer and gives up when it
// would not fit, so a long literal is "not a number" rather than out of range.
func TestMaybeParseNumberLengthLimit(t *testing.T) {
	if _, err := maybeParseNumber(nil, []byte(strings.Repeat("9", 127)), true, false, true); !errors.Is(err, errNotNumber) {
		t.Errorf("a 127-character literal must be rejected outright, got %v", err)
	}
	if _, err := maybeParseNumber(nil, []byte(strings.Repeat("9", 126)), true, false, true); !errors.Is(err, errNumberRange) {
		t.Errorf("a 126-character literal must reach the conversion and overflow, got %v", err)
	}
}

// The C's *pos on the final fallthrough is c, not start -- which for a
// negative literal is one past the sign.
func TestMaybeParseNumberFailurePosition(t *testing.T) {
	pos, err := maybeParseNumber(nil, []byte("-10abc"), true, false, true)
	if !errors.Is(err, errNotNumber) {
		t.Fatalf("err = %v, want errNotNumber", err)
	}
	if pos != 1 {
		t.Errorf("pos = %d, want 1 (the byte after the sign)", pos)
	}
}

// Exactly six words are booleans, and nothing else is.
func TestMaybeParseBoolean(t *testing.T) {
	tests := []struct {
		in      string
		wantOK  bool
		wantVal bool
	}{
		{"true", true, true},
		{"TRUE", true, true},
		{"True", true, true},
		{"false", true, false},
		{"FALSE", true, false},
		{"yes", true, true},
		{"YES", true, true},
		{"no", true, false},
		{"NO", true, false},
		{"on", true, true},
		{"ON", true, true},
		{"off", true, false},
		{"OFF", true, false},

		// Everything a more generous parser would accept, and does not here.
		{"y", false, false},
		{"n", false, false},
		{"t", false, false},
		{"f", false, false},
		{"1", false, false},
		{"0", false, false},
		{"enabled", false, false},
		{"disabled", false, false},
		{"tru", false, false},
		{"truex", false, false},
		{" true", false, false},
		{"true ", false, false},
		{"", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			obj := NewObject()
			got := maybeParseBoolean(obj, []byte(tc.in))
			if got != tc.wantOK {
				t.Fatalf("maybeParseBoolean(%q) = %v, want %v", tc.in, got, tc.wantOK)
			}
			if !tc.wantOK {
				if obj.Type() != TypeNull {
					t.Errorf("a non-match must leave the object alone, got %v", obj.Type())
				}
				return
			}
			if obj.Type() != TypeBoolean {
				t.Fatalf("type = %v, want TypeBoolean", obj.Type())
			}
			if got := ToBoolean(obj); got != tc.wantVal {
				t.Errorf("maybeParseBoolean(%q) value = %v, want %v", tc.in, got, tc.wantVal)
			}
		})
	}
}

// maybeParseBoolean accepts a nil object as a pure predicate.
func TestMaybeParseBooleanNilObject(t *testing.T) {
	if !maybeParseBoolean(nil, []byte("on")) {
		t.Error("maybeParseBoolean(nil, \"on\") must still report a match")
	}
}

func TestLexNumMultiplier(t *testing.T) {
	tests := []struct {
		c        byte
		isBytes  bool
		expected int64
	}{
		{'k', false, 1000}, {'k', true, 1024},
		{'K', false, 1000}, {'K', true, 1024},
		{'m', false, 1000000}, {'m', true, 1048576},
		{'g', false, 1000000000}, {'g', true, 1073741824},
		{'x', false, 1}, {'x', true, 1},
	}
	for _, tc := range tests {
		if got := lexNumMultiplier(tc.c, tc.isBytes); got != tc.expected {
			t.Errorf("lexNumMultiplier(%q, %v) = %d, want %d", tc.c, tc.isBytes, got, tc.expected)
		}
	}
}

func TestLexTimeMultiplier(t *testing.T) {
	tests := []struct {
		c        byte
		expected float64
	}{
		{'m', 60}, {'h', 3600}, {'d', 86400}, {'w', 604800},
		// 365 days exactly, not 365.25.
		{'y', 31536000}, {'Y', 31536000},
		{'q', 1},
	}
	for _, tc := range tests {
		if got := lexTimeMultiplier(tc.c); got != tc.expected {
			t.Errorf("lexTimeMultiplier(%q) = %v, want %v", tc.c, got, tc.expected)
		}
	}
}

// libucl's escaper is deliberately narrow: it does not \u-escape arbitrary
// control characters, and it leaves the space byte alone even though space is
// in the whitespace-unsafe class that selects it for escaping.
func TestEscapeJSONString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"quote", `a"b`, `a\"b`},
		{"backslash", `a\b`, `a\\b`},
		{"newline", "a\nb", `a\nb`},
		{"carriage return", "a\rb", `a\rb`},
		{"tab", "a\tb", `a\tb`},
		{"backspace", "a\bb", `a\bb`},
		{"form feed", "a\fb", `a\fb`},
		{"nul", "a\x00b", "a\\u0000b"},
		{"vertical tab is capital B", "a\vb", "a\\u000Bb"},
		{"space is untouched", "a b", "a b"},
		{"other control bytes are verbatim", "a\x01b", "a\x01b"},
		{"high bytes are verbatim", "a\xffb", "a\xffb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(escapeJSONString([]byte(tc.in))); got != tc.want {
				t.Errorf("escapeJSONString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The character classes are the ones the parser, the emitter and the key
// escape flag all share; a wrong member is a silent behaviour change.
func TestCharacterClasses(t *testing.T) {
	classes := []struct {
		name    string
		fn      func(byte) bool
		members []byte
	}{
		{"atom end", isAtomEnd, []byte{0x00, '\n', '\r', '#', ',', ';', ']', '}'}},
		{"whitespace", isWhitespace, []byte{'\t', ' '}},
		{"whitespace unsafe", isWhitespaceUnsafe, []byte{'\t', '\n', '\v', '\f', '\r', ' '}},
		{"json unsafe", isJSONUnsafe, []byte{0x00, '\b', '\t', '\n', '\f', '\r', '"', '\\'}},
		{"ucl unsafe", isUCLUnsafe, []byte{
			0x00, '\b', '\t', '\n', '\f', '\r', ' ', '"', '+', ':', '=', '[', '\\', '{',
		}},
	}

	for _, cl := range classes {
		t.Run(cl.name, func(t *testing.T) {
			want := map[byte]bool{}
			for _, c := range cl.members {
				want[c] = true
			}
			for i := 0; i < 256; i++ {
				c := byte(i)
				if got := cl.fn(c); got != want[c] {
					t.Errorf("%s(%#02x) = %v, want %v", cl.name, c, got, want[c])
				}
			}
		})
	}
}

func TestCStrtoul(t *testing.T) {
	tests := []struct {
		in       string
		wantVal  uint64
		wantEnd  int
		wantNone bool
	}{
		{"12", 12, 2, false},
		{"12.3", 12, 2, false},
		{"12abc", 12, 2, false},
		{"abc", 0, 0, true},
		{"", 0, 0, true},
		{"  7", 7, 3, false},
		{"+7", 7, 2, false},
	}
	for _, tc := range tests {
		v, n := cStrtoul(tc.in)
		if n != tc.wantEnd || (!tc.wantNone && v != tc.wantVal) {
			t.Errorf("cStrtoul(%q) = (%d, %d), want (%d, %d)", tc.in, v, n, tc.wantVal, tc.wantEnd)
		}
	}
}
