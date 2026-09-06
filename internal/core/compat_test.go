package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The compatibility surface is proven end to end by rebuilding
// github.com/mitchellh/go-libucl on top of it and running that package's own
// unmodified test suite. These tests pin the behaviour that suite depends on
// but does not directly assert, and the quirks it would be easy to "fix" by
// accident.

// compatObjectAPI is the exact method set go-libucl's decoder.go and test
// suite call on *Object. The assignment below fails to compile if a signature
// drifts -- including the asymmetry between Object.Close, which returns an
// error, and ObjectIter.Close, which returns nothing.
type compatObjectAPI interface {
	Close() error
	Emit(Emitter) (string, error)
	Delete(string)
	Get(string) *CompatObject
	Iterate(bool) *CompatObjectIter
	Key() string
	Len() uint
	Ref() error
	Type() Type
	ToBool() bool
	ToInt() int64
	ToFloat() float64
	ToString() string
}

type compatIterAPI interface {
	Close()
	Next() *CompatObject
}

type compatParserAPI interface {
	AddString(string) error
	AddFile(string) error
	Close()
	Object() *CompatObject
	RegisterMacro(string, MacroFunc)
}

var (
	_ compatObjectAPI = (*CompatObject)(nil)
	_ compatIterAPI   = (*CompatObjectIter)(nil)
	_ compatParserAPI = (*CompatParser)(nil)
)

func compatParse(t *testing.T, data string) *CompatObject {
	t.Helper()

	obj, err := CompatParseString(data)
	if err != nil {
		t.Fatalf("CompatParseString(%q): %v", data, err)
	}
	if obj == nil {
		t.Fatalf("CompatParseString(%q): no object", data)
	}
	return obj
}

func TestCompatParseString(t *testing.T) {
	obj := compatParse(t, "foo = bar;")
	if obj.Type() != TypeObject {
		t.Fatalf("root type = %v, want object", obj.Type())
	}

	v := obj.Get("foo")
	if v == nil {
		t.Fatal(`Get("foo") = nil`)
	}
	if v.Type() != TypeString {
		t.Errorf("value type = %v, want string", v.Type())
	}
	if v.Key() != "foo" {
		t.Errorf("Key() = %q, want %q", v.Key(), "foo")
	}
	if v.ToString() != "bar" {
		t.Errorf("ToString() = %q, want %q", v.ToString(), "bar")
	}
	if obj.Get("nope") != nil {
		t.Error(`Get("nope") should be nil`)
	}
}

// The root object must stay usable after the parser that produced it is
// closed: ParseString closes the parser before it returns.
func TestCompatObjectOutlivesItsParser(t *testing.T) {
	p := NewCompatParser(ParserDefault)
	if err := p.AddString("foo = bar;"); err != nil {
		t.Fatalf("AddString: %v", err)
	}
	obj := p.Object()
	p.Close()
	p.Close() // inert, so twice is harmless

	if got := obj.Get("foo").ToString(); got != "bar" {
		t.Fatalf("after Close, ToString() = %q, want %q", got, "bar")
	}
}

func TestCompatParserErrors(t *testing.T) {
	p := NewCompatParser(ParserDefault)
	defer p.Close()

	err := p.AddString("foo = {")
	if err == nil {
		t.Fatal("AddString of a truncated object should fail")
	}
	if err.Error() == "" {
		t.Error("the error should carry the parser's message")
	}

	if err := NewCompatParser(ParserDefault).AddFile(
		filepath.Join(t.TempDir(), "absent.conf")); err == nil {
		t.Error("AddFile of a missing file should fail")
	}
}

func TestCompatParserAddFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.conf")
	if err := os.WriteFile(path, []byte("foo = bar;"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewCompatParser(ParserDefault)
	defer p.Close()
	if err := p.AddFile(path); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if got := p.Object().Get("foo").ToString(); got != "bar" {
		t.Fatalf("ToString() = %q, want %q", got, "bar")
	}
}

// An empty chunk is a chunk, not the C API's "read until NUL" case.
func TestCompatAddStringEmpty(t *testing.T) {
	p := NewCompatParser(ParserDefault)
	defer p.Close()

	if err := p.AddString(""); err != nil {
		t.Fatalf("AddString(%q): %v", "", err)
	}
	obj := p.Object()
	if obj == nil {
		t.Fatal("empty input should still produce a root object")
	}
	if obj.Len() != 0 {
		t.Errorf("Len() = %d, want 0", obj.Len())
	}
}

// The macro fires during AddString, not later, and receives the argument
// bytes as they appeared in the source.
func TestCompatRegisterMacro(t *testing.T) {
	var calls []string

	p := NewCompatParser(ParserDefault)
	defer p.Close()
	p.RegisterMacro("foo", func(data string) {
		calls = append(calls, data)
	})

	if err := p.AddString(`.foo "bar";`); err != nil {
		t.Fatalf("AddString: %v", err)
	}
	if want := []string{"bar"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("macro calls = %#v, want %#v", calls, want)
	}
}

func TestCompatLen(t *testing.T) {
	tests := []struct {
		name string
		src  string
		key  string
		want uint
	}{
		// Plain paths: the tracked length of a container.
		{"array", "foo = [foo, bar, baz];", "foo", 3},
		{"object", "foo { a = 1; b = 2; }", "foo", 2},

		// The quirk. Both blocks are objects, so libucl chains them into an
		// implicit array whose head is still an object; the head's own length
		// counts its keys, not the chain, so Len counts by iterating.
		{"repeated block", `bundle "foo" {}; bundle "bar" {};`, "bundle", 2},

		// The latent other half: a chain whose head is not an object is not
		// special-cased, so this reports the head's raw length field -- for a
		// string, its byte count. Nothing in go-libucl asserts this, but the
		// C binding would return 3 here and so must this.
		{"repeated scalar", "foo = foo; foo = bar;", "foo", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := compatParse(t, tt.src).Get(tt.key)
			if v == nil {
				t.Fatalf("Get(%q) = nil", tt.key)
			}
			if got := v.Len(); got != tt.want {
				t.Fatalf("Len() = %d, want %d", got, tt.want)
			}
		})
	}

	if got := compatParse(t, "foo = bar; baz = boo;").Len(); got != 2 {
		t.Errorf("root Len() = %d, want 2", got)
	}
}

// Emit never reports an error, whatever happens underneath: where the C
// returned NULL the cgo binding reported success with an empty string.
func TestCompatEmit(t *testing.T) {
	obj := compatParse(t, "foo = bar; bar = baz;")

	tests := []struct {
		name   string
		format Emitter
		want   string
	}{
		{"json", EmitJSON, "{\n    \"foo\": \"bar\",\n    \"bar\": \"baz\"\n}"},
		{"json compact", EmitJSONCompact, `{"foo":"bar","bar":"baz"}`},
		{"config", EmitConfig, "foo = \"bar\";\nbar = \"baz\";\n"},
		{"msgpack", EmitMsgPack, "\x82\xa3foo\xa3bar\xa3bar\xa3baz"},

		// Not a format at all: ("", nil), never an error.
		{"out of range", Emitter(42), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := obj.Emit(tt.format)
			if err != nil {
				t.Fatalf("Emit returned an error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Emit() = %q, want %q", got, tt.want)
			}
		})
	}

	var missing *CompatObject
	if got, err := missing.Emit(EmitJSON); got != "" || err != nil {
		t.Fatalf("nil.Emit() = (%q, %v), want (%q, nil)", got, err, "")
	}
}

// Repeated lookups of one key must yield the same node, not equal copies:
// decoder_test.go compares an independently fetched *Object against one the
// decoder stored, with reflect.DeepEqual.
func TestCompatGetIdentity(t *testing.T) {
	obj := compatParse(t, "value { foo = bar; }")

	a := obj.Get("value").Get("foo")
	b := obj.Get("value").Get("foo")
	if a != b {
		t.Fatalf("Get twice returned different nodes: %p and %p", a, b)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Get twice returned nodes that are not deeply equal")
	}

	// Iteration must reach the same node as the lookup.
	iter := obj.Get("value").Iterate(true)
	defer iter.Close()
	if got := iter.Next(); got != a {
		t.Fatalf("iteration yielded %p, lookup yielded %p", got, a)
	}
}

