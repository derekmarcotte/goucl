package goucl

import (
	"bytes"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// Tests for the four text emitters.
//
// The emitters are also exercised end-to-end by TestBasicGoldens in
// parser_test.go, which runs every fixture in testdata/basic through all six
// modes -- config, json, compact, yaml, comments and nomacro -- against output
// captured from the C library. Those goldens need a parser, because each is
// emit(parse(emit(parse(input)))) plus the trailing newline the C harness adds
// (docs/findings.md #6).
//
// The tests below cover what that cannot: hand-built object trees exercising
// individual emitter rules, plus testdata/generate.res and
// testdata/streamline.res, which the C builds programmatically.

// --- helpers ---------------------------------------------------------------
//
// Everything here is prefixed "emit" to stay clear of the object API being
// written concurrently in the same package.

func emitTestStr(key, val string) *Object {
	return emitTestBytes(key, []byte(val))
}

func emitTestBytes(key string, val []byte) *Object {
	return &Object{ObjType: TypeString, key: emitTestKey(key), sv: val}
}

func emitTestInt(key string, val int64) *Object {
	return &Object{ObjType: TypeInt, key: emitTestKey(key), iv: val}
}

func emitTestFloat(key string, val float64) *Object {
	return &Object{ObjType: TypeFloat, key: emitTestKey(key), dv: val}
}

func emitTestTime(key string, val float64) *Object {
	return &Object{ObjType: TypeTime, key: emitTestKey(key), dv: val}
}

func emitTestBool(key string, val bool) *Object {
	o := &Object{ObjType: TypeBoolean, key: emitTestKey(key)}
	if val {
		o.iv = 1
	}
	return o
}

func emitTestNull(key string) *Object {
	return &Object{ObjType: TypeNull, key: emitTestKey(key)}
}

func emitTestUserdata(key, out string) *Object {
	return &Object{
		ObjType:   TypeUserdata,
		key:       emitTestKey(key),
		udEmitter: func(any) string { return out },
	}
}

func emitTestArray(key string, elts ...*Object) *Object {
	return &Object{ObjType: TypeArray, key: emitTestKey(key), av: elts}
}

// emitTestObject builds a TypeObject from members given in insertion order.
// Members repeating a key are appended to that key's Next chain, which is how
// the parser represents an implicit array.
func emitTestObject(key string, members ...*Object) *Object {
	o := &Object{ObjType: TypeObject, key: emitTestKey(key), ov: newHash(false)}
	for _, m := range members {
		if head := o.ov.Lookup(m.key); head != nil {
			dlAppend(head, m)
			continue
		}
		o.ov.Insert(m.key, dlAppend(nil, m))
	}
	return o
}

func emitTestKey(key string) []byte {
	if key == "" {
		return nil
	}
	return []byte(key)
}

// emitTestChain links objects into a Next chain with the utlist invariants
// dlAppend expects, which is how duplicate keys and comment chains are held.
func emitTestChain(objs ...*Object) *Object {
	var head *Object
	for _, o := range objs {
		head = dlAppend(head, o)
	}
	return head
}

// emitTestFlag returns obj with extra flag bits set, for the parser-set flags
// the emitter reads.
func emitTestFlag(obj *Object, flags ObjectFlag) *Object {
	obj.flags |= flags
	return obj
}

func emitTestRender(t *testing.T, obj *Object, format Emitter) string {
	t.Helper()
	got, err := EmitTextString(obj, format)
	if err != nil {
		t.Fatalf("EmitTextString: %v", err)
	}
	return got
}

func emitTestEqual(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// --- generate.res: a port of libucl's tests/test_generate.c ----------------

// TestEmitGenerateRes is the strongest single signal available before the
// parser lands: tests/test_generate.c builds an object tree through the C API
// and emits it as CONFIG, and tests/generate.res is the result.
//
// The tree here is the end state of that program, with the string values taken
// from ucl_object_fromstring_common's output for the same inputs and flags
// (verified against the reference build). Array ordering is likewise the
// post-sort, post-merge order the C ends up with.
func TestEmitGenerateRes(t *testing.T) {
	// key4's array: [9.999, 10, 10.1] after the append/prepend/delete churn.
	key4Array := emitTestArray("key4",
		emitTestFloat("", 9.999),
		emitTestInt("", 10),
		emitTestFloat("", 10.1),
	)

	// ar1: a copy of the above with five strings prepended, then sorted with
	// ucl_object_compare_qsort, which orders by type before value.
	ar1 := []*Object{
		emitTestInt("", 10),
		emitTestFloat("", 9.999),
		emitTestFloat("", 10.1),
		emitTestStr("", "abc"),
		emitTestStr("", "cde"),
		emitTestStr("", "\U0001F60E"),
		emitTestStr("", "\u0415\u0431\u0432"),
		emitTestStr("", "\u0430\u0431\u0432"),
	}

	key16 := emitTestStr("key16", "tes") // fromlstring("test", 3)

	root := emitTestObject("",
		emitTestFloat("key0", 0.1), // inserted as a string, then replaced
		emitTestStr("key1", "test string"),
		emitTestStr("key2", `test \nstring\n\r\n\b\t\f\\\"`),
		emitTestStr("key3", "  test string    \n"),
		key4Array,
		emitTestBool("key4", true), // duplicate key: a second key4 entry
		emitTestStr("key5", ""),
		emitTestStr("key6", ""),
		emitTestStr("key7", `   \n`),
		emitTestInt("key8", 1048576),
		emitTestFloat("key9", 3.14),
		emitTestBool("key10", true),
		emitTestBool("key11", false),
		emitTestStr("key12", "gslin@gslin.org"),
		emitTestStr("key13", "#test"),
		emitTestFlag(emitTestBool("k=3", true), FlagNeedKeyEscape),
		emitTestArray("key14", ar1...),
		emitTestUserdata("key15", "test userdata emit"),
		key16,
		emitTestArray("key17", append([]*Object{emitTestStr("", "test")}, ar1...)...),
	)

	// ucl_comments_add attached "# test comment" to key17's value, then
	// ucl_comments_move moved it to key16's. It has no INHERITED flag, so it
	// prints above the entry.
	comments := EmitComments{key16: emitTestStr("", "# test comment")}

	var buf bytes.Buffer
	if err := EmitTextFull(&buf, root, EmitConfig, comments); err != nil {
		t.Fatalf("EmitTextFull: %v", err)
	}
	// test_generate.c prints the emitted text with fprintf("%s\n"), so the
	// golden carries one newline the emitter did not produce.
	buf.WriteByte('\n')

	want, err := os.ReadFile("testdata/generate.res")
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("generate.res mismatch:\n--- got ---\n%s\n--- want ---\n%s", buf.Bytes(), want)
	}
}

// --- streamline.res: a port of libucl's tests/test_streamline.c ------------

// TestEmitStreamlineRes exercises the streaming API against
// tests/streamline.res. Note that the three keys already on the root object
// are emitted by EmitStreamNew itself, before any streaming happens, and that
// this golden has no synthetic trailing newline.
func TestEmitStreamlineRes(t *testing.T) {
	root := emitTestObject("",
		emitTestStr("key1", "test string"),
		emitTestStr("key2", `test \nstring`),
		emitTestStr("key3", "  test string    \n"),
	)

	var buf bytes.Buffer
	s, err := EmitStreamNew(&buf, root, EmitConfig)
	if err != nil {
		t.Fatalf("EmitStreamNew: %v", err)
	}

	s.StartContainer(emitTestArray("key4"))
	s.AddObject(emitTestInt("", 10))
	s.AddObject(emitTestFloat("", 10.1))
	s.AddObject(emitTestFloat("", 9.999))
	s.EndContainer()
	if err := s.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	want, err := os.ReadFile("testdata/streamline.res")
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("streamline.res mismatch:\n--- got ---\n%s\n--- want ---\n%s", buf.Bytes(), want)
	}
}

