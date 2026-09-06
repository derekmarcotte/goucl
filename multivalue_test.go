package goucl

import (
	"slices"
	"testing"
)

// How many values a key holds and what is inside a container are two separate
// questions. Values answers the first, Elements the second, and neither
// guesses which was meant. These tests pin that split, and the case that
// forced it: a repeated key whose values are arrays.

const (
	repeatedKey   = "listen = \"10.0.0.1:80\";\nlisten = \"10.0.0.2:80\";\n"
	explicitArray = "listen = [\"10.0.0.1:80\", \"10.0.0.2:80\"]\n"
)

// Values walks the chain and never looks inside a value.
func TestValuesWalksTheChain(t *testing.T) {
	repeated := mustParse(t, repeatedKey)
	array := mustParse(t, explicitArray)

	rn, _ := repeated.Get("listen")
	var got []string
	for v := range rn.Values() {
		s, ok := v.String("")
		if !ok {
			t.Fatal("a value yielded by Values could not be read")
		}
		got = append(got, s)
	}
	if !slices.Equal(got, []string{"10.0.0.1:80", "10.0.0.2:80"}) {
		t.Errorf("repeated key Values = %v, want both addresses", got)
	}

	// One value that happens to be an array is still one value.
	an, _ := array.Get("listen")
	count := 0
	for v := range an.Values() {
		count++
		if v.Kind() != KindArray {
			t.Errorf("Values yielded %v, want the array itself", v.Kind())
		}
	}
	if count != 1 {
		t.Errorf("array Values yielded %d, want 1", count)
	}
}

// Elements looks inside an array and nowhere else.
func TestElementsExpandsArraysOnly(t *testing.T) {
	array := mustParse(t, explicitArray)
	an, _ := array.Get("listen")
	var got []string
	for e := range an.Elements() {
		s, _ := e.String("")
		got = append(got, s)
	}
	if !slices.Equal(got, []string{"10.0.0.1:80", "10.0.0.2:80"}) {
		t.Errorf("Elements = %v", got)
	}

	// A repeated key is not an array, so Elements yields nothing.
	repeated := mustParse(t, repeatedKey)
	rn, _ := repeated.Get("listen")
	for range rn.Elements() {
		t.Error("Elements yielded something for a repeated key")
	}
	// Nor does a scalar.
	single := mustParse(t, `listen = "10.0.0.1:80";`)
	sn, _ := single.Get("listen")
	for range sn.Elements() {
		t.Error("Elements yielded something for a scalar")
	}
}

// The case that decided the design. Collapsing the two operations made the
// second array unreachable through the entire API.
func TestArrayValuedRepeatedKeyIsReachable(t *testing.T) {
	doc := mustParse(t, `x = [1]; x = [2];`)
	n, ok := doc.Get("x")
	if !ok {
		t.Fatal("x missing")
	}

	var seen [][]int64
	for v := range n.Values() {
		if v.Kind() != KindArray {
			t.Fatalf("value kind = %v, want array", v.Kind())
		}
		var elems []int64
		for e := range v.Elements() {
			i, _ := e.Int("")
			elems = append(elems, i)
		}
		seen = append(seen, elems)
	}
	if len(seen) != 2 {
		t.Fatalf("Values yielded %d values, want 2 -- the second array is unreachable", len(seen))
	}
	if !slices.Equal(seen[0], []int64{1}) || !slices.Equal(seen[1], []int64{2}) {
		t.Errorf("values = %v, want [[1] [2]]", seen)
	}
}

// Len and Index are container operations, matching ucl_array_size and
// ucl_array_find_index, which report nothing for a chain.
func TestLenAndIndexAreContainerOperations(t *testing.T) {
	repeated := mustParse(t, repeatedKey)
	rn, _ := repeated.Get("listen")
	if got := rn.Len(); got != 0 {
		t.Errorf("repeated key Len = %d, want 0: it is not a container", got)
	}
	if _, ok := rn.Index(0); ok {
		t.Error("Index found an element of a repeated key")
	}
	if _, ok := repeated.Get("listen.0"); ok {
		t.Error("a numeric path segment addressed a repeated key")
	}

	array := mustParse(t, explicitArray)
	an, _ := array.Get("listen")
	if got := an.Len(); got != 2 {
		t.Errorf("array Len = %d, want 2", got)
	}
	if _, ok := an.Index(1); !ok {
		t.Error("Index missed on an array")
	}
	if v, ok := array.String("listen.1"); !ok || v != "10.0.0.2:80" {
		t.Errorf("listen.1 = %q, %v", v, ok)
	}
}