func TestCompatIterate(t *testing.T) {
	// expand=true over an object yields its values in declaration order.
	var keys, values []string
	iter := compatParse(t, "foo = bar; bar = baz;").Iterate(true)
	for elt := iter.Next(); elt != nil; elt = iter.Next() {
		keys = append(keys, elt.Key())
		values = append(values, elt.ToString())
		elt.Close()
	}
	iter.Close()
	if want := []string{"foo", "bar"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %#v, want %#v", keys, want)
	}
	if want := []string{"bar", "baz"}; !reflect.DeepEqual(values, want) {
		t.Errorf("values = %#v, want %#v", values, want)
	}

	// expand=true over an array yields its elements in order.
	values = nil
	iter = compatParse(t, "foo = [foo, bar, baz];").Get("foo").Iterate(true)
	for elt := iter.Next(); elt != nil; elt = iter.Next() {
		values = append(values, elt.ToString())
		elt.Close()
	}
	iter.Close()
	if want := []string{"foo", "bar", "baz"}; !reflect.DeepEqual(values, want) {
		t.Errorf("array values = %#v, want %#v", values, want)
	}

	// expand=false walks the implicit-array chain instead: one step per
	// definition of the key. This is the distinction the decoder switches on.
	var count int
	iter = compatParse(t, `bundle "foo" {}; bundle "bar" {};`).Get("bundle").Iterate(false)
	for elt := iter.Next(); elt != nil; elt = iter.Next() {
		count++
		elt.Close()
	}
	iter.Close()
	if count != 2 {
		t.Errorf("unexpanded chain walk visited %d links, want 2", count)
	}

	// A scalar yields itself, so a lone value still decodes into a slice.
	count = 0
	iter = compatParse(t, "foo = bar;").Get("foo").Iterate(true)
	for elt := iter.Next(); elt != nil; elt = iter.Next() {
		count++
		elt.Close()
	}
	iter.Close()
	if count != 1 {
		t.Errorf("scalar iteration yielded %d values, want 1", count)
	}
}

// Abandoning an iterator part way through, then closing it, must be safe.
func TestCompatIteratePartial(t *testing.T) {
	iter := compatParse(t, "a = 1; b = 2; c = 3;").Iterate(true)
	if iter.Next() == nil {
		t.Fatal("iteration produced nothing")
	}
	iter.Close()
	iter.Close()
}

func TestCompatDelete(t *testing.T) {
	obj := compatParse(t, "bar = baz;")

	obj.Delete("foo") // absent: a silent no-op
	if obj.Get("bar") == nil {
		t.Fatal("deleting an absent key removed the wrong one")
	}

	obj.Delete("bar")
	if obj.Get("bar") != nil {
		t.Fatal(`Get("bar") after Delete should be nil`)
	}
}

func TestCompatConversions(t *testing.T) {
	obj := compatParse(t, `i = 42; f = 0.5; b = true; s = hello; arr = [1];`)

	if got := obj.Get("i").ToInt(); got != 42 {
		t.Errorf("ToInt() = %d, want 42", got)
	}
	if got := obj.Get("f").ToFloat(); got != 0.5 {
		t.Errorf("ToFloat() = %v, want 0.5", got)
	}
	if got := obj.Get("b").ToBool(); !got {
		t.Error("ToBool() = false, want true")
	}
	if got := obj.Get("s").ToString(); got != "hello" {
		t.Errorf("ToString() = %q, want %q", got, "hello")
	}

	// A rejected conversion yields the zero value, never an error: these are
	// the C's unsafe accessors.
	if got := obj.Get("arr").ToInt(); got != 0 {
		t.Errorf("ToInt() of an array = %d, want 0", got)
	}
	if got := obj.Get("i").ToString(); got != "" {
		t.Errorf("ToString() of an int = %q, want %q", got, "")
	}

	// An object with no key of its own reports "", the C's NULL key.
	if got := obj.Key(); got != "" {
		t.Errorf("root Key() = %q, want %q", got, "")
	}
}

// Ref and Close are inert, and neither can fail. Ref still runs the C's
// ephemeral branch, which deep copies; the cgo binding discarded that copy,
// so the object the caller holds is unaffected either way.
func TestCompatRefAndCloseAreInert(t *testing.T) {
	obj := compatParse(t, "foo = bar;")
	v := obj.Get("foo")

	for i := 0; i < 3; i++ {
		if err := v.Ref(); err != nil {
			t.Fatalf("Ref returned %v", err)
		}
		if err := v.Close(); err != nil {
			t.Fatalf("Close returned %v", err)
		}
	}
	if got := v.ToString(); got != "bar" {
		t.Fatalf("after Ref/Close cycles, ToString() = %q, want %q", got, "bar")
	}

	(*Object)(v).flags |= FlagEphemeral
	if err := v.Ref(); err != nil {
		t.Fatalf("Ref of an ephemeral object returned %v", err)
	}
	if got := v.ToString(); got != "bar" {
		t.Fatalf("the ephemeral copy leaked into the caller's object: %q", got)
	}
}