// TestEmitStreamNested checks that a container opened after the first one is
// treated as a non-first sibling, and that Finish closes what the caller left
// open.
func TestEmitStreamNested(t *testing.T) {
	var buf bytes.Buffer
	s, err := EmitStreamNew(&buf, emitTestObject(""), EmitJSON)
	if err != nil {
		t.Fatalf("EmitStreamNew: %v", err)
	}
	s.AddObject(emitTestInt("a", 1))
	s.StartContainer(emitTestArray("b"))
	s.AddObject(emitTestInt("", 2))
	s.AddObject(emitTestInt("", 3))
	// Deliberately unclosed: Finish must close both the array and the object.
	if err := s.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	want := "{\n    \"a\": 1,\n    \"b\": [\n        2,\n        3\n    ]\n}"
	emitTestEqual(t, "streamed nested JSON", buf.String(), want)
}

// A value that is neither object nor array is API misuse and opens nothing.
func TestEmitStreamRejectsScalarContainer(t *testing.T) {
	var buf bytes.Buffer
	s, err := EmitStreamNew(&buf, emitTestObject(""), EmitConfig)
	if err != nil {
		t.Fatalf("EmitStreamNew: %v", err)
	}
	if s.StartContainer(emitTestInt("x", 1)) {
		t.Error("StartContainer accepted a scalar")
	}
	if err := s.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	emitTestEqual(t, "scalar container", buf.String(), "")
}

