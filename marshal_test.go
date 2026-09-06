package goucl

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Scalars ---------------------------------------------------------------

func TestMarshalScalars(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want Kind
	}{
		{"bool", true, KindBool},
		{"int", 42, KindInt},
		{"int8", int8(-1), KindInt},
		{"uint", uint(7), KindInt},
		{"float32", float32(1.5), KindFloat},
		{"float64", 1.5, KindFloat},
		{"string", "hi", KindString},
		{"bytes", []byte("hi"), KindString},
		{"duration", 5 * time.Second, KindTime},
		{"nil", nil, KindNull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := toNode(tc.v)
			if err != nil {
				t.Fatalf("toNode(%v): %v", tc.v, err)
			}
			if n.Kind() != tc.want {
				t.Errorf("toNode(%v).Kind() = %v, want %v", tc.v, n.Kind(), tc.want)
			}
		})
	}
}

// A nil pointer and a nil interface both become KindNull.
func TestMarshalNil(t *testing.T) {
	var p *int
	var i any
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"nil pointer", p},
		{"nil interface", i},
		{"literal nil", nil},
	} {
		n, err := toNode(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.Kind() != KindNull {
			t.Errorf("%s: Kind() = %v, want KindNull", tc.name, n.Kind())
		}
	}
}

// Pointers are followed to the value they point at.
func TestMarshalPointer(t *testing.T) {
	x := 42
	n, err := toNode(&x)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := n.Int(""); !ok || got != 42 {
		t.Errorf("Int() = %v, %v; want 42, true", got, ok)
	}
}

// time.Duration must produce a KindTime node, and Node.Duration must recover
// the exact value.
//
// This is checked at the Node level, not through a text round trip: the
// config emitter has no syntax for re-stating "this number means seconds" --
// see the engine's own comment at TypeTime's case in emit.go, "UCL_TIME has no
// duration rendering at emit time; it is a plain double, formatted exactly
// like UCL_FLOAT" -- so re-emitting and re-parsing a time value degrades it to
// a float even for a document the C itself wrote and read back. That is the
// engine's documented, faithful behaviour, not something Marshal introduces or
// can repair from this package.
func TestMarshalDuration(t *testing.T) {
	for _, d := range []time.Duration{
		0,
		time.Second,
		1500 * time.Millisecond,
		90 * time.Second,
		2 * time.Hour,
		-5 * time.Second,
		250 * time.Microsecond,
		123456789 * time.Nanosecond,
	} {
		n, err := toNode(d)
		if err != nil {
			t.Fatalf("%v: %v", d, err)
		}
		if n.Kind() != KindTime {
			t.Fatalf("%v: Kind() = %v, want KindTime", d, n.Kind())
		}
		got, ok := n.Duration("")
		if !ok || got != d {
			t.Errorf("%v: Duration() = %v, %v; want %v, true", d, got, ok, d)
		}
	}
}

// []byte holds raw bytes, not base64: UCL strings already hold raw bytes, so
// there is nothing to encode. This is checked at the Node level so that the
// assertion is about what Marshal built, not about what the config emitter's
// string escaping does to arbitrary bytes when it re-renders them as text --
// see TestMarshalBytesRoundTrip for the boundary of what that survives.
func TestMarshalBytesRaw(t *testing.T) {
	data := []byte{0, 1, 2, 3, 0xff, 0xfe, 'h', 'i'}
	n, err := toNode(data)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindString {
		t.Fatalf("Kind() = %v, want KindString", n.Kind())
	}
	got, ok := n.String("")
	if !ok || got != string(data) {
		t.Errorf("String() = %q, %v; want %q, true", got, ok, data)
	}
}

// A nil []byte and an empty []byte both encode as an empty string.
func TestMarshalBytesNilAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
	} {
		n, err := toNode(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.Kind() != KindString {
			t.Fatalf("%s: Kind() = %v, want KindString", tc.name, n.Kind())
		}
		if got, ok := n.String(""); !ok || got != "" {
			t.Errorf("%s: String() = %q, %v; want \"\", true", tc.name, got, ok)
		}
	}
}

