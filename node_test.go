package goucl

import (
	"slices"
	"testing"
	"time"
)

func mustParse(t *testing.T, src string) *Node {
	t.Helper()
	n, err := ParseString(src)
	if err != nil {
		t.Fatalf("ParseString(%q): %v", src, err)
	}
	return n
}

func TestKinds(t *testing.T) {
	doc := mustParse(t, `
		s = "text";
		i = 42;
		f = 1.5;
		b = true;
		t = 10s;
		n = null;
		arr = [1, 2];
		obj { k = 1; }
	`)

	for _, tc := range []struct {
		path string
		want Kind
	}{
		{"s", KindString},
		{"i", KindInt},
		{"f", KindFloat},
		{"b", KindBool},
		{"t", KindTime},
		{"n", KindNull},
		{"arr", KindArray},
		{"obj", KindObject},
	} {
		v, ok := doc.Get(tc.path)
		if !ok {
			t.Errorf("%s: not found", tc.path)
			continue
		}
		if got := v.Kind(); got != tc.want {
			t.Errorf("%s: kind = %v, want %v", tc.path, got, tc.want)
		}
	}

	if got := doc.Kind(); got != KindObject {
		t.Errorf("root kind = %v, want object", got)
	}
}

// A failed conversion and a real zero must be distinguishable. This is the
// reason the accessors are comma-ok rather than returning a bare value.
func TestZeroIsNotAMiss(t *testing.T) {
	doc := mustParse(t, `zero = 0; empty = ""; no = false;`)

	if v, ok := doc.Int("zero"); !ok || v != 0 {
		t.Errorf(`Int("zero") = %v, %v; want 0, true`, v, ok)
	}
	if v, ok := doc.Int("absent"); ok || v != 0 {
		t.Errorf(`Int("absent") = %v, %v; want 0, false`, v, ok)
	}
	if v, ok := doc.String("empty"); !ok || v != "" {
		t.Errorf(`String("empty") = %q, %v; want "", true`, v, ok)
	}
	if v, ok := doc.String("absent"); ok || v != "" {
		t.Errorf(`String("absent") = %q, %v; want "", false`, v, ok)
	}
	if v, ok := doc.Bool("no"); !ok || v != false {
		t.Errorf(`Bool("no") = %v, %v; want false, true`, v, ok)
	}
	if v, ok := doc.Bool("absent"); ok {
		t.Errorf(`Bool("absent") = %v, %v; want false, false`, v, ok)
	}
}

// A present key of the wrong kind is a miss, not a coerced value.
func TestWrongKindIsAMiss(t *testing.T) {
	doc := mustParse(t, `s = "text"; i = 42;`)

	if v, ok := doc.Int("s"); ok {
		t.Errorf(`Int("s") on a string = %v, %v; want 0, false`, v, ok)
	}
	if v, ok := doc.String("i"); ok {
		t.Errorf(`String("i") on an int = %q, %v; want "", false`, v, ok)
	}
	if v, ok := doc.Bool("i"); ok {
		t.Errorf(`Bool("i") on an int = %v, %v; want false, false`, v, ok)
	}
	// Numeric kinds do convert between themselves, as the C does.
	if v, ok := doc.Float("i"); !ok || v != 42 {
		t.Errorf(`Float("i") = %v, %v; want 42, true`, v, ok)
	}
}

func TestPaths(t *testing.T) {
	doc := mustParse(t, `
		server {
			host = "example.org";
			ports = [80, 443];
		}
	`)

	if v, ok := doc.String("server.host"); !ok || v != "example.org" {
		t.Errorf(`String("server.host") = %q, %v`, v, ok)
	}
	if v, ok := doc.Int("server.ports.1"); !ok || v != 443 {
		t.Errorf(`Int("server.ports.1") = %v, %v; want 443, true`, v, ok)
	}
	if _, ok := doc.Get("server.missing"); ok {
		t.Error(`Get("server.missing") should miss`)
	}
	if _, ok := doc.Get("server.ports.9"); ok {
		t.Error(`Get("server.ports.9") should miss`)
	}
	// An empty path is the node itself, which is what lets the accessors
	// take a path uniformly.
	self, ok := doc.Get("")
	if !ok || self != doc {
		t.Errorf(`Get("") = %v, %v; want the receiver`, self, ok)
	}
}

func TestIndexAndValues(t *testing.T) {
	doc := mustParse(t, `arr = [10, 20, 30]`)
	arr, ok := doc.Get("arr")
	if !ok {
		t.Fatal("arr missing")
	}
	if got := arr.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	for i, want := range []int64{10, 20, 30} {
		v, ok := arr.Index(i)
		if !ok {
			t.Fatalf("Index(%d) missed", i)
		}
		if got, _ := v.Int(""); got != want {
			t.Errorf("Index(%d) = %d, want %d", i, got, want)
		}
	}
	if _, ok := arr.Index(3); ok {
		t.Error("Index(3) should miss")
	}
	if _, ok := arr.Index(-1); ok {
		t.Error("Index(-1) should miss")
	}

	var got []int64
	for v := range arr.Values() {
		n, _ := v.Int("")
		got = append(got, n)
	}
	if !slices.Equal(got, []int64{10, 20, 30}) {
		t.Errorf("Values = %v, want [10 20 30]", got)
	}
}