// --- number formatting -----------------------------------------------------

// TestEmitDouble pins the three-branch formatter, including the 32-bit cast
// that keeps large integral doubles out of the "%.1lf" branch
// (docs/findings.md #5). Every expectation was produced by compiling the C
// logic with gcc -O0 and running it on the same input.
func TestEmitDouble(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		// Branch A: exact integer within int32 range.
		{1.0, "1.0"},
		{0.0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{60.0, "60.0"},
		{315360000.0, "315360000.0"},
		{2147483647.0, "2147483647.0"},   // int32 max
		{-2147483648.0, "-2147483648.0"}, // int32 min

		// The 32-bit cast: integral, but out of int32 range, so branch C.
		{2147483648.0, "2147483648.000000"},
		{-2147483649.0, "-2147483649.000000"},
		{4294967296.0, "4294967296.000000"},
		{1234567890123.0, "1234567890123.000000"},

		// Branch B: within 1e-7 of its truncation, printed at %.15g.
		{-1e-10, "-1e-10"},
		{1.0000000001, "1.0000000001"},
		{100.00000005, "100.00000005"},
		{-100.00000005, "-100.00000005"},
		{1e-15, "1e-15"},
		{5e-8, "5e-08"},
		{2.5e-8, "2.5e-08"},
		{123456789.00000001, "123456789"},

		// Branch C: everything else, at six decimals, never scientific.
		{0.01, "0.010000"},
		{0.1, "0.100000"},
		{123.2, "123.200000"},
		{3.14, "3.140000"},
		{9.999, "9.999000"},
		{10.1, "10.100000"},
		{0.5, "0.500000"},
		{-1e-5, "-0.000010"},
		{1e-7, "0.000000"}, // not < delta, so not branch B
		{1e20, "100000000000000000000.000000"},

		// Truncation is toward zero, so this is not "close to" 1: branch C
		// rounds it to 1.000000 on its own.
		{0.99999999996, "1.000000"},

		// C's spelling, not Go's.
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
		{math.NaN(), "nan"},
	}

	for _, c := range cases {
		if got := emitDouble(c.in); got != c.want {
			t.Errorf("emitDouble(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 1.5e300 expands in full rather than going scientific, which is worth pinning
// separately because it is where a naive %g would diverge most visibly.
func TestEmitDoubleHugeExpandsInFull(t *testing.T) {
	got := emitDouble(1.5e300)
	if strings.ContainsAny(got, "eE") {
		t.Fatalf("emitDouble(1.5e300) went scientific: %q", got)
	}
	if !strings.HasPrefix(got, "1500000000000000078757140382806630373056") {
		t.Errorf("emitDouble(1.5e300) = %q, want the exact decimal expansion", got)
	}
	if !strings.HasSuffix(got, ".000000") {
		t.Errorf("emitDouble(1.5e300) = %q, want six trailing decimals", got)
	}
}

// UCL_TIME has no duration rendering at emit time; it is formatted as a plain
// double, exactly like UCL_FLOAT.
func TestEmitTimeIsFormattedAsFloat(t *testing.T) {
	root := emitTestObject("", emitTestTime("t", 5.0), emitTestFloat("f", 5.0))
	emitTestEqual(t, "time value", emitTestRender(t, root, EmitConfig), "t = 5.0;\nf = 5.0;\n")
}

func TestEmitIntegers(t *testing.T) {
	root := emitTestObject("",
		emitTestInt("a", 0),
		emitTestInt("b", -3735928559),
		emitTestInt("c", math.MaxInt64),
		emitTestInt("d", math.MinInt64),
	)
	want := "a = 0;\nb = -3735928559;\nc = 9223372036854775807;\nd = -9223372036854775808;\n"
	emitTestEqual(t, "integers", emitTestRender(t, root, EmitConfig), want)
}

// --- string escaping -------------------------------------------------------

// TestEmitJSONStringEscapes walks the whole byte-classification table: the
// named escapes, the vertical tab's real Unicode escape, the fixed \uFFFD text
// that every other flagged byte collapses to, and the untouched pass-through
// of 0x80-0xFF.
func TestEmitJSONStringEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"named escapes", []byte("\b\t\n\f\r"), `"\b\t\n\f\r"`},
		{"vertical tab", []byte{0x0b}, `"\u000B"`},
		{"quote and backslash", []byte(`a"b\c`), `"a\"b\\c"`},
		{"space is not escaped", []byte("a b"), `"a b"`},
		{"NUL has no named escape", []byte{0x00}, `"\uFFFD"`},
		{"C0 controls collapse", []byte{0x01, 0x07, 0x0e, 0x1f}, `"\uFFFD\uFFFD\uFFFD\uFFFD"`},
		{"DEL is denied", []byte{0x7f}, `"\uFFFD"`},
		{"0x7e is not", []byte{0x7e}, `"~"`},
		// No UTF-8 validation happens anywhere: high bytes are copied raw,
		// valid sequences or not.
		{"UTF-8 passes through", []byte("\u03b4\u0b90\u1f64"), "\"\u03b4\u0b90\u1f64\""},
		{"invalid UTF-8 passes through", []byte{0x80, 0xff, 0xc3}, "\"\x80\xff\xc3\""},
		{"empty", nil, `""`},
		{"runs are preserved", []byte("abc\ndef\tghi"), `"abc\ndef\tghi"`},
	}

	for _, c := range cases {
		root := emitTestObject("", emitTestBytes("k", c.in))
		got := emitTestRender(t, root, EmitJSONCompact)
		want := `{"k":` + c.want + `}`
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
	}
}

// Every byte flagged by ucl_chartable.h and nothing else must be escaped. The
// flagged set was read off the reference build: 0x00-0x20, '"', '\\', 0x7F.
func TestEmitJSONStringEscapeTableCoverage(t *testing.T) {
	for b := 0; b < 256; b++ {
		flagged := b <= 0x20 || b == '"' || b == '\\' || b == 0x7f
		escaped := emitJSONEscapes[b] != ""
		if flagged != escaped {
			t.Errorf("byte %#02x: flagged=%v but table entry %q", b, flagged, emitJSONEscapes[b])
		}
	}
	// Space is flagged yet renders as itself.
	if emitJSONEscapes[' '] != " " {
		t.Errorf("space entry = %q, want a literal space", emitJSONEscapes[' '])
	}
}

// A key needing escapes is quoted by CONFIG only when the parser said so, is
// always quoted by JSON, and follows the CONFIG rule under YAML.
func TestEmitKeyQuoting(t *testing.T) {
	root := emitTestObject("",
		emitTestFlag(emitTestBool("k=3", true), FlagNeedKeyEscape),
		emitTestBool("/usr/local/bin/pkgconf", true),
	)
	emitTestEqual(t, "config keys", emitTestRender(t, root, EmitConfig),
		"\"k=3\" = true;\n/usr/local/bin/pkgconf = true;\n")
	emitTestEqual(t, "yaml keys", emitTestRender(t, root, EmitYAML),
		"\"k=3\": true\n/usr/local/bin/pkgconf: true")
	emitTestEqual(t, "json keys", emitTestRender(t, root, EmitJSONCompact),
		`{"k=3":true,"/usr/local/bin/pkgconf":true}`)
}

// An empty key renders as the bare word null under YAML and JSON alike.
func TestEmitEmptyKey(t *testing.T) {
	root := emitTestObject("", emitTestInt("", 1))
	emitTestEqual(t, "yaml empty key", emitTestRender(t, root, EmitYAML), "null: 1")
	emitTestEqual(t, "json empty key", emitTestRender(t, root, EmitJSONCompact), "{null:1}")
	emitTestEqual(t, "config empty key", emitTestRender(t, root, EmitConfig), " = 1;\n")
}

// --- CONFIG string writer selection ---------------------------------------

func TestEmitConfigMultilineString(t *testing.T) {
	long := "line one\n" + strings.Repeat("x", 80)
	root := emitTestObject("", emitTestStr("k", long))
	emitTestEqual(t, "heredoc", emitTestRender(t, root, EmitConfig),
		"k = <<EOD\n"+long+"\nEOD;\n")

	// The flag substitutes for the length test.
	short := emitTestFlag(emitTestStr("k", "a\nb"), FlagMultiline)
	emitTestEqual(t, "flagged heredoc", emitTestRender(t, emitTestObject("", short), EmitConfig),
		"k = <<EOD\na\nb\nEOD;\n")

	// Long but without a newline: not a heredoc.
	noNL := emitTestStr("k", strings.Repeat("y", 100))
	emitTestEqual(t, "no newline", emitTestRender(t, emitTestObject("", noNL), EmitConfig),
		`k = "`+strings.Repeat("y", 100)+"\";\n")

	// Every other format ignores all of this.
	emitTestEqual(t, "json ignores multiline", emitTestRender(t, emitTestObject("", short), EmitJSONCompact),
		`{"k":"a\nb"}`)
}

// A value that would close the heredoc early falls back to JSON quoting.
func TestEmitConfigMultilineFallback(t *testing.T) {
	for _, val := range []string{
		"line one\n" + strings.Repeat("x", 80) + "\nEOD\nline three",
		"line one\n" + strings.Repeat("x", 80) + "\nEOD",
	} {
		root := emitTestObject("", emitTestFlag(emitTestStr("k", val), FlagMultiline))
		got := emitTestRender(t, root, EmitConfig)
		if strings.Contains(got, "<<EOD") {
			t.Errorf("value %q produced a heredoc, want the JSON fallback:\n%s", val, got)
		}
		if !strings.HasPrefix(got, `k = "`) {
			t.Errorf("value %q: want a double-quoted fallback, got %q", val, got)
		}
	}

	// "\nEODx" is not a terminator, so the heredoc still applies.
	ok := "a\nEODx\n" + strings.Repeat("z", 80)
	root := emitTestObject("", emitTestStr("k", ok))
	if !strings.HasPrefix(emitTestRender(t, root, EmitConfig), "k = <<EOD\n") {
		t.Error("\\nEODx wrongly triggered the fallback")
	}
}

func TestEmitConfigSquotedString(t *testing.T) {
	sq := func(v string) *Object {
		return emitTestFlag(emitTestStr("k", v), FlagSquoted)
	}

	// Only the single quote is escaped. The two-byte sequence \n and the
	// double quote are passed through completely untouched.
	emitTestEqual(t, "squoted", emitTestRender(t, emitTestObject("", sq(`b\n'a`)), EmitConfig),
		"k = 'b\\n\\'a';\n")
	emitTestEqual(t, "empty squoted", emitTestRender(t, emitTestObject("", sq("")), EmitConfig),
		"k = '';\n")
	emitTestEqual(t, "dquote inside squotes", emitTestRender(t, emitTestObject("", sq(`"`)), EmitConfig),
		"k = '\"';\n")

	// A stored \' cannot round-trip through single quotes, so the whole value
	// falls back to JSON quoting.
	emitTestEqual(t, "squoted fallback", emitTestRender(t, emitTestObject("", sq(`a\'b`)), EmitConfig),
		`k = "a\\'b";`+"\n")

	// Only CONFIG consults the flag.
	emitTestEqual(t, "json ignores squoted", emitTestRender(t, emitTestObject("", sq("a'b")), EmitJSONCompact),
		`{"k":"a'b"}`)
}

// --- whole-document shapes -------------------------------------------------

// The worked example from docs/emitter.md section 2.5, in all four formats.
func TestEmitWorkedExample(t *testing.T) {
	root := emitTestObject("",
		emitTestObject("section1", emitTestStr("param1", "value")),
		emitTestArray("key1", emitTestFloat("", 1.0), emitTestStr("", "some string")),
	)

	cases := []struct {
		name   string
		format Emitter
		want   string
	}{
		{"EmitConfig", EmitConfig, "section1 {\n    param1 = \"value\";\n}\nkey1 [\n    1.0,\n    \"some string\",\n]\n"},
		{"EmitJSON", EmitJSON, "{\n    \"section1\": {\n        \"param1\": \"value\"\n    },\n    \"key1\": [\n        1.0,\n        \"some string\"\n    ]\n}"},
		{"EmitJSONCompact", EmitJSONCompact, `{"section1":{"param1":"value"},"key1":[1.0,"some string"]}`},
		{"EmitYAML", EmitYAML, "section1: {\n    param1: \"value\"\n}\nkey1: [\n    1.0,\n    \"some string\"\n]"},
	}
	for _, c := range cases {
		emitTestEqual(t, c.name, emitTestRender(t, root, c.format), c.want)
	}
}

// Empty containers, which differ in every format. The blank line the
// non-CONFIG formats show inside "{}" is not a special case: it is the closing
// newline that end_object always writes, with no child having written one.
func TestEmitEmptyContainers(t *testing.T) {
	root := emitTestObject("",
		emitTestObject("obj"),
		emitTestArray("arr"),
	)

	cases := []struct {
		name   string
		format Emitter
		want   string
	}{
		{"EmitConfig", EmitConfig, "obj {\n}\narr [\n]\n"},
		{"EmitJSON", EmitJSON, "{\n    \"obj\": {\n\n    },\n    \"arr\": [\n\n    ]\n}"},
		{"EmitJSONCompact", EmitJSONCompact, `{"obj":{},"arr":[]}`},
		{"EmitYAML", EmitYAML, "obj: {\n\n}\narr: [\n\n]"},
	}
	for _, c := range cases {
		emitTestEqual(t, c.name, emitTestRender(t, root, c.format), c.want)
	}
}

// A completely empty root behaves the same way.
func TestEmitEmptyRoot(t *testing.T) {
	root := emitTestObject("")
	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig), "")
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML), "")
	emitTestEqual(t, "json", emitTestRender(t, root, EmitJSON), "{\n\n}")
	emitTestEqual(t, "compact", emitTestRender(t, root, EmitJSONCompact), "{}")
}