// A nil slice and an empty slice both encode as an empty array.
func TestMarshalSliceNilAndEmpty(t *testing.T) {
	var nilSlice []int
	for _, tc := range []struct {
		name string
		v    []int
	}{
		{"nil", nilSlice},
		{"empty", []int{}},
	} {
		n, err := toNode(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.Kind() != KindArray {
			t.Fatalf("%s: Kind() = %v, want KindArray", tc.name, n.Kind())
		}
		if got := n.Len(); got != 0 {
			t.Errorf("%s: Len() = %d, want 0", tc.name, got)
		}
	}
}

// A nil map encodes as an empty object, matching the nil-slice rule.
func TestMarshalNilMap(t *testing.T) {
	var m map[string]int
	n, err := toNode(m)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindObject {
		t.Fatalf("Kind() = %v, want KindObject", n.Kind())
	}
	if got := n.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}

// --- Slices, arrays, maps ----------------------------------------------

func TestMarshalSlice(t *testing.T) {
	n, err := toNode([]int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindArray || n.Len() != 3 {
		t.Fatalf("Kind()=%v Len()=%d, want array of 3", n.Kind(), n.Len())
	}
	for i, want := range []int64{1, 2, 3} {
		v, ok := n.Index(i)
		if !ok {
			t.Fatalf("Index(%d) missed", i)
		}
		if got, _ := v.Int(""); got != want {
			t.Errorf("Index(%d) = %d, want %d", i, got, want)
		}
	}
}

func TestMarshalArray(t *testing.T) {
	n, err := toNode([3]string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindArray || n.Len() != 3 {
		t.Fatalf("Kind()=%v Len()=%d, want array of 3", n.Kind(), n.Len())
	}
}

// Map keys are sorted, so the encoding is deterministic. This matters enough
// to assert exact bytes: this project has already been bitten once by a test
// that depended on Go's randomised map order.
func TestMarshalMapDeterministic(t *testing.T) {
	m := map[string]int{"zebra": 1, "apple": 2, "mango": 3}
	const want = "apple = 2;\nmango = 3;\nzebra = 1;\n"

	for i := 0; i < 20; i++ {
		out, err := Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != want {
			t.Fatalf("iteration %d:\ngot:  %q\nwant: %q", i, out, want)
		}
	}
}

// Integer and encoding.TextMarshaler map keys render to strings.
func TestMarshalMapKeyKinds(t *testing.T) {
	out, err := Marshal(map[int]string{2: "b", 1: "a", 10: "c"})
	if err != nil {
		t.Fatal(err)
	}
	// Sorted as strings: "1" < "10" < "2".
	want := "1 = \"a\";\n10 = \"c\";\n2 = \"b\";\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// An unsupported map key type is an error, not a panic.
func TestMarshalMapKeyUnsupported(t *testing.T) {
	_, err := Marshal(map[float64]string{1.5: "x"})
	if err == nil {
		t.Fatal("expected an error for a float map key")
	}
}

// --- Structs -----------------------------------------------------------

type marshalInner struct {
	A int    `ucl:"a"`
	B string `json:"b"`
}

type marshalOuter struct {
	Name       string `ucl:"name"`
	Skipped    string `ucl:"-"`
	Empty      string `ucl:"empty,omitempty"`
	Zero       int    `ucl:"zero,omitempty"`
	NoTag      string
	Inner      marshalInner `ucl:"inner"`
	unexported int
}

func TestMarshalStruct(t *testing.T) {
	v := marshalOuter{
		Name:       "svc",
		Skipped:    "must not appear",
		Empty:      "",
		Zero:       0,
		NoTag:      "asis",
		Inner:      marshalInner{A: 1, B: "x"},
		unexported: 99,
	}
	n, err := toNode(v)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := n.String("name"); !ok || got != "svc" {
		t.Errorf(`String("name") = %q, %v; want "svc", true`, got, ok)
	}
	if _, ok := n.Get("Skipped"); ok {
		t.Error(`"Skipped" should not appear, its tag is "-"`)
	}
	if _, ok := n.Get("empty"); ok {
		t.Error(`"empty" should be omitted, it is the zero value with omitempty`)
	}
	if _, ok := n.Get("zero"); ok {
		t.Error(`"zero" should be omitted, it is the zero value with omitempty`)
	}
	if got, ok := n.String("NoTag"); !ok || got != "asis" {
		t.Errorf(`String("NoTag") = %q, %v; want "asis", true (falls back to the field name)`, got, ok)
	}
	if got, ok := n.Int("inner.a"); !ok || got != 1 {
		t.Errorf(`Int("inner.a") = %v, %v; want 1, true`, got, ok)
	}
	if got, ok := n.String("inner.b"); !ok || got != "x" {
		t.Errorf(`String("inner.b") = %q, %v; want "x", true`, got, ok)
	}
	for _, key := range []string{"unexported"} {
		if _, ok := n.Get(key); ok {
			t.Errorf("%q should not appear, it is unexported", key)
		}
	}
}

// A ucl tag wins over a json tag when both are present.
func TestMarshalTagPrecedence(t *testing.T) {
	type both struct {
		X int `ucl:"fromUcl" json:"fromJSON"`
	}
	n, err := toNode(both{X: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.Get("fromUcl"); !ok {
		t.Error(`"fromUcl" missing: the ucl tag should win`)
	}
	if _, ok := n.Get("fromJSON"); ok {
		t.Error(`"fromJSON" present: the ucl tag should have won`)
	}
}

// A literal "-" tag skips the field even with a trailing comma, matching
// encoding/json: only the bare tag "-" means skip, "-,anything" names a field
// literally called "-".
func TestMarshalDashTag(t *testing.T) {
	type s struct {
		X int `ucl:"-,omitempty"`
	}
	n, err := toNode(s{X: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.Get("-"); !ok {
		t.Error(`"-," should name a field literally called "-", not skip it`)
	}
}

// omitempty follows encoding/json's meaning of empty.
func TestMarshalOmitEmpty(t *testing.T) {
	type s struct {
		B   bool           `ucl:"b,omitempty"`
		I   int            `ucl:"i,omitempty"`
		S   string         `ucl:"s,omitempty"`
		Sl  []int          `ucl:"sl,omitempty"`
		M   map[string]int `ucl:"m,omitempty"`
		P   *int           `ucl:"p,omitempty"`
		Any any            `ucl:"any,omitempty"`
	}
	n, err := toNode(s{})
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0; every field is its empty value", got)
	}
}

// --- encoding.TextMarshaler ----------------------------------------------

type marshalHex int

func (h marshalHex) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprintf("0x%x", int(h))), nil
}

func TestMarshalTextMarshaler(t *testing.T) {
	n, err := toNode(marshalHex(255))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindString {
		t.Fatalf("Kind() = %v, want KindString", n.Kind())
	}
	if got, ok := n.String(""); !ok || got != "0xff" {
		t.Errorf("String() = %q, %v; want \"0xff\", true", got, ok)
	}
}

type marshalErrText struct{}

func (marshalErrText) MarshalText() ([]byte, error) {
	return nil, errors.New("boom")
}

func TestMarshalTextMarshalerError(t *testing.T) {
	_, err := Marshal(marshalErrText{})
	if err == nil {
		t.Fatal("expected an error from a failing MarshalText")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not mention the underlying failure", err)
	}
}

// --- Unsupported types and cycles ------------------------------------------

func TestMarshalUnsupportedType(t *testing.T) {
	for name, v := range map[string]any{
		"chan":    make(chan int),
		"func":    func() {},
		"complex": complex(1, 2),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Marshal(v)
			if err == nil {
				t.Fatalf("Marshal(%s) succeeded, want an error", name)
			}
			var ute *UnsupportedTypeError
			if !errors.As(err, &ute) {
				t.Errorf("error %v is not an *UnsupportedTypeError", err)
			}
		})
	}
}

// An unsupported type nested in a struct names its path.
func TestMarshalUnsupportedTypePath(t *testing.T) {
	type s struct {
		Bad chan int `ucl:"bad"`
	}
	_, err := Marshal(s{})
	var ute *UnsupportedTypeError
	if !errors.As(err, &ute) {
		t.Fatalf("error %v is not an *UnsupportedTypeError", err)
	}
	if ute.Path != "bad" {
		t.Errorf("Path = %q, want %q", ute.Path, "bad")
	}
}

type marshalCycle struct {
	Next *marshalCycle
}

// A directly self-referential structure must error, not hang.
func TestMarshalCyclePointer(t *testing.T) {
	c := &marshalCycle{}
	c.Next = c
	_, err := Marshal(c)
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Errorf("error %v is not a *CycleError", err)
	}
}

// A cycle through a map must also error, not hang.
func TestMarshalCycleMap(t *testing.T) {
	type node struct {
		Children map[string]any
	}
	n := &node{Children: map[string]any{}}
	n.Children["self"] = n
	_, err := Marshal(n)
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Errorf("error %v is not a *CycleError", err)
	}
}

// A cycle through a slice must also error, not hang.
func TestMarshalCycleSlice(t *testing.T) {
	type node struct {
		Children []any
	}
	n := &node{}
	n.Children = []any{n}
	_, err := Marshal(n)
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Errorf("error %v is not a *CycleError", err)
	}
}

