// Package libucl parses and manipulates configuration in the Universal
// Configuration Language.
//
// It is a drop-in replacement for github.com/mitchellh/go-libucl, the cgo
// binding to the C libucl, and exists so that programs using that binding can
// drop the C toolchain without changing their code. The API is identical and
// the migration is an import-path swap:
//
//	import "github.com/derekmarcotte/goucl/compat/libucl"
//
// Existing call sites keep compiling, because the types, method sets and
// numeric constants are the ones the binding exposed. That claim is tested
// rather than asserted: the binding's own test suite runs against this
// package, from its own files. Three are byte-identical to the commit they
// came from; the fourth differs only by the markers disabling one test that
// is broken upstream, with the evidence recorded beside it. Run
// tools/check-compat-identity.sh to verify both.
//
// New code should prefer github.com/derekmarcotte/goucl, which offers the same
// engine behind an API written for Go rather than for C. This package exists
// for migration and makes no attempt to be pleasant.
//
// # Attribution
//
// The API reproduced here is Mitchell Hashimoto's work, from
// github.com/mitchellh/go-libucl at commit f9d0e55 (2015-11-19): the design,
// the "libucl" struct tag, the type and constant names and the method sets are
// all his, carried over deliberately rather than reinvented. decoder.go,
// object_test.go and parser_test.go are his files unmodified; decoder_test.go
// is his too, differing only by the markers disabling one test that is broken
// upstream, with the evidence recorded beside it.
//
// That repository states no licence: it carries no LICENSE file, no per-file
// copyright headers and no licence statement in its README, and it is archived
// with no instruction about reuse. The credit given here is therefore credit
// and not a licence grant. See the NOTICE file at the repository root before
// redistributing this package.
//
// For that reason this package is a separate Go module, required explicitly
// rather than pulled in with the rest of goucl. Programs using only the
// idiomatic API never carry these files. The import path is unaffected.
//
// This file is the shim that puts the API of the original cgo binding on top
// of the pure-Go github.com/derekmarcotte/goucl/internal/core. It replaces
// the binding's parser.go, object.go, libucl.go and go-libucl.h, and with
// them the dependency on the C libucl. Everything else in the package comes
// from the binding, which is the point: the original test suite exercises the
// Go implementation exactly as it exercised the C one.
//
// Object, ObjectIter and Parser are defined types over their engine
// counterparts, so a pointer to one *is* a pointer to the other: the
// conversions below are free and preserve node identity, which is what the
// binding got from passing raw ucl_object_t pointers around. They are not
// aliases because decoder.go declares a method, Decode, on Object, and Go only
// allows that on a type declared in this package. They are not wrappers
// either, because a wrapper would have to allocate on every lookup and would
// not compare equal under reflect.DeepEqual the way decoder_test.go expects.
//
// What was C memory management is now inert: Object.Ref, Object.Close,
// ObjectIter.Close and Parser.Close do nothing, because the garbage collector
// owns lifetime here. Their signatures are unchanged so that existing call
// sites keep compiling, including the asymmetry between Object.Close, which
// returns an error, and ObjectIter.Close, which returns nothing.
package libucl

import "github.com/derekmarcotte/goucl/internal/core"

// Object represents a single object within a configuration.
type Object core.CompatObject

// ObjectIter is an interator for objects.
type ObjectIter core.CompatObjectIter

// Parser is responsible for parsing libucl data.
type Parser core.CompatParser

// The remaining types need no methods of their own, so they are plain
// aliases: the numeric values are the engine's, which are the C enums' values.
type (
	// ObjectType is an enum of the type that an Object represents.
	ObjectType = core.Type

	// Emitter is a type of built-in emitter that can be used to convert
	// an object to another config format.
	Emitter = core.Emitter

	// ParserFlag are flags that can be used to initialize a parser.
	//
	// ParserKeyLowercase will lowercase all keys.
	//
	// ParserKeyZeroCopy will attempt to do a zero-copy parse if possible.
	ParserFlag = core.ParserFlag

	// MacroFunc is the callback type for macros.
	MacroFunc = core.MacroFunc
)

