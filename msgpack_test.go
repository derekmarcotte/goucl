package goucl

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// Ports tests/test_msgpack.c (randomized emit -> parse -> emit byte identity)
// and tests/test_msgpack_malformed.c (twelve inputs that must be rejected),
// plus fixed vectors for every row of the format table in docs/msgpack.md.
//
// Every expected byte string below was captured from the reference build in
// ../libucl/build unless a comment says otherwise.

// --- helpers ---------------------------------------------------------------

func mpDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex vector %q: %v", s, err)
	}
	return b
}

// mpEncodeHex encodes obj and returns the bytes as hex, failing the test if
// encoding reports an error.
func mpEncodeHex(t *testing.T, obj *Object) string {
	t.Helper()
	b, err := EncodeMsgPack(obj)
	if err != nil {
		t.Fatalf("EncodeMsgPack: %v", err)
	}
	return hex.EncodeToString(b)
}

// mpBinary returns a TypeString object carrying the binary flag, the way
// test_msgpack.c builds one (fromstring_common then |= UCL_OBJECT_BINARY).
func mpBinary(b []byte) *Object {
	o := FromLString(b)
	o.flags |= FlagBinary
	return o
}

// mpFill returns n bytes of 'a'..'z' repeated, matching the generator used to
// capture the string-length vectors from the C.
func mpFill(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// --- encoder: integers -----------------------------------------------------

// TestMsgPackEncodeIntWidths pins ucl_emitter_print_int_msgpack's width
// selection across every threshold.
func TestMsgPackEncodeIntWidths(t *testing.T) {
	cases := []struct {
		val  int64
		want string
	}{
		{0, "00"},
		{1, "01"},
		{127, "7f"},
		{128, "cc80"},
		{255, "ccff"},
		{256, "cd0100"},
		{65535, "cdffff"},
		{65536, "ce00010000"},
		{4294967295, "ceffffffff"},
		{4294967296, "cf0000000100000000"},
		{math.MaxInt64, "cf7fffffffffffffff"},
		{-1, "ff"},
		{-32, "e0"},
		{-33, "d0df"},
		{-127, "d081"},
		{-129, "d1ff7f"},
		{-32767, "d18001"},
		{-32769, "d2ffff7fff"},
		{-2147483647, "d280000001"},
		{-2147483649, "d3ffffffff7fffffff"},
		{math.MinInt64, "d38000000000000000"},
	}
	for _, c := range cases {
		if got := mpEncodeHex(t, FromInt(c.val)); got != c.want {
			t.Errorf("FromInt(%d) = %s, want %s", c.val, got, c.want)
		}
	}
}

// TestMsgPackEncodeNegativeWidthQuirk pins docs/findings.md #4.
//
// The C picks the width by comparing the magnitude against INT8_MAX,
// INT16_MAX and INT32_MAX rather than the matching _MIN magnitude, so the most
// negative value of each width is promoted to the next wider encoding even
// though it fits. This is deliberate: test_msgpack.c asserts byte identity of
// emit -> parse -> emit, so an encoder that picked the mathematically smallest
// width would disagree with the C on the wire. If this test starts failing
// because someone "fixed" the encoder, the fix is what is wrong.
func TestMsgPackEncodeNegativeWidthQuirk(t *testing.T) {
	cases := []struct {
		val        int64
		want, note string
	}{
		{-127, "d081", "largest magnitude that really does use int8"},
		{-128, "d1ff80", "fits int8, promoted to int16"},
		{-32767, "d18001", "largest magnitude that really does use int16"},
		{-32768, "d2ffff8000", "fits int16, promoted to int32"},
		{-2147483647, "d280000001", "largest magnitude that really does use int32"},
		{-2147483648, "d3ffffffff80000000", "fits int32, promoted to int64"},
		{math.MinInt64, "d38000000000000000", "bithack abs of MinInt64 stays negative-magnitude"},
	}
	for _, c := range cases {
		if got := mpEncodeHex(t, FromInt(c.val)); got != c.want {
			t.Errorf("FromInt(%d) = %s, want %s (%s)", c.val, got, c.want, c.note)
		}
	}
}

// --- encoder: everything else ----------------------------------------------

func TestMsgPackEncodeScalars(t *testing.T) {
	cases := []struct {
		name string
		obj  *Object
		want string
	}{
		{"null", TypedNew(TypeNull), "c0"},
		{"true", FromBool(true), "c3"},
		{"false", FromBool(false), "c2"},
		{"double 0.0", FromDouble(0), "cb0000000000000000"},
		{"double 1.5", FromDouble(1.5), "cb3ff8000000000000"},
		{"double -1.5e300", FromDouble(-1.5e300), "cbfe41eb2d66005835"},
		{"str 0", FromLString([]byte{}), "a0"},
		{"str 2", FromString("hi"), "a26869"},
		{"str 31", FromLString(mpFill(31)), "bf" + hex.EncodeToString(mpFill(31))},
		{"str 32", FromLString(mpFill(32)), "d920" + hex.EncodeToString(mpFill(32))},
		{"bin 0", mpBinary([]byte{}), "c400"},
		{"bin 2", mpBinary([]byte("hi")), "c4026869"},
		{"bin 32", mpBinary(mpFill(32)), "c420" + hex.EncodeToString(mpFill(32))},
	}
	for _, c := range cases {
		if got := mpEncodeHex(t, c.obj); got != c.want {
			t.Errorf("%s = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestMsgPackEncodeStringLengthBoundaries checks the header the str and bin
// families choose at each width change. Only the header is compared; the
// payload is the same in both directions.
func TestMsgPackEncodeStringLengthBoundaries(t *testing.T) {
	cases := []struct {
		n      int
		binary bool
		want   string
	}{
		{31, false, "bf"},
		{32, false, "d920"},
		{255, false, "d9ff"},
		{256, false, "da0100"},
		{65535, false, "daffff"},
		{65536, false, "db00010000"},
		{0, true, "c400"},
		{255, true, "c4ff"},
		{256, true, "c50100"},
		{65535, true, "c5ffff"},
		{65536, true, "c600010000"},
	}
	for _, c := range cases {
		payload := mpFill(c.n)
		obj := FromLString(payload)
		if c.binary {
			obj = mpBinary(payload)
		}
		b, err := EncodeMsgPack(obj)
		if err != nil {
			t.Fatalf("EncodeMsgPack: %v", err)
		}
		head := len(c.want) / 2
		if got := hex.EncodeToString(b[:head]); got != c.want {
			t.Errorf("len %d binary=%v header = %s, want %s", c.n, c.binary, got, c.want)
		}
		if !bytes.Equal(b[head:], payload) {
			t.Errorf("len %d binary=%v: payload not written verbatim", c.n, c.binary)
		}
	}
}

// TestMsgPackEncodeTimeIsFloat64 pins the lossy mapping: UCL_TIME has no wire
// type of its own and is emitted, like UCL_FLOAT, as a float64.
func TestMsgPackEncodeTimeIsFloat64(t *testing.T) {
	o := NewObject()
	o.setTime(2.5)
	if got := mpEncodeHex(t, o); got != "cb4004000000000000" {
		t.Errorf("time 2.5 = %s, want cb4004000000000000", got)
	}
}

// TestMsgPackEncodeUserdata pins the C's dead-callback behaviour: the emitter
// callback's result is discarded and the object's own string fields are
// written, always through the plain string writer.
func TestMsgPackEncodeUserdata(t *testing.T) {
	o := NewUserdata(nil, func(any) string { return "ignored" }, nil)
	o.sv = []byte("ud")
	o.length = 2
	o.flags |= FlagBinary // still not emitted as bin

	want := "a2" + hex.EncodeToString([]byte("ud")) // fixstr(2) "ud"
	if got := mpEncodeHex(t, o); got != want {
		t.Errorf("userdata = %s, want %s", got, want)
	}
}

func TestMsgPackEncodeContainers(t *testing.T) {
	array := func(n int) *Object {
		a := TypedNew(TypeArray)
		for i := 0; i < n; i++ {
			ArrayAppend(a, FromBool(true))
		}
		return a
	}
	object := func(n int) *Object {
		o := TypedNew(TypeObject)
		for i := 0; i < n; i++ {
			o.InsertKey(FromBool(true), []byte(fmt.Sprintf("k%d", i)), true)
		}
		return o
	}

	cases := []struct {
		name string
		obj  *Object
		want string // header only
	}{
		{"array 0", array(0), "90"},
		{"array 15", array(15), "9f"},
		{"array 16", array(16), "dc0010"},
		{"array 65535", array(65535), "dcffff"},
		{"array 65536", array(65536), "dd00010000"},
		{"map 0", object(0), "80"},
		{"map 15", object(15), "8f"},
		{"map 16", object(16), "de0010"},
	}
	for _, c := range cases {
		b, err := EncodeMsgPack(c.obj)
		if err != nil {
			t.Fatalf("EncodeMsgPack: %v", err)
		}
		head := len(c.want) / 2
		if got := hex.EncodeToString(b[:head]); got != c.want {
			t.Errorf("%s header = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestMsgPackEncodeNested pins a mixed document, captured from the C.
func TestMsgPackEncodeNested(t *testing.T) {
	m := TypedNew(TypeObject)
	a := TypedNew(TypeArray)
	ArrayAppend(a, FromString("hi"))
	ArrayAppend(a, TypedNew(TypeNull))
	m.InsertKey(a, []byte("arr"), true)
	m.InsertKey(mpBinary([]byte("ab")), []byte("bin"), true)

	const want = "82a361727292a26869c0a362696ec4026162"
	if got := mpEncodeHex(t, m); got != want {
		t.Errorf("nested = %s, want %s", got, want)
	}
}

// TestMsgPackEncodeKeysAreAlwaysStrings: a binary-flagged value keeps its bin
// encoding, but the key it hangs off is written with the str family
// regardless -- ucl_emitter_print_key_msgpack only ever calls the string
// writer.
func TestMsgPackEncodeKeysAreAlwaysStrings(t *testing.T) {
	m := TypedNew(TypeObject)
	m.InsertKey(mpBinary([]byte("a")), []byte("kk"), true)

	const want = "81a26b6bc40161"
	if got := mpEncodeHex(t, m); got != want {
		t.Errorf("binary value under a key = %s, want %s", got, want)
	}
}

// TestMsgPackEncodeDuplicateKeyKeepsFirstValue mirrors the LL_FOREACH ...
// break in ucl_emit_msgpack_elt: a map's length is written once in the header,
// so only the first value of a multi-value key survives.
//
// Divergence from the C, deliberate: the C writes obj->len, and its *parser*
// counts a wire-level duplicate key twice, so re-emitting a tree parsed from
// duplicate keys produces a header of 2 followed by a single pair -- a
// document nothing can decode (captured from the reference build as
// 82a16101). This port writes the number of pairs it is really going to emit,
// so the output stays decodable. Trees built through the object API, which is
// what test_msgpack.c does, are unaffected: both agree.
func TestMsgPackEncodeDuplicateKeyKeepsFirstValue(t *testing.T) {
	m := TypedNew(TypeObject)
	m.InsertKey(FromInt(1), []byte("a"), true)
	m.InsertKey(FromInt(2), []byte("a"), true) // chained onto "a"
	m.InsertKey(FromInt(3), []byte("b"), true)

	const want = "82a16101a16203" // captured from the C
	if got := mpEncodeHex(t, m); got != want {
		t.Errorf("duplicate key map = %s, want %s", got, want)
	}
}

// TestMsgPackEncodeArrayHoles: nil array slots are skipped by
// ucl_object_iterate, and the header counts what is actually written.
func TestMsgPackEncodeArrayHoles(t *testing.T) {
	a := TypedNew(TypeArray)
	ArrayAppend(a, FromInt(1))
	ArrayAppend(a, FromInt(2))
	a.av[0] = nil

	if got := mpEncodeHex(t, a); got != "9102" {
		t.Errorf("array with a hole = %s, want 9102", got)
	}
}

func TestMsgPackEncodeNilObject(t *testing.T) {
	b, err := EncodeMsgPack(nil)
	if err != nil || b != nil {
		t.Errorf("EncodeMsgPack(nil) = %v, %v; want nil, nil", b, err)
	}
}

// --- decoder ---------------------------------------------------------------

// TestMsgPackDecodeFormats walks every decodable row of the format table.
// Each input is decoded, then re-encoded; the re-encoded bytes are the C's,
// captured from the reference build, which pins the decoded value as tightly
// as comparing the tree would while also exercising the encoder.
func TestMsgPackDecodeFormats(t *testing.T) {
	cases := []struct {
		name, in, reemit string
	}{
		{"positive fixint", "912a", "912a"},
		{"negative fixint", "91e1", "91e1"},
		{"uint8", "91ccff", "91ccff"},
		{"uint16", "91cd0100", "91cd0100"},
		{"uint32", "91ce00010000", "91ce00010000"},
		{"uint64", "91cf0000000100000000", "91cf0000000100000000"},
		{"uint64 wraps negative", "91cfffffffffffffffff", "91ff"},
		{"int8", "91d080", "91d1ff80"}, // -128 comes back promoted, see the quirk
		{"int16", "91d18000", "91d2ffff8000"},
		{"int32", "91d280000000", "91d3ffffffff80000000"},
		{"int64", "91d38000000000000000", "91d38000000000000000"},
		{"float32 widens", "91ca3f800000", "91cb3ff0000000000000"},
		{"float64", "91cb3ff8000000000000", "91cb3ff8000000000000"},
		{"nil, false, true", "93c0c2c3", "93c0c2c3"},
		{"fixstr", "91a26869", "91a26869"},
		{"str8", "91d9026869", "91a26869"},
		{"str16", "91da00026869", "91a26869"},
		{"str32", "91db000000026869", "91a26869"},
		{"bin8", "91c4026869", "91c4026869"},
		{"bin16", "91c500026869", "91c4026869"},
		{"bin32", "91c6000000026869", "91c4026869"},
		{"fixarray", "920102", "920102"},
		{"array16", "dc00020102", "920102"},
		{"array32", "dd000000020102", "920102"},
		{"fixmap", "81a16b07", "81a16b07"},
		{"map16", "de0001a16b07", "81a16b07"},
		{"map32", "df00000001a16b07", "81a16b07"},
		{"str8 key", "81d9016b07", "81a16b07"},
		{"bin8 key", "81c4016b07", "81a16b07"},
		{"nested", "9181a16b91c3", "9181a16b91c3"},
		{"empty containers nested", "82a16180a16290", "82a16180a16290"},
		{"empty string value", "92a0a100", "92a0a100"},
	}

	for _, c := range cases {
		obj, err := DecodeMsgPack(mpDecodeHex(t, c.in))
		if err != nil {
			t.Errorf("%s: DecodeMsgPack(%s): %v", c.name, c.in, err)
			continue
		}
		if got := mpEncodeHex(t, obj); got != c.reemit {
			t.Errorf("%s: decode(%s) re-emitted as %s, want %s", c.name, c.in, got, c.reemit)
		}
	}
}

// TestMsgPackDecodeValues checks the decoded tree itself rather than its
// re-emission, for the cases where the two are not equivalent.
func TestMsgPackDecodeValues(t *testing.T) {
	obj, err := DecodeMsgPack(mpDecodeHex(t, "91cfffffffffffffffff"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A uint64 above 2^63-1 is reinterpreted, not clamped: the C assigns it
	// straight into the int64 half of the value union.
	if got := ToInt(ArrayHead(obj)); got != -1 {
		t.Errorf("uint64 0xffffffffffffffff decoded as %d, want -1", got)
	}

	obj, err = DecodeMsgPack(mpDecodeHex(t, "91ca3f800000"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	elt := ArrayHead(obj)
	if elt.Type() != TypeFloat || ToDouble(elt) != 1.0 {
		t.Errorf("float32 decoded as %v/%v, want float 1", elt.Type(), ToDouble(elt))
	}

	// A binary string keeps the flag, which is what makes it round-trip as bin.
	obj, err = DecodeMsgPack(mpDecodeHex(t, "91c4026869"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	elt = ArrayHead(obj)
	if elt.Flags()&FlagBinary == 0 {
		t.Error("bin8 decoded without FlagBinary")
	}
	// ToString reports "" for a binary string (the C wart ToStringSafe
	// documents); the bytes come back through ToLString.
	if b, n := ToLString(elt); n != 2 || !bytes.Equal(b, []byte("hi")) {
		t.Errorf("bin8 value = %q/%d, want \"hi\"/2", b, n)
	}
	// Strings carry the length invariant the package requires of every
	// constructor (see invariant_test.go).
	assertStringLen(t, "decoded bin8", elt)

	// An embedded NUL survives: msgpack strings are length-prefixed.
	obj, err = DecodeMsgPack(mpDecodeHex(t, "91a3610062"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b, n := ToLString(ArrayHead(obj)); n != 3 || !bytes.Equal(b, []byte("a\x00b")) {
		t.Errorf("string with NUL = %q/%d, want \"a\\x00b\"/3", b, n)
	}
}

// TestMsgPackDecodeDuplicateKeys: UCL_DUPLICATE_APPEND chains the values of a
// repeated key into an implicit array.
func TestMsgPackDecodeDuplicateKeys(t *testing.T) {
	obj, err := DecodeMsgPack(mpDecodeHex(t, "82a16101a16102"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	head := obj.Lookup("a")
	if head == nil || head.Next == nil {
		t.Fatalf("duplicate key did not build a chain: %v", head)
	}
	if ToInt(head) != 1 || ToInt(head.Next) != 2 {
		t.Errorf("chain = %d, %d; want 1, 2", ToInt(head), ToInt(head.Next))
	}
	// Only the first value survives re-emission, and the header counts the
	// key once.
	if got := mpEncodeHex(t, obj); got != "81a16101" {
		t.Errorf("re-emitted %s, want 81a16101", got)
	}
}

// TestMsgPackDecodeCopiesInput: the decoded tree must not alias the caller's
// buffer, so mutating the input afterwards cannot corrupt the tree. (The C
// copies too unless UCL_PARSER_ZEROCOPY is set.)
func TestMsgPackDecodeCopiesInput(t *testing.T) {
	in := mpDecodeHex(t, "81a16ba26869") // {"k": "hi"}
	obj, err := DecodeMsgPack(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i := range in {
		in[i] = 'X'
	}
	if got := ToString(obj.Lookup("k")); got != "hi" {
		t.Errorf("value became %q after the input was overwritten", got)
	}
	if got := mpEncodeHex(t, obj); got != "81a16ba26869" {
		t.Errorf("re-emitted %s after the input was overwritten", got)
	}
}

// TestMsgPackDecodeEmptyInput mirrors test_empty_input: zero-length input is
// accepted and produces an empty object, because ucl_parser_add_chunk_full
// short-circuits before the msgpack parser (which asserts a non-empty chunk).
func TestMsgPackDecodeEmptyInput(t *testing.T) {
	for _, in := range [][]byte{nil, {}} {
		obj, err := DecodeMsgPack(in)
		if err != nil {
			t.Fatalf("DecodeMsgPack(%v): %v", in, err)
		}
		if obj == nil || obj.Type() != TypeObject {
			t.Fatalf("DecodeMsgPack(%v) = %v, want an empty object", in, obj)
		}
		if n := obj.ov.Len(); n != 0 {
			t.Errorf("empty input produced %d keys", n)
		}
	}
}

// --- malformed input (tests/test_msgpack_malformed.c) ----------------------

// TestMsgPackMalformed is the full contents of test_msgpack_malformed.c: the
// issue #363 ext/fixext truncation regressions plus the general truncation
// cases. Every one must be rejected without reading past the buffer.
func TestMsgPackMalformed(t *testing.T) {
	cases := []struct {
		name, in, why string
	}{
		{"issue363 fixext2 truncated", "9bd5613a",
			"fixarray(11) then fixext2 with 2 of the 3 bytes it needs"},
		{"fixext1 truncated", "91d4aa",
			"fixext1 needs type + 1 data byte, 1 available"},
		{"fixext4 truncated", "91d6010203",
			"fixext4 needs 5 bytes, 3 available"},
		{"fixext8 truncated", "91d701020304",
			"fixext8 needs 9 bytes, 4 available"},
		{"fixext16 truncated", "91d801020304",
			"fixext16 needs 17 bytes, 4 available"},
		{"ext8 truncated", "91c70abbcc",
			"ext8 declares 10 data bytes (11 with the type byte), 2 available"},
		{"ext16 truncated", "91c80100ff",
			"ext16 declares 256 data bytes (257 with the type byte), 1 available"},
		{"truncated string", "91d9",
			"str8 with no length byte"},
		{"truncated map key", "81a56162",
			"fixmap(1) whose key claims fixstr(5) with 2 bytes following"},
		{"truncated int", "91ce0001",
			"uint32 needs 4 payload bytes, 2 available"},
		{"unknown format", "91c1",
			"0xc1 is msgpack's permanently reserved byte"},
		// test_empty_input is the one case that must *succeed*; see
		// TestMsgPackDecodeEmptyInput.
	}

	for _, c := range cases {
		obj, err := DecodeMsgPack(mpDecodeHex(t, c.in))
		if err == nil {
			t.Errorf("%s (%s): accepted %s, producing %v; want a decode error",
				c.name, c.why, c.in, obj)
			continue
		}
		if obj != nil {
			t.Errorf("%s: returned both an object and an error", c.name)
		}
		if !strings.Contains(err.Error(), "msgpack") {
			t.Errorf("%s: error %q does not identify itself as a msgpack error", c.name, err)
		}
	}
}

// TestMsgPackMalformedMore covers the error surface the mechanism implies but
// test_msgpack_malformed.c does not enumerate. Each expectation was confirmed
// against the reference build.
func TestMsgPackMalformedMore(t *testing.T) {
	cases := []struct{ name, in, why string }{
		{"top level positive fixint", "01", "rejected by the first-byte gate"},
		{"top level string", "a26869", "passes the gate, dies in error_state"},
		{"top level nil", "c0", "same"},
		{"top level float", "cb3ff0000000000000", "same"},
		{"lone empty array", "90", "the C checks the stack before inserting"},
		{"lone empty map", "80", "same"},
		{"lone empty array16", "dc0000", "same"},
		{"lone empty map32", "df00000000", "same"},
		{"unclosed array", "930101", "fixarray(3) with two elements"},
		{"unclosed map", "81a16b", "key with no value"},
		{"empty key", "81a005", "zero-length keys are rejected"},
		{"int key", "810102", "bad type for key"},
		{"array key", "819001", "bad type for key"},
		{"trailing garbage", "9001", "the top container is already closed"},
		{"truncated array header", "dc00", "array16 needs 2 length bytes"},
		{"truncated str16 header", "91da00", "str16 needs 2 length bytes"},
		{"truncated float", "91cb0102", "float64 needs 8 payload bytes"},
		{"str declaring more than remains", "91d90561", "str8 of 5 with 1 byte"},
		{"map value cut short", "81a16bcc", "uint8 with no payload"},
		{"map ending on an empty string value", "81a16ba0",
			"the C's terminal read_assoc_value inserts with a NULL key"},
		{"map ending on an empty binary value", "81a16bc400", "same"},
		{"array header larger than the input", "dcffff0102", "count exceeds the elements"},
		{"map header larger than the input", "deffffa16b01", "count exceeds the pairs"},
	}

	for _, c := range cases {
		if obj, err := DecodeMsgPack(mpDecodeHex(t, c.in)); err == nil {
			t.Errorf("%s (%s): accepted %s, producing %v", c.name, c.why, c.in, obj)
		}
	}
}

// TestMsgPackExtIsRejected documents the one deliberate divergence from the C
// (docs/msgpack.md sections 2.2 and 4): ext and fixext are a hard decode error
// in any position.
//
// The C skips an ext value's bytes and produces no object, but then decrements
// nothing, so the enclosing container can never finish and the parse fails at
// EOF with "incomplete container" anyway. Confirmed against the reference
// build: every one of these inputs is rejected there too, so the divergence is
// confined to the error message.
func TestMsgPackExtIsRejected(t *testing.T) {
	cases := []struct{ name, in string }{
		{"fixext1 as array value", "91d40102"},
		{"fixext1 as map value", "81a16bd40102"},
		{"fixext2 well formed", "91d5010203"},
		{"fixext4 well formed", "91d60102030405"},
		{"fixext8 well formed", "91d7010203040506070809"},
		{"fixext16 well formed", "91d8000102030405060708090a0b0c0d0e0f10"},
		{"ext8 well formed", "91c70201aabb"},
		{"ext16 well formed", "91c8000201aabb"},
		{"ext32 well formed", "91c90000000201aabb"},
		{"ext as map key", "81d4010205"},
		{"ext at top level", "d40102"},
		{"ext after a complete container", "9101d40102"},
	}
	for _, c := range cases {
		if obj, err := DecodeMsgPack(mpDecodeHex(t, c.in)); err == nil {
			t.Errorf("%s: accepted %s, producing %v; ext must be rejected", c.name, c.in, obj)
		}
	}
}

// TestMsgPackNestingLimit pins UCL_MAX_NESTING. 1024 open containers are fine;
// the 1025th is "nested too deep". Both confirmed against the reference build.
func TestMsgPackNestingLimit(t *testing.T) {
	nest := func(depth int) []byte {
		b := make([]byte, depth)
		for i := range b {
			b[i] = 0x91 // fixarray(1)
		}
		b[depth-1] = 0x90 // the innermost one is empty, closing the chain
		return b
	}

	if _, err := DecodeMsgPack(nest(1024)); err != nil {
		t.Errorf("1024 levels rejected: %v", err)
	}
	if obj, err := DecodeMsgPack(nest(1025)); err == nil {
		t.Errorf("1025 levels accepted: %v", obj)
	} else if !strings.Contains(err.Error(), "too deep") {
		t.Errorf("1025 levels failed with %q, want a nesting error", err)
	}
}

// --- round trip (tests/test_msgpack.c) -------------------------------------

// msgpackGen mirrors test_msgpack.c's generator harness: a PRNG plus the six
// value generators it picks from, with the same recursion guard.
type msgpackGen struct {
	rnd       *rand.Rand
	recursion int
}

const msgpackNTests = 6 // len(tests[]) in the C

// randomKey is random_key: 1..511 bytes of printable, non-space ASCII.
func (g *msgpackGen) randomKey() []byte {
	n := g.rnd.Intn(511) + 1
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(0x21 + g.rnd.Intn(0x7e-0x21+1)) // isgraph()
	}
	return b
}

func (g *msgpackGen) gen(sel int) *Object {
	switch sel {
	case 0:
		return g.integers()
	case 1:
		return g.strings()
	case 2:
		return g.booleans()
	case 3:
		return g.mapOf()
	case 4:
		return g.arrayOf()
	default:
		return TypedNew(TypeNull)
	}
}

// selector is the C's "re-roll to exclude the container generators once
// recursion goes past 10" safety valve.
func (g *msgpackGen) selector() int {
	if g.recursion > 10 {
		for {
			sel := g.rnd.Intn(msgpackNTests)
			if sel != 3 && sel != 4 {
				return sel
			}
		}
	}
	return g.rnd.Intn(msgpackNTests)
}

// integers is ucl_test_integer: three signed/unsigned pairs at each width
// boundary, plus scaled doubles, per iteration.
func (g *msgpackGen) integers() *Object {
	res := TypedNew(TypeArray)
	count := g.rnd.Intn(20)
	for i := 0; i < count; i++ {
		cur := g.rnd.Uint64()
		ArrayAppend(res, FromInt(int64(cur%128)))
		ArrayAppend(res, FromInt(-int64(cur%128)))
		cur = g.rnd.Uint64()
		ArrayAppend(res, FromInt(int64(cur%math.MaxUint16)))
		ArrayAppend(res, FromInt(-int64(cur%math.MaxInt16)))
		cur = g.rnd.Uint64()
		ArrayAppend(res, FromInt(int64(cur%math.MaxUint32)))
		ArrayAppend(res, FromInt(-int64(cur%math.MaxInt32)))
		cur = g.rnd.Uint64()
		ArrayAppend(res, FromInt(int64(cur)))
		ArrayAppend(res, FromInt(-int64(cur)))

		cur = g.rnd.Uint64()
		ArrayAppend(res, FromDouble(float64(cur%128)/19*16))
		cur = g.rnd.Uint64()
		ArrayAppend(res, FromDouble(-float64(cur%128)/19*16))
		cur = g.rnd.Uint64()
		curf := float64(cur%65536) / 19 * 16
		ArrayAppend(res, FromDouble(curf))
		ArrayAppend(res, FromDouble(-curf))
		cur = g.rnd.Uint64()
		ArrayAppend(res, FromDouble(float64(cur%math.MaxInt32)/19*16))
		ArrayAppend(res, FromInt(int64(g.rnd.Uint64())))
	}
	return res
}

// randomBytes returns n bytes. The C hands ucl_object_fromstring_common a
// malloc'd buffer it never fills, so the content there is whatever the heap
// held; random bytes exercise the length handling at least as well.
func (g *msgpackGen) randomBytes(n int) []byte {
	b := make([]byte, n)
	g.rnd.Read(b)
	return b
}

// strings is ucl_test_string: per iteration two text strings and two binary
// ones, crossing the fixstr/str8 and str8/str16 (and bin8/bin16) boundaries,
// plus one 65537-byte binary string per array to force bin32.
func (g *msgpackGen) strings() *Object {
	res := TypedNew(TypeArray)
	count := g.rnd.Intn(20)
	nonZero := func(mod int) int {
		for {
			if n := int(g.rnd.Uint32()) % mod; n != 0 {
				return n
			}
		}
	}
	for i := 0; i < count; i++ {
		ArrayAppend(res, FromLString(g.randomBytes(nonZero(128))))
		ArrayAppend(res, FromLString(g.randomBytes(nonZero(512))))
		ArrayAppend(res, mpBinary(g.randomBytes(nonZero(128))))
		ArrayAppend(res, mpBinary(g.randomBytes(nonZero(512))))
	}
	ArrayAppend(res, mpBinary(g.randomBytes(65537)))
	return res
}

func (g *msgpackGen) booleans() *Object {
	res := TypedNew(TypeArray)
	count := g.rnd.Intn(20)
	for i := 0; i < count; i++ {
		ArrayAppend(res, FromBool(g.rnd.Intn(2) == 1))
	}
	return res
}

// mapOf is ucl_test_map, which deliberately inserts every key twice to
// exercise the "only the first value of a duplicate key survives" rule.
func (g *msgpackGen) mapOf() *Object {
	res := TypedNew(TypeObject)
	count := g.rnd.Intn(20)
	g.recursion++
	for i := 0; i < count; i++ {
		sel := g.selector()
		key := g.randomKey()
		res.InsertKey(g.gen(sel), key, true)
		res.InsertKey(g.gen(sel), key, true) // multi value key
	}
	return res
}

func (g *msgpackGen) arrayOf() *Object {
	res := TypedNew(TypeArray)
	count := g.rnd.Intn(20)
	g.recursion++
	for i := 0; i < count; i++ {
		ArrayAppend(res, g.gen(g.selector()))
	}
	return res
}

// largeArray and largeMap are sized to force the array32 and map32 headers.
func (g *msgpackGen) largeArray() *Object {
	res := TypedNew(TypeArray)
	g.recursion++
	for i := 0; i < 65537; i++ {
		ArrayAppend(res, FromBool(g.rnd.Intn(2) == 1))
	}
	return res
}

func (g *msgpackGen) largeMap() *Object {
	res := TypedNew(TypeObject)
	g.recursion++
	for i := 0; i < 65537; i++ {
		// Unique keys: 65537 random keys would collide only rarely, but a
		// collision would silently shrink the map and cost the map32 header
		// this generator exists to exercise.
		res.InsertKey(FromBool(g.rnd.Intn(2) == 1),
			append(g.randomKey(), []byte(fmt.Sprintf("-%d", i))...), true)
	}
	return res
}

func (g *msgpackGen) largeString() *Object {
	for {
		if n := int(g.rnd.Uint32()) % 100000; n != 0 {
			return mpBinary(g.randomBytes(n))
		}
	}
}

// TestMsgPackRoundTripByteIdentity is tests/test_msgpack.c: build a random
// document, emit it, parse the emitted bytes, emit again, and require the two
// emissions to be byte-for-byte identical.
//
// The assertion is deliberately about bytes, not trees. The generated document
// contains duplicate keys, whose extra values are dropped on the way out, so
// the parsed tree is *not* equal to the one that was emitted -- but emission
// is deterministic and the two directions agree on every type and width, which
// is what byte identity proves.
//
// The C seeds from /dev/urandom and runs 20 iterations; this uses a fixed seed
// so a failure reproduces, and fewer iterations because each one builds
// several hundred thousand objects (the large generators alone are 65537
// elements each).
func TestMsgPackRoundTripByteIdentity(t *testing.T) {
	const seed = 0x5eed1015

	niter := 5
	if testing.Short() {
		niter = 1
	}

	g := &msgpackGen{rnd: rand.New(rand.NewSource(seed))}

	for iter := 0; iter < niter; iter++ {
		obj := TypedNew(TypeObject)

		for j := 0; j < 10; j++ { // ntests
			g.recursion = 0
			obj.InsertKey(g.gen(g.rnd.Intn(msgpackNTests)), g.randomKey(), true)
		}
		obj.InsertKey(g.largeArray(), g.randomKey(), true)
		obj.InsertKey(g.largeMap(), g.randomKey(), true)
		obj.InsertKey(g.largeString(), g.randomKey(), true)

		first, err := EncodeMsgPack(obj)
		if err != nil {
			t.Fatalf("iteration %d: encode: %v", iter, err)
		}

		parsed, err := DecodeMsgPack(first)
		if err != nil {
			t.Fatalf("iteration %d: decode of %d emitted bytes: %v", iter, len(first), err)
		}

		second, err := EncodeMsgPack(parsed)
		if err != nil {
			t.Fatalf("iteration %d: re-encode: %v", iter, err)
		}

		if len(first) != len(second) {
			t.Fatalf("iteration %d: re-emitted %d bytes, first emission was %d",
				iter, len(second), len(first))
		}
		if !bytes.Equal(first, second) {
			for i := range first {
				if first[i] != second[i] {
					lo := i - 16
					if lo < 0 {
						lo = 0
					}
					t.Fatalf("iteration %d: emissions differ at byte %d: %x vs %x (context %x / %x)",
						iter, i, first[i], second[i], first[lo:i+8], second[lo:i+8])
				}
			}
		}
	}
}

// TestMsgPackRoundTripVectors is the same property on small hand-built trees,
// where a failure is readable.
func TestMsgPackRoundTripVectors(t *testing.T) {
	build := func() []*Object {
		mixed := TypedNew(TypeObject)
		mixed.InsertKey(FromInt(math.MinInt64), []byte("min"), true)
		mixed.InsertKey(FromInt(-128), []byte("quirk"), true)
		mixed.InsertKey(FromDouble(math.Pi), []byte("pi"), true)
		mixed.InsertKey(FromBool(false), []byte("no"), true)
		mixed.InsertKey(TypedNew(TypeNull), []byte("nul"), true)
		mixed.InsertKey(mpBinary(mpFill(300)), []byte("blob"), true)
		mixed.InsertKey(FromLString(mpFill(70000)), []byte("big"), true)

		nested := TypedNew(TypeArray)
		inner := TypedNew(TypeObject)
		inner.InsertKey(TypedNew(TypeArray), []byte("empty array"), true)
		inner.InsertKey(TypedNew(TypeObject), []byte("empty map"), true)
		ArrayAppend(nested, inner)
		ArrayAppend(nested, FromString(""))

		wide := TypedNew(TypeArray)
		for i := 0; i < 70000; i++ {
			ArrayAppend(wide, FromInt(int64(i)))
		}

		return []*Object{mixed, nested, wide}
	}

	for i, obj := range build() {
		first, err := EncodeMsgPack(obj)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		parsed, err := DecodeMsgPack(first)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		second, err := EncodeMsgPack(parsed)
		if err != nil {
			t.Fatalf("case %d: re-encode: %v", i, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("case %d: emit -> parse -> emit is not byte identical", i)
		}
	}
}

// TestMsgPackEmptyRootDoesNotReparse records an upstream round-trip defect
// that this port reproduces (see NOTES-msgpack.md): emitting a document whose
// root is an empty container produces a single byte, 0x80 or 0x90, which the
// msgpack parser then rejects -- the finishing-state branch for an empty
// container checks the stack before inserting, and at the top level there is
// nothing on it. Confirmed against the reference build, where both the empty
// object produced by empty input and a hand-built empty array behave this way.
//
// It is the msgpack analogue of docs/findings.md #3: emitting byte-identical
// output is the contract, emitting reparseable output is not something libucl
// itself provides.
func TestMsgPackEmptyRootDoesNotReparse(t *testing.T) {
	roots := []*Object{TypedNew(TypeObject), TypedNew(TypeArray)}
	wants := []string{"80", "90"}

	for i, root := range roots {
		out, err := EncodeMsgPack(root)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got := hex.EncodeToString(out); got != wants[i] {
			t.Errorf("empty root emitted as %s, want %s", got, wants[i])
		}
		if obj, err := DecodeMsgPack(out); err == nil {
			t.Errorf("a lone %s decoded to %v; the C rejects it", wants[i], obj)
		}
	}
}

// --- fuzzing ---------------------------------------------------------------

// FuzzMsgPackDecode requires only that the decoder never panics, however
// malformed the input, and that anything it does accept re-encodes and decodes
// again -- the same emit/parse agreement the round-trip test asserts, reached
// from the other side.
//
// The seed corpus is the malformed corpus from test_msgpack_malformed.c plus a
// handful of well-formed documents to give the mutator something to chew on.
func FuzzMsgPackDecode(f *testing.F) {
	seeds := []string{
		// test_msgpack_malformed.c
		"9bd5613a", "91d4aa", "91d6010203", "91d701020304", "91d801020304",
		"91c70abbcc", "91c80100ff", "", "91d9", "81a56162", "91ce0001", "91c1",
		// well formed
		"912a", "91e1", "91ccff", "91cd0100", "91ce00010000",
		"91cf0000000100000000", "91d38000000000000000", "91cb3ff8000000000000",
		"93c0c2c3", "91a26869", "91c4026869", "dc00020102", "de0001a16b07",
		"9181a16b91c3", "82a16101a16203", "82a16180a16290",
		// Found by the fuzzer: duplicate keys leave a zero-length value last
		// in the re-emitted map, which libucl's own parser then rejects.
		"82a13082a130a0a13030a13030",
	}
	for _, s := range seeds {
		b, err := hex.DecodeString(s)
		if err != nil {
			f.Fatalf("bad seed %q: %v", s, err)
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		obj, err := DecodeMsgPack(data)
		if err != nil {
			if obj != nil {
				t.Fatalf("returned an object alongside the error %v", err)
			}
			return
		}
		if obj == nil {
			t.Fatal("accepted the input but returned no object")
		}
		if obj.Type() != TypeObject && obj.Type() != TypeArray {
			t.Fatalf("top level object has type %v", obj.Type())
		}

		out, err := EncodeMsgPack(obj)
		if err != nil {
			t.Fatalf("cannot re-encode a tree that was just decoded: %v", err)
		}

		again, err := DecodeMsgPack(out)
		if err != nil {
			// libucl's msgpack emitter can produce documents its own parser
			// rejects, and this port reproduces both known shapes: a lone
			// empty container (0x80/0x90), and a map whose last value is a
			// zero-length string or blob -- which duplicate keys can produce
			// out of an input that had a pair after it. See
			// NOTES-msgpack.md section 4 and
			// TestMsgPackEmptyRootDoesNotReparse. Re-encoding must always
			// work; decoding the result back is not something upstream
			// guarantees.
			return
		}
		out2, err := EncodeMsgPack(again)
		if err != nil {
			t.Fatalf("cannot re-encode: %v", err)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("emit -> parse -> emit is not stable: %x vs %x", out, out2)
		}
	})
}