// The same value reachable twice without a cycle -- shared, not
// self-referential -- must not be mistaken for one.
func TestMarshalSharedNotACycle(t *testing.T) {
	shared := []int{1, 2, 3}
	type s struct {
		A []int `ucl:"a"`
		B []int `ucl:"b"`
	}
	n, err := toNode(s{A: shared, B: shared})
	if err != nil {
		t.Fatalf("shared, non-cyclic value should not error: %v", err)
	}
	if got := n.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
}

// --- Marshal, end to end -----------------------------------------------

func TestMarshalReturnsConfigSyntax(t *testing.T) {
	out, err := Marshal(struct {
		Key string `ucl:"key"`
	}{"value"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `key = "value";`) {
		t.Errorf("Marshal output does not look like config syntax: %q", out)
	}
}

// --- Round trip: Marshal, then Parse -----------------------------------

// The strongest test available: what Marshal writes must be read back by
// Parse into the same values, through the ordinary typed accessors.
func TestMarshalParseRoundTrip(t *testing.T) {
	type inner struct {
		A int    `ucl:"a"`
		B string `ucl:"b"`
	}
	type doc struct {
		Str    string         `ucl:"str"`
		Int    int            `ucl:"int"`
		Neg    int            `ucl:"neg"`
		Float  float64        `ucl:"float"`
		Bool   bool           `ucl:"bool"`
		Bytes  []byte         `ucl:"bytes"`
		Nested inner          `ucl:"nested"`
		Nums   []int          `ucl:"nums"`
		Empty  []int          `ucl:"empty"`
		Map    map[string]int `ucl:"map"`
		Ptr    *int           `ucl:"ptr"`
		Null   *int           `ucl:"null"`
	}

	forty := 40
	v := doc{
		Str:    "hello \"world\"",
		Int:    12345,
		Neg:    -99,
		Float:  3.5,
		Bool:   true,
		Bytes:  []byte("raw bytes"),
		Nested: inner{A: 7, B: "seven"},
		Nums:   []int{1, 2, 3},
		Empty:  []int{},
		Map:    map[string]int{"x": 1, "y": 2},
		Ptr:    &forty,
		Null:   nil,
	}

	out, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	parsed, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%s): %v", out, err)
	}

	if got, ok := parsed.String("str"); !ok || got != v.Str {
		t.Errorf("str = %q, %v; want %q, true", got, ok, v.Str)
	}
	if got, ok := parsed.Int("int"); !ok || got != int64(v.Int) {
		t.Errorf("int = %v, %v; want %d, true", got, ok, v.Int)
	}
	if got, ok := parsed.Int("neg"); !ok || got != int64(v.Neg) {
		t.Errorf("neg = %v, %v; want %d, true", got, ok, v.Neg)
	}
	if got, ok := parsed.Float("float"); !ok || got != v.Float {
		t.Errorf("float = %v, %v; want %v, true", got, ok, v.Float)
	}
	if got, ok := parsed.Bool("bool"); !ok || got != v.Bool {
		t.Errorf("bool = %v, %v; want %v, true", got, ok, v.Bool)
	}
	if got, ok := parsed.String("bytes"); !ok || got != string(v.Bytes) {
		t.Errorf("bytes = %q, %v; want %q, true", got, ok, v.Bytes)
	}
	if got, ok := parsed.Int("nested.a"); !ok || got != int64(v.Nested.A) {
		t.Errorf("nested.a = %v, %v; want %d, true", got, ok, v.Nested.A)
	}
	if got, ok := parsed.String("nested.b"); !ok || got != v.Nested.B {
		t.Errorf("nested.b = %q, %v; want %q, true", got, ok, v.Nested.B)
	}
	nums, ok := parsed.Get("nums")
	if !ok || nums.Len() != len(v.Nums) {
		t.Fatalf("nums missing or wrong length")
	}
	for i, want := range v.Nums {
		got, ok := parsed.Int(fmt.Sprintf("nums.%d", i))
		if !ok || got != int64(want) {
			t.Errorf("nums.%d = %v, %v; want %d, true", i, got, ok, want)
		}
	}
	empty, ok := parsed.Get("empty")
	if !ok || empty.Kind() != KindArray || empty.Len() != 0 {
		t.Errorf("empty = %v (kind %v, len %d); want an empty array", empty, empty.Kind(), empty.Len())
	}
	for k, want := range v.Map {
		got, ok := parsed.Int("map." + k)
		if !ok || got != int64(want) {
			t.Errorf("map.%s = %v, %v; want %d, true", k, got, ok, want)
		}
	}
	if got, ok := parsed.Int("ptr"); !ok || got != int64(*v.Ptr) {
		t.Errorf("ptr = %v, %v; want %d, true", got, ok, *v.Ptr)
	}
	null, ok := parsed.Get("null")
	if !ok || null.Kind() != KindNull {
		t.Errorf("null = %v (kind %v); want KindNull", null, null.Kind())
	}
}