// The hazard this all started with: reading one value out of several is the
// one wrong answer an accessor cannot signal, so it refuses instead.
func TestAccessorsRefuseMultiValued(t *testing.T) {
	doc := mustParse(t, "n = 1; n = 2;\ns = \"a\"; s = \"b\";\nb = true; b = false;\nf = 1.5; f = 2.5;\nd = 10s; d = 20s;\n")
	if v, ok := doc.String("s"); ok {
		t.Errorf(`String = %q, true; want false -- it hides a value`, v)
	}
	if _, ok := doc.Int("n"); ok {
		t.Error("Int accepted a repeated key")
	}
	if _, ok := doc.Bool("b"); ok {
		t.Error("Bool accepted a repeated key")
	}
	if _, ok := doc.Float("f"); ok {
		t.Error("Float accepted a repeated key")
	}
	if _, ok := doc.Duration("d"); ok {
		t.Error("Duration accepted a repeated key")
	}
}

// A single-valued key is unaffected, which is what makes the refusal safe.
func TestSingleValuedKeyUnaffected(t *testing.T) {
	doc := mustParse(t, `listen = "10.0.0.1:80";`)
	n, _ := doc.Get("listen")

	if got, ok := doc.String("listen"); !ok || got != "10.0.0.1:80" {
		t.Errorf(`String = %q, %v; want the value, true`, got, ok)
	}
	count := 0
	for v := range n.Values() {
		count++
		if s, ok := v.String(""); !ok || s != "10.0.0.1:80" {
			t.Errorf("Values yielded %q, %v", s, ok)
		}
	}
	if count != 1 {
		t.Errorf("Values yielded %d, want 1", count)
	}
}

// Kind distinguishes the two spellings, and a config round trip preserves
// each, so reading a document and writing it back does not rewrite the file.
func TestKindPreservesDocumentShape(t *testing.T) {
	repeated := mustParse(t, repeatedKey)
	array := mustParse(t, explicitArray)

	rn, _ := repeated.Get("listen")
	an, _ := array.Get("listen")
	if rn.Kind() != KindString {
		t.Errorf("repeated key Kind = %v, want string", rn.Kind())
	}
	if an.Kind() != KindArray {
		t.Errorf("array Kind = %v, want array", an.Kind())
	}

	// They are nonetheless the same document in JSON.
	rj, _ := repeated.Bytes(FormatJSONCompact)
	aj, _ := array.Bytes(FormatJSONCompact)
	if string(rj) != string(aj) {
		t.Errorf("JSON differs:\n %s\n %s", rj, aj)
	}

	rc, _ := repeated.Bytes(FormatConfig)
	ac, _ := array.Bytes(FormatConfig)
	if string(rc) == string(ac) {
		t.Errorf("the two forms collapsed to the same config output:\n%s", rc)
	}
	rn2, _ := mustParse(t, string(rc)).Get("listen")
	an2, _ := mustParse(t, string(ac)).Get("listen")
	if rn2.Kind() != KindString || an2.Kind() != KindArray {
		t.Errorf("round trip changed the shape: %v, %v", rn2.Kind(), an2.Kind())
	}
}

// Repeated objects are the nginx pattern, and are read the same way.
func TestRepeatedKeyOfObjects(t *testing.T) {
	doc := mustParse(t, "server { port = 80; }\nserver { port = 443; }\n")
	n, _ := doc.Get("server")
	if n.Kind() != KindObject {
		t.Errorf("Kind = %v, want object", n.Kind())
	}
	var ports []int64
	for v := range n.Values() {
		p, ok := v.Int("port")
		if !ok {
			t.Fatal("port missing from a server block")
		}
		ports = append(ports, p)
	}
	if !slices.Equal(ports, []int64{80, 443}) {
		t.Errorf("ports = %v, want [80 443]", ports)
	}
}