// The root object's braces are suppressed for CONFIG and YAML but never for
// JSON, and the root is exempt from CONFIG's entry terminator.
func TestEmitRootObjectWrapping(t *testing.T) {
	root := emitTestObject("", emitTestInt("a", 1))
	emitTestEqual(t, "config root", emitTestRender(t, root, EmitConfig), "a = 1;\n")
	emitTestEqual(t, "yaml root", emitTestRender(t, root, EmitYAML), "a: 1")
	emitTestEqual(t, "json root", emitTestRender(t, root, EmitJSON), "{\n    \"a\": 1\n}")
}

// Arrays are always bracketed, even as the document root, in every format:
// only objects consult UCL_EMIT_IDENT_TOP_OBJ.
func TestEmitRootArrayIsAlwaysBracketed(t *testing.T) {
	root := emitTestArray("", emitTestInt("", 1), emitTestInt("", 2))
	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig), "[\n    1,\n    2,\n]")
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML), "[\n    1,\n    2\n]")
	emitTestEqual(t, "json", emitTestRender(t, root, EmitJSON), "[\n    1,\n    2\n]")
	emitTestEqual(t, "compact", emitTestRender(t, root, EmitJSONCompact), "[1,2]")
}

// Indentation is four spaces per level and closing delimiters sit one level
// out, level with the line that opened them.
func TestEmitNestedIndentation(t *testing.T) {
	root := emitTestObject("",
		emitTestObject("a", emitTestObject("b", emitTestArray("c", emitTestInt("", 1)))),
	)
	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig),
		"a {\n    b {\n        c [\n            1,\n        ]\n    }\n}\n")
	emitTestEqual(t, "json", emitTestRender(t, root, EmitJSON),
		"{\n    \"a\": {\n        \"b\": {\n            \"c\": [\n                1\n            ]\n        }\n    }\n}")
}

