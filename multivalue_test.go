package goucl

import (
	"slices"
	"testing"
)

// A key written twice and an array literal are the same document: they emit
// identical JSON. So they must read identically too, or a program that works
// against one silently misreads the other.
//
// The exception is Kind, which keeps reporting the node's own type. That is
// the only thing telling the two documents apart, and losing it would mean
// re-emitting a document in a shape its author did not write.

const (
	repeatedKey   = "listen = \"10.0.0.1:80\";\nlisten = \"10.0.0.2:80\";\n"
	explicitArray = "listen = [\"10.0.0.1:80\", \"10.0.0.2:80\"]\n"
)

func TestRepeatedKeyAndArrayReadAlike(t *testing.T) {
	repeated := mustParse(t, repeatedKey)
	array := mustParse(t, explicitArray)

	// They really are the same document.
	rj, _ := repeated.Bytes(FormatJSONCompact)
	aj, _ := array.Bytes(FormatJSONCompact)
	if string(rj) != string(aj) {
		t.Fatalf("not the same document:\n %s\n %s", rj, aj)
	}

	for _, tc := range []struct {
		name string
		doc  *Node
	}{{"repeated", repeated}, {"array", array}} {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := tc.doc.Get("listen")
			if !ok {
				t.Fatal("listen missing")
			}
			if got := n.Len(); got != 2 {
				t.Errorf("Len = %d, want 2", got)
			}
			// The dangerous one: reading several values as one must fail.
			if v, ok := tc.doc.String("listen"); ok {
				t.Errorf(`String("listen") = %q, true; want false -- it hides a value`, v)
			}
			for i, want := range []string{"10.0.0.1:80", "10.0.0.2:80"} {
				elt, ok := n.Index(i)
				if !ok {
					t.Fatalf("Index(%d) missed", i)
				}
				if got, _ := elt.String(""); got != want {
					t.Errorf("Index(%d) = %q, want %q", i, got, want)
				}
				p, ok := tc.doc.Get("listen." + string(rune('0'+i)))
				if !ok {
					t.Fatalf("path listen.%d missed", i)
				}
				if got, _ := p.String(""); got != want {
					t.Errorf("listen.%d = %q, want %q", i, got, want)
				}
			}
			var all []string
			for v := range n.Values() {
				s, ok := v.String("")
				if !ok {
					t.Fatal("a value yielded by Values could not be read")
				}
				all = append(all, s)
			}
			if !slices.Equal(all, []string{"10.0.0.1:80", "10.0.0.2:80"}) {
				t.Errorf("Values = %v", all)
			}
		})
	}
}

// Kind is what distinguishes the two, and the config round trip must preserve
// the form the author wrote.
func TestKindPreservesDocumentShape(t *testing.T) {
	repeated := mustParse(t, repeatedKey)
	array := mustParse(t, explicitArray)

	rn, _ := repeated.Get("listen")
	an, _ := array.Get("listen")
	if rn.Kind() != KindString {
		t.Errorf("repeated key Kind = %v, want string (the shape it was written in)", rn.Kind())
	}
	if an.Kind() != KindArray {
		t.Errorf("array Kind = %v, want array", an.Kind())
	}

	// And the emitted config keeps each shape rather than converting one.
	rc, err := repeated.Bytes(FormatConfig)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := array.Bytes(FormatConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(rc) == string(ac) {
		t.Errorf("the two forms collapsed to the same config output:\n%s", rc)
	}
	// Re-parsing each keeps its kind, so the distinction survives a round trip.
	rn2, _ := mustParse(t, string(rc)).Get("listen")
	an2, _ := mustParse(t, string(ac)).Get("listen")
	if rn2.Kind() != KindString {
		t.Errorf("after round trip, repeated key became %v", rn2.Kind())
	}
	if an2.Kind() != KindArray {
		t.Errorf("after round trip, array became %v", an2.Kind())
	}
}

// A single-valued key is unaffected, so every correct existing read keeps
// working. This is what makes the change safe to adopt.
func TestSingleValuedKeyUnaffected(t *testing.T) {
	doc := mustParse(t, `listen = "10.0.0.1:80";`)
	n, _ := doc.Get("listen")

	if got, ok := doc.String("listen"); !ok || got != "10.0.0.1:80" {
		t.Errorf(`String = %q, %v; want the value, true`, got, ok)
	}
	if got := n.Len(); got != 0 {
		t.Errorf("Len = %d, want 0: one value is not a sequence", got)
	}
	if _, ok := n.Index(0); ok {
		t.Error("Index(0) should miss on a single value")
	}
	if _, ok := doc.Get("listen.0"); ok {
		t.Error("listen.0 should miss on a single value")
	}
	count := 0
	for range n.Values() {
		count++
	}
	if count != 1 {
		t.Errorf("Values yielded %d, want 1", count)
	}
}

func TestRepeatedKeyOfObjects(t *testing.T) {
	doc := mustParse(t, "server { port = 80; }\nserver { port = 443; }\n")
	n, ok := doc.Get("server")
	if !ok {
		t.Fatal("server missing")
	}
	if got := n.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
	if n.Kind() != KindObject {
		t.Errorf("Kind = %v, want object", n.Kind())
	}
	for i, want := range []int64{80, 443} {
		got, ok := doc.Int("server." + string(rune('0'+i)) + ".port")
		if !ok || got != want {
			t.Errorf("server.%d.port = %d, %v; want %d, true", i, got, ok, want)
		}
	}
}

// Nested under a key, so the path fallback has to survive more than one
// segment.
func TestRepeatedKeyNested(t *testing.T) {
	doc := mustParse(t, "http {\n  listen = 80;\n  listen = 443;\n}\n")
	if got, ok := doc.Int("http.listen.1"); !ok || got != 443 {
		t.Errorf("http.listen.1 = %d, %v; want 443, true", got, ok)
	}
	if _, ok := doc.Int("http.listen"); ok {
		t.Error("http.listen should refuse: it holds two values")
	}
	n, _ := doc.Get("http.listen")
	if got := n.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
}

// Every typed accessor refuses, not just String.
func TestAllAccessorsRefuseMultiValued(t *testing.T) {
	doc := mustParse(t, "n = 1; n = 2;\ns = \"a\"; s = \"b\";\nb = true; b = false;\nf = 1.5; f = 2.5;\nd = 10s; d = 20s;\n")
	if _, ok := doc.Int("n"); ok {
		t.Error("Int accepted a repeated key")
	}
	if _, ok := doc.String("s"); ok {
		t.Error("String accepted a repeated key")
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