// A repeated key is an implicit array in UCL. All yields the key once; Values
// walks the chain.
func TestImplicitArray(t *testing.T) {
	doc := mustParse(t, "k = 1;\nk = 2;\nk = 3;\n")

	var keys []string
	for key := range doc.All() {
		keys = append(keys, key)
	}
	if !slices.Equal(keys, []string{"k"}) {
		t.Errorf("All keys = %v, want [k]", keys)
	}

	v, ok := doc.Get("k")
	if !ok {
		t.Fatal("k missing")
	}
	var got []int64
	for elt := range v.Values() {
		n, _ := elt.Int("")
		got = append(got, n)
	}
	if !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("chain = %v, want [1 2 3]", got)
	}
}

func TestAllOrderAndNonObjects(t *testing.T) {
	doc := mustParse(t, `first = 1; second = 2; third = 3;`)
	var keys []string
	for key, v := range doc.All() {
		keys = append(keys, key)
		if v.Key() != key {
			t.Errorf("Key() = %q, want %q", v.Key(), key)
		}
	}
	if !slices.Equal(keys, []string{"first", "second", "third"}) {
		t.Errorf("All = %v, want insertion order", keys)
	}

	// All over a scalar yields nothing rather than panicking.
	s, _ := doc.Get("first")
	for range s.All() {
		t.Error("All over a scalar yielded something")
	}
}

func TestAllEarlyExit(t *testing.T) {
	doc := mustParse(t, `a = 1; b = 2; c = 3;`)
	count := 0
	for range doc.All() {
		count++
		break
	}
	if count != 1 {
		t.Errorf("break yielded %d, want 1", count)
	}
}

func TestLen(t *testing.T) {
	doc := mustParse(t, `obj { a = 1; b = 2; } arr = [1,2,3,4]; s = "hello";`)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"obj", 2},
		{"arr", 4},
		{"s", 0}, // deliberately not the byte length the C reports
	} {
		v, ok := doc.Get(tc.path)
		if !ok {
			t.Fatalf("%s missing", tc.path)
		}
		if got := v.Len(); got != tc.want {
			t.Errorf("%s: Len = %d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestDuration(t *testing.T) {
	doc := mustParse(t, `short = 10s; long = 2min; plain = 10;`)

	if got, ok := doc.Duration("short"); !ok || got != 10*time.Second {
		t.Errorf(`Duration("short") = %v, %v; want 10s, true`, got, ok)
	}
	if got, ok := doc.Duration("long"); !ok || got != 2*time.Minute {
		t.Errorf(`Duration("long") = %v, %v; want 2m, true`, got, ok)
	}
	// A bare number is an integer, not seconds. Reading it as a duration
	// would be inventing a unit the document did not write.
	if got, ok := doc.Duration("plain"); ok {
		t.Errorf(`Duration("plain") = %v, %v; want 0, false`, got, ok)
	}
}

// Every method must tolerate a nil receiver, matching the engine's own
// nil-safety guarantee.
func TestNilSafety(t *testing.T) {
	var n *Node
	var zero Node

	for _, node := range []*Node{n, &zero} {
		if node.Kind() != KindInvalid {
			t.Errorf("Kind = %v, want invalid", node.Kind())
		}
		if node.Key() != "" {
			t.Error("Key should be empty")
		}
		if node.Len() != 0 {
			t.Error("Len should be 0")
		}
		if _, ok := node.Get("a.b"); ok {
			t.Error("Get should miss")
		}
		if _, ok := node.Index(0); ok {
			t.Error("Index should miss")
		}
		if _, ok := node.String("x"); ok {
			t.Error("String should miss")
		}
		if _, ok := node.Int("x"); ok {
			t.Error("Int should miss")
		}
		if _, ok := node.Float("x"); ok {
			t.Error("Float should miss")
		}
		if _, ok := node.Bool("x"); ok {
			t.Error("Bool should miss")
		}
		if _, ok := node.Duration("x"); ok {
			t.Error("Duration should miss")
		}
		if node.Before() != nil || node.After() != nil {
			t.Error("comments should be nil")
		}
		for range node.All() {
			t.Error("All should yield nothing")
		}
		for range node.Values() {
			t.Error("Values should yield nothing")
		}
		if node.GoString() == "" {
			t.Error("GoString should not be empty")
		}
		if _, err := node.Bytes(FormatJSON); err == nil {
			t.Error("Bytes on an invalid node should error")
		}
	}
}

func TestKindNames(t *testing.T) {
	for _, tc := range []struct {
		k    Kind
		want string
	}{
		{KindObject, "object"},
		{KindArray, "array"},
		{KindInt, "integer"},
		{KindFloat, "float"},
		{KindString, "string"},
		{KindBool, "boolean"},
		{KindTime, "time"},
		{KindNull, "null"},
		{KindUserdata, "userdata"},
		{KindInvalid, "invalid"},
		{Kind(99), "invalid"},
	} {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("Kind(%d).String = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestGoString(t *testing.T) {
	doc := mustParse(t, `key = 1; arr = [1];`)

	v, _ := doc.Get("key")
	if got := v.GoString(); got != `goucl.Node(integer "key")` {
		t.Errorf("GoString = %s", got)
	}

	arr, _ := doc.Get("arr")
	elt, _ := arr.Index(0)
	if got := elt.GoString(); got != "goucl.Node(integer)" {
		t.Errorf("array element GoString = %s", got)
	}

	var nilNode *Node
	if got := nilNode.GoString(); got != "goucl.Node(invalid)" {
		t.Errorf("nil GoString = %s", got)
	}
}
