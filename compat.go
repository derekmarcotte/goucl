package goucl

import (
	"bytes"
	"errors"
)

// This file is the drop-in surface for github.com/mitchellh/go-libucl: the
// exact method sets its decoder.go and its test suite call, reproduced so that
// package can be rebuilt as a shim of type aliases over this one and keep its
// own tests unmodified. See docs/reference/compat.md for the per-item C semantics and
// docs/notes/compat-plan.md for the shape of the shim.
//
// Naming. The plan called for these methods to live on Object and Parser
// themselves. Four of the names they need are already taken by the C-mirror
// API with incompatible signatures:
//
//	Object.Key() []byte                              vs. Key() string
//	Parser.AddString(string) bool                     vs. AddString(string) error
//	Parser.AddFile(string) bool                       vs. AddFile(string) error
//	Parser.RegisterMacro(string, MacroHandler) bool   vs. RegisterMacro(string, MacroFunc)
//
// so the compatibility surface gets its own types. CompatObject and
// CompatParser are *defined types over the same structs*, not wrappers: the
// conversions below are free pointer conversions, so a CompatObject is
// literally the node it came from. That preserves the identity the C binding
// got from passing raw ucl_object_t pointers around — two lookups of the same
// key return the same pointer, which docs/reference/compat.md section 4.2 requires — and
// it keeps the two API sets from colliding.
//
// Refcounting is gone: Ref, Close and Parser.Close are inert, because the GC
// owns lifetime here. Their signatures are kept exactly, including the
// asymmetry between Object.Close() error and CompatObjectIter.Close().

// MacroFunc is the callback type for macros, mirroring go-libucl's MacroFunc.
// It receives the raw macro argument bytes as they appeared in the source.
type MacroFunc func(string)

// CompatObject is a UCL object presented with go-libucl's method set.
//
// It is the same node as an Object and converts to and from one for free; the
// two differ only in the methods they carry. Every method here mirrors the cgo
// binding's object.go, quirks included.
type CompatObject Object

// CompatObjectIter iterates the children of a CompatObject, mirroring
// go-libucl's ObjectIter. It is single-pass and need not be fully consumed.
type CompatObjectIter struct {
	expand bool
	obj    *Object
	iter   Iter
}

// CompatParser is a UCL parser presented with go-libucl's method set.
//
// As with CompatObject it is the same parser as a Parser, differing only in
// the methods it carries: the ones here report failure as an error rather than
// a bool, matching the cgo binding, which turned ucl_parser_get_error into a
// Go error at every call site.
type CompatParser Parser

// CompatParseString parses data with default flags and returns the root
// object, mirroring go-libucl's package-level ParseString.
//
// The parser is closed before returning. The object outlives it: the C took
// its own reference in ucl_parser_get_object, and here the GC keeps the tree
// alive for as long as the caller holds it.
func CompatParseString(data string) (*CompatObject, error) {
	p := NewCompatParser(ParserDefault)
	defer p.Close()

	if err := p.AddString(data); err != nil {
		return nil, err
	}
	return p.Object(), nil
}

// NewCompatParser returns a parser with go-libucl's method set, mirroring its
// package-level NewParser.
func NewCompatParser(flags ParserFlag) *CompatParser {
	return (*CompatParser)(NewParser(flags))
}

// AddString parses data as the next chunk, mirroring
// ucl_parser_add_string(p, data, len(data)).
//
// The true byte length is always passed, so an empty string is an empty chunk
// rather than the C API's "read until NUL" special case.
func (p *CompatParser) AddString(data string) error {
	if (*Parser)(p).AddString(data) {
		return nil
	}
	return p.lastError()
}

// AddFile parses the named file as the next chunk, mirroring
// ucl_parser_add_file.
func (p *CompatParser) AddFile(path string) error {
	if (*Parser)(p).AddFile(path) {
		return nil
	}
	return p.lastError()
}

// lastError reports the parser's recorded error the way the cgo binding did:
// errors.New(C.GoString(ucl_parser_get_error(p))). C.GoString of a NULL
// pointer is "", so a failure that recorded no message still produced a
// non-nil error, and callers testing err != nil still saw the failure.
func (p *CompatParser) lastError() error {
	if err := (*Parser)(p).GetError(); err != nil {
		return err
	}
	return errors.New("")
}

// Close releases the parser, mirroring ucl_parser_free plus the binding's
// macro-map cleanup. Both were memory management for the C side and are inert
// here, so a double Close is harmless. Once closed the parser should not be
// used again.
func (p *CompatParser) Close() {
	(*Parser)(p).Free()
}

// Object returns the root object of the parse, or nil if nothing was parsed,
// mirroring ucl_parser_get_object.
func (p *CompatParser) Object() *CompatObject {
	obj := (*Parser)(p).GetObject()
	if obj == nil {
		return nil
	}
	return (*CompatObject)(obj)
}

// RegisterMacro registers a macro handler called synchronously, during the
// AddString or AddFile that reaches the macro. It mirrors
// ucl_parser_register_macro; name is given without its leading '.'.
func (p *CompatParser) RegisterMacro(name string, f MacroFunc) {
	(*Parser)(p).RegisterMacro(name, func(data []byte, args *Object) bool {
		// The C bridge looked the closure up in a global map and reported
		// failure when it was absent; a nil MacroFunc is the same case.
		if f == nil {
			return false
		}
		f(string(data))
		return true
	})
}