// The object types, with the values of the C enum ucl_type_t.
const (
	ObjectTypeObject   = core.TypeObject
	ObjectTypeArray    = core.TypeArray
	ObjectTypeInt      = core.TypeInt
	ObjectTypeFloat    = core.TypeFloat
	ObjectTypeString   = core.TypeString
	ObjectTypeBoolean  = core.TypeBoolean
	ObjectTypeTime     = core.TypeTime
	ObjectTypeUserData = core.TypeUserdata
	ObjectTypeNull     = core.TypeNull
)

// The emitters, with the values of the C enum ucl_emitter_t.
const (
	EmitJSON        = core.EmitJSON
	EmitJSONCompact = core.EmitJSONCompact
	EmitConfig      = core.EmitConfig
	EmitYAML        = core.EmitYAML
)

// The parser flags, with the values of the C enum ucl_parser_flags_t.
const (
	ParserKeyLowercase = core.ParserKeyLowercase
	ParserZeroCopy     = core.ParserZeroCopy
	ParserNoTime       = core.ParserNoTime
)

// ParseString parses a string and returns the top-level object.
func ParseString(data string) (*Object, error) {
	obj, err := core.CompatParseString(data)
	return (*Object)(obj), err
}

// NewParser returns a parser
func NewParser(flags ParserFlag) *Parser {
	return (*Parser)(core.NewCompatParser(flags))
}

func (p *Parser) compat() *core.CompatParser { return (*core.CompatParser)(p) }

// AddString adds a string data to parse.
func (p *Parser) AddString(data string) error { return p.compat().AddString(data) }

// AddFile adds a file to parse.
func (p *Parser) AddFile(path string) error { return p.compat().AddFile(path) }

// Close closes the parser. Once it is closed it can no longer be used.
func (p *Parser) Close() { p.compat().Close() }

// Object retrieves the root-level object for a configuration.
func (p *Parser) Object() *Object { return (*Object)(p.compat().Object()) }

// RegisterMacro registers a macro that is called from the configuration.
func (p *Parser) RegisterMacro(name string, f MacroFunc) { p.compat().RegisterMacro(name, f) }

func (o *Object) compat() *core.CompatObject { return (*core.CompatObject)(o) }

// Close frees the memory associated with the object.
func (o *Object) Close() error { return o.compat().Close() }

// Emit converts this object to another format and returns it.
func (o *Object) Emit(t Emitter) (string, error) { return o.compat().Emit(t) }

// Delete removes the given key from the object.
func (o *Object) Delete(key string) { o.compat().Delete(key) }

// Get returns the value stored under key, or nil if there is none.
func (o *Object) Get(key string) *Object { return (*Object)(o.compat().Get(key)) }

// Iterate over the objects in this object.
//
// The iterator must be closed when it is finished.
//
// The iterator does not need to be fully consumed.
func (o *Object) Iterate(expand bool) *ObjectIter {
	return (*ObjectIter)(o.compat().Iterate(expand))
}

// Key returns the key of this value/object as a string, or the empty
// string if the object doesn't have a key.
func (o *Object) Key() string { return o.compat().Key() }

// Len returns the length of the object, or how many elements are part
// of this object.
//
// For objects, this is the number of key/value pairs.
// For arrays, this is the number of elements.
func (o *Object) Len() uint { return o.compat().Len() }

// Ref increments the ref count associated with this.
func (o *Object) Ref() error { return o.compat().Ref() }

// Type returns the type that this object represents.
func (o *Object) Type() ObjectType { return o.compat().Type() }

//------------------------------------------------------------------------
// Conversion Functions
//------------------------------------------------------------------------

func (o *Object) ToBool() bool { return o.compat().ToBool() }

func (o *Object) ToInt() int64 { return o.compat().ToInt() }

func (o *Object) ToFloat() float64 { return o.compat().ToFloat() }

func (o *Object) ToString() string { return o.compat().ToString() }

// Close releases the iterator. It returns nothing, unlike Object.Close.
func (it *ObjectIter) Close() { (*core.CompatObjectIter)(it).Close() }

// Next returns the next object of the iteration, or nil when it is done.
func (it *ObjectIter) Next() *Object {
	return (*Object)((*core.CompatObjectIter)(it).Next())
}