// []byte holding printable, valid-UTF-8 bytes survives a full text round
// trip. Control bytes (0x00-0x20, 0x7F) do not: the config emitter replaces
// them with the Unicode replacement character on every string it writes,
// whatever produced the string, which is the engine's documented behaviour
// (see emitJSONEscapes in internal/core/emit.go) and applies equally to a
// document parsed from UCL source, so it is not particular to Marshal.
func TestMarshalBytesRoundTrip(t *testing.T) {
	data := []byte("printable bytes with a high byte \xff here")
	out, err := Marshal(struct {
		B []byte `ucl:"b"`
	}{data})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%s): %v", out, err)
	}
	got, ok := parsed.String("b")
	if !ok || got != string(data) {
		t.Errorf("b = %q, %v; want %q, true", got, ok, data)
	}
}

// Round trip through a slice of structs and a pointer to a struct.
func TestMarshalParseRoundTripSliceOfStructs(t *testing.T) {
	type item struct {
		Name  string  `ucl:"name"`
		Price float64 `ucl:"price"`
	}
	items := []item{
		{"apple", 1.5},
		{"pear", 2.25},
	}
	out, err := Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%s): %v", out, err)
	}
	if got := parsed.Len(); got != len(items) {
		t.Fatalf("Len() = %d, want %d", got, len(items))
	}
	for i, want := range items {
		name, ok := parsed.String(fmt.Sprintf("%d.name", i))
		if !ok || name != want.Name {
			t.Errorf("%d.name = %q, %v; want %q, true", i, name, ok, want.Name)
		}
		price, ok := parsed.Float(fmt.Sprintf("%d.price", i))
		if !ok || price != want.Price {
			t.Errorf("%d.price = %v, %v; want %v, true", i, price, ok, want.Price)
		}
	}
}

// A round trip through TextMarshaler: the marshaled text reads back as an
// ordinary string, since UCL has no notion of the original Go type.
func TestMarshalTextMarshalerRoundTrip(t *testing.T) {
	out, err := Marshal(struct {
		H marshalHex `ucl:"h"`
	}{marshalHex(255)})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%s): %v", out, err)
	}
	if got, ok := parsed.String("h"); !ok || got != "0xff" {
		t.Errorf("h = %q, %v; want \"0xff\", true", got, ok)
	}
}
