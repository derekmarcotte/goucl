package core

import "testing"

// The string-length invariant.
//
// A TypeString object carries its length in two places: the tracked length
// field (which the C reads as obj->len) and the sv slice. Components that
// build string objects must set both, because different parts of the package
// read different ones -- ToLStringSafe returns length raw, while the emitters
// use len(sv). A constructor that sets sv but forgets length produces an
// object that emits its full text but converts to "".
//
// The one legitimate exception is Copy of a binary string: the C duplicates
// the value with strdup, which stops at the first NUL, while leaving len
// untouched. There length deliberately exceeds len(sv), and readers clamp.
//
// These tests exist so the parser, msgpack and sexp components -- all of which
// create strings -- fail loudly rather than silently if they skip length.

// assertStringLen checks the invariant for an object built by a constructor.
func assertStringLen(t *testing.T, what string, o *Object) {
	t.Helper()
	if o == nil {
		t.Fatalf("%s: returned nil", what)
	}
	if o.ObjType != TypeString {
		return // reclassified to a number or boolean; length is the string length by design
	}
	if int(o.length) != len(o.sv) {
		t.Errorf("%s: length = %d but len(sv) = %d; every string constructor must set both",
			what, o.length, len(o.sv))
	}
}

func TestStringConstructorsSetLength(t *testing.T) {
	cases := []struct {
		name string
		obj  *Object
	}{
		{`FromString("hello")`, FromString("hello")},
		{`FromString("")`, FromString("")},
		{`FromLString([]byte("bytes"))`, FromLString([]byte("bytes"))},
		{`FromStringCommon raw`, FromStringCommon([]byte("raw value"), StringRaw)},
		{`FromStringCommon trim`, FromStringCommon([]byte("  padded  "), StringTrim)},
		{`FromStringCommon escape`, FromStringCommon([]byte("a\"b\\c"), StringEscape)},
		{`FromStringCommon all-whitespace trim`, FromStringCommon([]byte("    "), StringTrim)},
		{`FromStringCommon embedded NUL`, FromStringCommon([]byte("a\x00b"), StringRaw)},
		{`FromStringCommon non-number`, FromStringCommon([]byte("10abc"), StringParse)},
	}
	for _, c := range cases {
		assertStringLen(t, c.name, c.obj)
	}
}

// A plain (non-binary) string must survive Copy with the invariant intact.
func TestCopyPreservesStringLength(t *testing.T) {
	orig := FromString("some text")
	assertStringLen(t, "original", orig)
	assertStringLen(t, "Copy", Copy(orig))
}

// Reading a string back must agree with what was put in, for every reader.
func TestStringReadersAgree(t *testing.T) {
	const text = "consistent"
	o := FromString(text)

	if got := ToString(o); got != text {
		t.Errorf("ToString = %q, want %q", got, text)
	}
	b, n := ToLString(o)
	if string(b[:n]) != text {
		t.Errorf("ToLString = %q, want %q", string(b[:n]), text)
	}
	if int(n) != len(text) {
		t.Errorf("ToLString length = %d, want %d", n, len(text))
	}
	// The emitters read len(sv); it must describe the same bytes.
	if len(o.sv) != len(text) {
		t.Errorf("len(sv) = %d, want %d", len(o.sv), len(text))
	}
}

// Copy of a binary string is the documented exception: strdup truncates at the
// first NUL while length carries over, so length exceeds len(sv) and every
// reader must clamp rather than slice out of range.
func TestCopyBinaryStringClampsRatherThanPanics(t *testing.T) {
	orig := FromStringCommon([]byte("before\x00after"), StringRaw)
	orig.flags |= FlagBinary

	cp := Copy(orig)
	if cp == nil {
		t.Fatal("Copy returned nil")
	}
	if cp.ObjType != TypeString {
		t.Fatalf("Copy changed the type to %v", cp.ObjType)
	}

	// Whatever the tracked length says, reading must not panic and must not
	// return more bytes than are actually present.
	got := ToString(cp)
	if len(got) > len(cp.sv) {
		t.Errorf("ToString returned %d bytes but only %d are present", len(got), len(cp.sv))
	}
	b, n := ToLString(cp)
	if int(n) > len(b) {
		// This is the C's out-of-bounds read. Go must clamp instead; a caller
		// slicing b[:n] would panic.
		t.Errorf("ToLString reported length %d with only %d bytes available; "+
			"readers must clamp to avoid an out-of-range slice", n, len(b))
	}
}