// All scalar types in one pass.
func TestEmitScalarTypes(t *testing.T) {
	root := emitTestObject("",
		emitTestInt("i", 42),
		emitTestFloat("f", 1.5),
		emitTestBool("t", true),
		emitTestBool("f2", false),
		emitTestStr("s", "x"),
		emitTestNull("n"),
		emitTestUserdata("u", "test userdata emit"),
	)
	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig),
		"i = 42;\nf = 1.500000;\nt = true;\nf2 = false;\ns = \"x\";\nn = null;\nu = \"test userdata emit\";\n")
	emitTestEqual(t, "compact", emitTestRender(t, root, EmitJSONCompact),
		`{"i":42,"f":1.500000,"t":true,"f2":false,"s":"x","n":null,"u":"test userdata emit"}`)
}

// Userdata without an emitter callback renders as an empty string, the C's
// initial ud_out value.
func TestEmitUserdataWithoutEmitter(t *testing.T) {
	root := emitTestObject("", &Object{ObjType: TypeUserdata, key: []byte("u")})
	emitTestEqual(t, "userdata", emitTestRender(t, root, EmitConfig), "u = \"\";\n")
}

// --- duplicate keys --------------------------------------------------------

// The single most important cross-format difference: one duplicate-key chain
// becomes repeated self-terminating entries under CONFIG, but collapses into
// one array under every other format.
func TestEmitDuplicateKeys(t *testing.T) {
	root := emitTestObject("",
		emitTestInt("dup", 1),
		emitTestInt("dup", 2),
		emitTestInt("dup", 3),
		emitTestStr("after", "x"),
	)

	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig),
		"dup = 1;\ndup = 2;\ndup = 3;\nafter = \"x\";\n")
	emitTestEqual(t, "json", emitTestRender(t, root, EmitJSON),
		"{\n    \"dup\": [\n        1,\n        2,\n        3\n    ],\n    \"after\": \"x\"\n}")
	emitTestEqual(t, "compact", emitTestRender(t, root, EmitJSONCompact),
		`{"dup":[1,2,3],"after":"x"}`)
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML),
		"dup: [\n    1,\n    2,\n    3\n]\nafter: \"x\"")
}

