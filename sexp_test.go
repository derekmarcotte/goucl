package goucl

import (
	"bytes"
	"errors"
	"testing"
)

// Ports tests/test_csexp.c: 19 cases, 8 valid-input and 11 error-input (6
// plain error cases plus 5 issue #365 regression cases). See docs/reference/sexp.md
// section 6 for the case-by-case description this file follows.

// --- valid-input cases -----------------------------------------------------

// test_single_element: (3:abc)
func TestCSexpSingleElement(t *testing.T) {
	top, err := DecodeCSexp([]byte("(3:abc)"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if top.Type() != TypeArray {
		t.Fatalf("top type = %v, want TypeArray", top.Type())
	}
	if got := ArraySize(top); got != 1 {
		t.Fatalf("array size = %d, want 1", got)
	}
	elem := ArrayHead(top)
	if elem == nil || elem.Type() != TypeString {
		t.Fatalf("element = %+v, want a TypeString", elem)
	}
	sv, slen := ToLString(elem)
	if slen != 3 || !bytes.Equal(sv, []byte("abc")) {
		t.Fatalf("element content = %q (len %d), want \"abc\" (len 3)", sv, slen)
	}
}

// test_multi_digit_length: (11:hello world)
func TestCSexpMultiDigitLength(t *testing.T) {
	top, err := DecodeCSexp([]byte("(11:hello world)"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if got := ArraySize(top); got != 1 {
		t.Fatalf("array size = %d, want 1", got)
	}
	sv, slen := ToLString(ArrayHead(top))
	if slen != 11 || !bytes.Equal(sv, []byte("hello world")) {
		t.Fatalf("content = %q (len %d), want \"hello world\" (len 11)", sv, slen)
	}
}

// test_multiple_elements: (3:abc3:def)
func TestCSexpMultipleElements(t *testing.T) {
	top, err := DecodeCSexp([]byte("(3:abc3:def)"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if got := ArraySize(top); got != 2 {
		t.Fatalf("array size = %d, want 2", got)
	}
	want := []string{"abc", "def"}
	for i, w := range want {
		elem := ArrayFindIndex(top, uint32(i))
		if elem == nil {
			t.Fatalf("element %d missing", i)
		}
		sv, slen := ToLString(elem)
		if slen != 3 || !bytes.Equal(sv, []byte(w)) {
			t.Fatalf("element %d = %q, want %q", i, sv, w)
		}
	}
}

// test_empty_list: ()
func TestCSexpEmptyList(t *testing.T) {
	top, err := DecodeCSexp([]byte("()"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if top.Type() != TypeArray {
		t.Fatalf("top type = %v, want TypeArray", top.Type())
	}
	if got := ArraySize(top); got != 0 {
		t.Fatalf("array size = %d, want 0", got)
	}
}

// test_nested_lists: ((3:abc)(4:test))
func TestCSexpNestedLists(t *testing.T) {
	top, err := DecodeCSexp([]byte("((3:abc)(4:test))"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if top.Type() != TypeArray {
		t.Fatalf("top type = %v, want TypeArray", top.Type())
	}
	if got := ArraySize(top); got != 2 {
		t.Fatalf("array size = %d, want 2", got)
	}

	inner := ArrayFindIndex(top, 0)
	if inner == nil || inner.Type() != TypeArray || ArraySize(inner) != 1 {
		t.Fatalf("element 0 = %+v, want a 1-element TypeArray", inner)
	}
	sv, slen := ToLString(ArrayHead(inner))
	if slen != 3 || !bytes.Equal(sv, []byte("abc")) {
		t.Fatalf("element 0 content = %q, want \"abc\"", sv)
	}

	inner = ArrayFindIndex(top, 1)
	if inner == nil || inner.Type() != TypeArray || ArraySize(inner) != 1 {
		t.Fatalf("element 1 = %+v, want a 1-element TypeArray", inner)
	}
	sv, slen = ToLString(ArrayHead(inner))
	if slen != 4 || !bytes.Equal(sv, []byte("test")) {
		t.Fatalf("element 1 content = %q, want \"test\"", sv)
	}
}

// test_binary_data: (3:\x00\x01\x02)
func TestCSexpBinaryData(t *testing.T) {
	top, err := DecodeCSexp([]byte{'(', '3', ':', 0x00, 0x01, 0x02, ')'})
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if got := ArraySize(top); got != 1 {
		t.Fatalf("array size = %d, want 1", got)
	}
	elem := ArrayHead(top)
	if elem.flags&FlagBinary == 0 {
		t.Fatalf("element missing FlagBinary")
	}
	sv, slen := ToLString(elem)
	if slen != 3 || sv[0] != 0x00 || sv[1] != 0x01 || sv[2] != 0x02 {
		t.Fatalf("content = %v, want [0 1 2]", sv)
	}
}

// test_zerocopy in the C exercises UCL_PARSER_ZEROCOPY; this port has no
// zerocopy variant (see DecodeCSexp's doc comment), so this simply confirms
// the same input still decodes correctly.
func TestCSexpZerocopyEquivalent(t *testing.T) {
	input := []byte("(3:abc)")
	top, err := DecodeCSexp(input)
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	sv, slen := ToLString(ArrayHead(top))
	if slen != 3 || !bytes.Equal(sv, []byte("abc")) {
		t.Fatalf("content = %q, want \"abc\"", sv)
	}
}

// test_deeply_nested: ((6:nested))
func TestCSexpDeeplyNested(t *testing.T) {
	top, err := DecodeCSexp([]byte("((6:nested))"))
	if err != nil {
		t.Fatalf("DecodeCSexp: %v", err)
	}
	if got := ArraySize(top); got != 1 {
		t.Fatalf("outer array size = %d, want 1", got)
	}
	inner := ArrayHead(top)
	if inner == nil || inner.Type() != TypeArray || ArraySize(inner) != 1 {
		t.Fatalf("inner = %+v, want a 1-element TypeArray", inner)
	}
	sv, slen := ToLString(ArrayHead(inner))
	if slen != 6 || !bytes.Equal(sv, []byte("nested")) {
		t.Fatalf("nested content = %q, want \"nested\"", sv)
	}
}

// --- error-input cases -------------------------------------------------

// TestCSexpErrors ports the six plain error-input cases and the five issue
// #365 regression cases from test_csexp.c: every one must fail cleanly
// (return a non-nil error, never panic).
func TestCSexpErrors(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		// test_err_truncated: declared length 15 but only 3 bytes available.
		{"truncated", []byte("(15:abc)")},
		// test_err_bad_start: no leading paren.
		{"bad_start", []byte("abc")},
		// test_err_bad_length_char: non-numeric length digit.
		{"bad_length_char", []byte("(a:abc)")},
		// test_err_zero_length: zero-length atoms are rejected.
		{"zero_length", []byte("(0:data)")},
		// test_err_extra_brace: extra unmatched closing paren.
		{"extra_brace", []byte("(3:abc))")},
		// test_err_unclosed: no closing paren at all.
		{"unclosed", []byte("(3:abc")},

		// Issue #365 regression tests.

		// test_err_value_after_close: a value token appears after the
		// outermost list has already closed.
		{"value_after_close", []byte("()3:abc")},
		// test_err_unclosed_nested: two opened, zero closed lists.
		{"unclosed_nested", []byte("((")},
		// test_err_unclosed_deeply_nested: three levels of unclosed nesting.
		{"unclosed_deeply_nested", []byte("(((")},
		// test_err_open_after_close: an empty list followed by another
		// unclosed open paren.
		{"open_after_close", []byte("()(")},
		// test_err_unclosed_with_data: unclosed lists where the inner,
		// never-appended array actually has a child element.
		{"unclosed_with_data", []byte("((3:abc")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj, err := DecodeCSexp(c.input)
			if err == nil {
				t.Fatalf("DecodeCSexp(%q) succeeded, want an error (got %+v)", c.input, obj)
			}
			if !errors.Is(err, ErrCSexp) {
				t.Errorf("error %v does not wrap ErrCSexp", err)
			}
		})
	}
}

// --- additional coverage beyond test_csexp.c --------------------------

// TestCSexpEmptyInput mirrors DecodeMsgPackFull's documented empty-input
// special case: a zero-length chunk never reaches the csexp state machine at
// all and produces an empty top-level object instead of an error.
func TestCSexpEmptyInput(t *testing.T) {
	top, err := DecodeCSexp(nil)
	if err != nil {
		t.Fatalf("DecodeCSexp(nil): %v", err)
	}
	if top.Type() != TypeObject {
		t.Fatalf("top type = %v, want TypeObject", top.Type())
	}
}

// TestCSexpNestingTooDeep exercises the sexpMaxNesting cap (UCL_MAX_NESTING,
// 1024) documented in docs/reference/sexp.md section 1 but not covered by
// test_csexp.c. 1024 levels of nesting must be accepted; 1025 must not.
func TestCSexpNestingTooDeep(t *testing.T) {
	ok := bytes.Repeat([]byte("("), sexpMaxNesting)
	ok = append(ok, bytes.Repeat([]byte(")"), sexpMaxNesting)...)
	if _, err := DecodeCSexp(ok); err != nil {
		t.Fatalf("DecodeCSexp at max nesting (%d): %v", sexpMaxNesting, err)
	}

	tooDeep := bytes.Repeat([]byte("("), sexpMaxNesting+1)
	tooDeep = append(tooDeep, bytes.Repeat([]byte(")"), sexpMaxNesting+1)...)
	if _, err := DecodeCSexp(tooDeep); err == nil {
		t.Fatalf("DecodeCSexp at %d levels of nesting succeeded, want an error", sexpMaxNesting+1)
	}
}

// TestCSexpPriority checks that DecodeCSexpFull's priority argument reaches
// every object the decode creates, mirroring how every object the C's
// ucl_parse_csexp builds inherits parser->chunks->priority.
func TestCSexpPriority(t *testing.T) {
	top, err := DecodeCSexpFull([]byte("((3:abc))"), 5)
	if err != nil {
		t.Fatalf("DecodeCSexpFull: %v", err)
	}
	if top.Priority() != 5 {
		t.Fatalf("top priority = %d, want 5", top.Priority())
	}
	inner := ArrayHead(top)
	if inner.Priority() != 5 {
		t.Fatalf("inner list priority = %d, want 5", inner.Priority())
	}
	if ArrayHead(inner).Priority() != 5 {
		t.Fatalf("atom priority = %d, want 5", ArrayHead(inner).Priority())
	}
}

// TestCSexpTooBigLength exercises the UINT32_MAX soft cap on an atom's
// declared length ("too big length of an element").
func TestCSexpTooBigLength(t *testing.T) {
	if _, err := DecodeCSexp([]byte("(4294967296:x)")); err == nil {
		t.Fatalf("DecodeCSexp with an over-UINT32_MAX length succeeded, want an error")
	}
}

// --- fuzz target -------------------------------------------------------

// FuzzDecodeCSexp seeds from every malformed case in test_csexp.c (plus the
// valid cases, so the fuzzer starts from well-formed input too) and asserts
// only that decoding never panics, regardless of how malformed the input is.
func FuzzDecodeCSexp(f *testing.F) {
	seeds := []string{
		"(3:abc)",
		"(11:hello world)",
		"(3:abc3:def)",
		"()",
		"((3:abc)(4:test))",
		"((6:nested))",
		"(15:abc)",
		"abc",
		"(a:abc)",
		"(0:data)",
		"(3:abc))",
		"(3:abc",
		"()3:abc",
		"((",
		"(((",
		"()(",
		"((3:abc",
		"",
		"(",
		")",
		":",
		"(:)",
		"(999999999999999999999999999999:x)",
		"(4294967296:x)",
		"(4294967295:x)",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Add([]byte{'(', '3', ':', 0x00, 0x01, 0x02, ')'})

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeCSexp panicked on %q: %v", data, r)
			}
		}()
		_, _ = DecodeCSexp(data)
	})
}