// Close releases the object, mirroring ucl_object_unref.
//
// It is inert: nothing is freed, so calling it twice, or using the object
// afterwards, is safe. The error is always nil, as it was in the cgo binding.
func (o *CompatObject) Close() error {
	Unref((*Object)(o))
	return nil
}

// Ref takes a reference to the object, mirroring ucl_object_ref.
//
// It is inert for the same reason Close is, and the error is always nil. One
// branch of the C is still real: an ephemeral object is deep copied rather
// than referenced. The cgo binding discarded that copy (it kept using its own
// pointer), so the call is observably a no-op there too.
func (o *CompatObject) Ref() error {
	Ref((*Object)(o))
	return nil
}

// Type returns the object's type, mirroring ucl_object_type.
func (o *CompatObject) Type() Type {
	return (*Object)(o).Type()
}

// Key returns the object's key, or "" if it has none, mirroring
// C.GoString(ucl_object_key(o)), which turned the C's NULL into "".
func (o *CompatObject) Key() string {
	return string((*Object)(o).Key())
}

// Get returns the value stored under key, or nil, mirroring
// ucl_object_find_keyl(o, key, len(key)).
//
// The key is matched by exact bytes, and the result is the head of any
// implicit-array chain formed by duplicate keys. Repeated calls return the
// identical pointer.
func (o *CompatObject) Get(key string) *CompatObject {
	elt := (*Object)(o).LookupLen([]byte(key))
	if elt == nil {
		return nil
	}
	// The cgo binding took a reference here so the caller owned one. That is
	// inert now; for an ephemeral object the C deep copied and then dropped
	// the copy, so omitting the call changes nothing observable.
	return (*CompatObject)(elt)
}

// Delete removes key and everything on its implicit-array chain, mirroring
// ucl_object_delete_key. Deleting an absent key is a silent no-op: the C's
// "was it there" result was discarded by the binding and is discarded here.
func (o *CompatObject) Delete(key string) {
	(*Object)(o).DeleteKey([]byte(key))
}

// Iterate returns an iterator over this object's children.
//
// With expand true a container yields its values and a scalar yields itself;
// with expand false the iteration walks the object's own implicit-array chain,
// so a key defined more than once yields one step per definition. The
// difference is load-bearing for the decoder, which switches between the two
// at different depths.
//
// The iterator must be closed, and need not be fully consumed.
func (o *CompatObject) Iterate(expand bool) *CompatObjectIter {
	return &CompatObjectIter{expand: expand, obj: (*Object)(o)}
}

// Len returns the number of elements in the object.
//
// For an array this is the element count and for an object the key count, read
// from the tracked length. The exception is the C binding's own quirk, kept
// deliberately: an object that is itself a link in an implicit-array chain
// (the same key defined more than once as a block) has a length describing
// only its own keys, so the chain is counted by iterating it instead. Scalars
// report their raw tracked length, whatever that happens to mean for the type.
func (o *CompatObject) Len() uint {
	if o == nil {
		return 0
	}

	if o.Type() == TypeObject && o.Next != nil {
		iter := o.Iterate(false)
		defer iter.Close()

		var count uint
		for elt := iter.Next(); elt != nil; elt = iter.Next() {
			elt.Close()
			count++
		}
		return count
	}

	return uint(o.length)
}

// Emit renders the object in the given format, mirroring ucl_object_emit.
//
// The error is always nil, exactly as in the cgo binding: where the C returned
// NULL the binding reported success with an empty string, so a caller checking
// err != nil never saw anything. A format outside the enum, or an emitter
// failure, therefore yields ("", nil) rather than an error.
func (o *CompatObject) Emit(t Emitter) (string, error) {
	var buf bytes.Buffer

	var err error
	if t == EmitMsgPack {
		err = EncodeMsgPackTo(&buf, (*Object)(o))
	} else {
		err = EmitTextFull(&buf, (*Object)(o), t, nil)
	}
	if err != nil {
		return "", nil
	}
	return buf.String(), nil
}

// ToBool converts the object to a bool, mirroring ucl_object_toboolean.
// A conversion the C rejects yields false, with no way to tell that apart from
// a real false.
func (o *CompatObject) ToBool() bool {
	return ToBoolean((*Object)(o))
}

// ToInt converts the object to an int64, mirroring ucl_object_toint.
// A rejected conversion yields 0.
func (o *CompatObject) ToInt() int64 {
	return ToInt((*Object)(o))
}

// ToFloat converts the object to a float64, mirroring ucl_object_todouble.
// A rejected conversion yields 0.
func (o *CompatObject) ToFloat() float64 {
	return ToDouble((*Object)(o))
}

// ToString returns the object's string value, mirroring
// C.GoString(ucl_object_tostring(o)). Only a string converts; anything else
// yields "", the C's NULL.
func (o *CompatObject) ToString() string {
	return ToString((*Object)(o))
}

// Close releases the iterator, mirroring ucl_object_unref on the object the
// iterator referenced.
//
// It is inert, and unlike CompatObject.Close it returns nothing. That
// asymmetry is part of the call-site contract and is deliberate.
func (it *CompatObjectIter) Close() {
	Unref(it.obj)
}

// Next returns the next child, or nil when the iteration is done, mirroring
// ucl_iterate_object. The iterator is single-pass: once exhausted it stays so.
func (it *CompatObjectIter) Next() *CompatObject {
	elt := Iterate(it.obj, &it.iter, it.expand)
	if elt == nil {
		return nil
	}
	return (*CompatObject)(elt)
}