// Duplicate object values, the shape testdata/basic/8 has.
func TestEmitDuplicateObjectValues(t *testing.T) {
	root := emitTestObject("",
		emitTestObject("section", emitTestStr("a", "1")),
		emitTestObject("section", emitTestStr("b", "2")),
	)
	emitTestEqual(t, "config", emitTestRender(t, root, EmitConfig),
		"section {\n    a = \"1\";\n}\nsection {\n    b = \"2\";\n}\n")
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML),
		"section: [\n    {\n        a: \"1\"\n    },\n    {\n        b: \"2\"\n    }\n]")
}

// A duplicate-key chain that is not the first top-level key takes ",\n", not
// YAML's bare-newline top-level separator: the chain branch in
// ucl_emitter_common_start_object does not implement that special case.
func TestEmitYAMLDuplicateChainSeparator(t *testing.T) {
	root := emitTestObject("",
		emitTestInt("first", 0),
		emitTestInt("dup", 1),
		emitTestInt("dup", 2),
	)
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML),
		"first: 0,\ndup: [\n    1,\n    2\n]")
}

// Top-level YAML pairs are separated by a bare newline; anything nested uses
// an ordinary comma.
func TestEmitYAMLTopLevelSeparator(t *testing.T) {
	root := emitTestObject("",
		emitTestInt("a", 1),
		emitTestInt("b", 2),
		emitTestObject("c", emitTestInt("d", 3), emitTestInt("e", 4)),
	)
	emitTestEqual(t, "yaml", emitTestRender(t, root, EmitYAML),
		"a: 1\nb: 2\nc: {\n    d: 3,\n    e: 4\n}")
}

// --- comments --------------------------------------------------------------

// Comments are CONFIG-only. A chain without FlagInherited prints above the
// entry with indentation after every node; one with it prints below, with
// indentation only between nodes.
func TestEmitComments(t *testing.T) {
	inner := emitTestInt("k", 1)
	trailing := emitTestInt("t", 2)
	root := emitTestObject("", emitTestObject("obj", inner, trailing))

	before := emitTestChain(
		emitTestStr("", "# leading one"),
		emitTestStr("", "# leading two"),
	)
	after := emitTestChain(
		emitTestFlag(emitTestStr("", "# trailing one"), FlagInherited),
		emitTestStr("", "# trailing two"),
	)

	var buf bytes.Buffer
	comments := EmitComments{inner: before, trailing: after}
	if err := EmitTextFull(&buf, root, EmitConfig, comments); err != nil {
		t.Fatalf("EmitTextFull: %v", err)
	}

	want := "obj {\n" +
		"    # leading one\n" +
		"    # leading two\n" +
		"    k = 1;\n" +
		"    t = 2;\n" +
		"# trailing one\n" +
		"    # trailing two\n" +
		"}\n"
	emitTestEqual(t, "comments", buf.String(), want)

	// The other formats ignore the comments map entirely.
	var jsonBuf bytes.Buffer
	if err := EmitTextFull(&jsonBuf, root, EmitJSONCompact, comments); err != nil {
		t.Fatalf("EmitTextFull: %v", err)
	}
	emitTestEqual(t, "json ignores comments", jsonBuf.String(), `{"obj":{"k":1,"t":2}}`)
}

// A block comment whose stored text already ends in a newline produces a blank
// line, because the emitter appends one unconditionally. That doubling is the
// entire mechanism behind the blank lines in comments.comments.golden.
func TestEmitCommentBlankLine(t *testing.T) {
	inner := emitTestInt("k", 1)
	root := emitTestObject("", inner)

	var buf bytes.Buffer
	comments := EmitComments{inner: emitTestStr("", "/* block */\n")}
	if err := EmitTextFull(&buf, root, EmitConfig, comments); err != nil {
		t.Fatalf("EmitTextFull: %v", err)
	}
	emitTestEqual(t, "block comment", buf.String(), "/* block */\n\nk = 1;\n")
}

// --- API surface -----------------------------------------------------------

func TestEmitTextRejectsNonTextFormat(t *testing.T) {
	_, err := EmitText(emitTestObject(""), EmitMsgPack)
	if !errors.Is(err, ErrEmitFormat) {
		t.Errorf("EmitText(EmitMsgPack) error = %v, want ErrEmitFormat", err)
	}
	if _, err := EmitStreamNew(&bytes.Buffer{}, emitTestObject(""), Emitter(99)); !errors.Is(err, ErrEmitFormat) {
		t.Errorf("EmitStreamNew(99) error = %v, want ErrEmitFormat", err)
	}
}

// A nil object emits nothing and does not fail, mirroring ucl_object_emit's
// NULL return.
func TestEmitTextNilObject(t *testing.T) {
	got, err := EmitText(nil, EmitConfig)
	if err != nil || got != nil {
		t.Errorf("EmitText(nil) = %q, %v; want nil, nil", got, err)
	}
}

// A failing sink surfaces its first error rather than being swallowed.
func TestEmitTextWriterError(t *testing.T) {
	want := errors.New("sink failed")
	root := emitTestObject("", emitTestStr("k", strings.Repeat("v", 8192)))
	err := EmitTextTo(emitTestFailingWriter{want}, root, EmitConfig)
	if !errors.Is(err, want) {
		t.Errorf("EmitTextTo error = %v, want %v", err, want)
	}
}

type emitTestFailingWriter struct{ err error }

func (w emitTestFailingWriter) Write([]byte) (int, error) { return 0, w.err }

// Nil holes in an array are skipped without being counted as elements, as
// ucl_object_iterate does.
func TestEmitArraySkipsNilElements(t *testing.T) {
	root := emitTestObject("", emitTestArray("a", nil, emitTestInt("", 1), nil, emitTestInt("", 2), nil))
	emitTestEqual(t, "compact", emitTestRender(t, root, EmitJSONCompact), `{"a":[1,2]}`)
}
