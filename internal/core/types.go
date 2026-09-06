// Package core is a pure-Go implementation of libucl, the Universal
// Configuration Language library (https://github.com/vstakhov/libucl).
//
// The structure deliberately mirrors the C API one-to-one so that libucl's
// own test suite ports across mechanically and behaviour can be diffed
// against the C implementation. That fidelity is what the goldens measure,
// so this package is the engine rather than the public surface: the
// idiomatic API in the parent package and the drop-in in compat/libucl are
// both built on top of it.
//
// Behaviour is pinned against output captured from the C library; see
// docs/ for the per-component specs and docs/findings.md for the upstream
// defects this package reproduces deliberately.
package core

// Type is the type of an Object, mirroring C ucl_type_t.
//
// The numeric values are part of the compatibility contract:
// github.com/mitchellh/go-libucl exposes them as iota-based constants and
// callers compare against them, so the ordering must not change.
type Type uint16

const (
	TypeObject   Type = iota // key/value pairs
	TypeArray                // array of values
	TypeInt                  // integer number
	TypeFloat                // floating point number
	TypeString               // string
	TypeBoolean              // boolean
	TypeTime                 // time, as a floating point number of seconds
	TypeUserdata             // opaque user data
	TypeNull                 // null
)

// String returns the type name used by ucl_object_type_to_string.
func (t Type) String() string {
	switch t {
	case TypeObject:
		return "object"
	case TypeArray:
		return "array"
	case TypeInt:
		return "integer"
	case TypeFloat:
		return "float"
	case TypeString:
		return "string"
	case TypeBoolean:
		return "boolean"
	case TypeTime:
		return "time"
	case TypeUserdata:
		return "userdata"
	case TypeNull:
		return "null"
	}
	return "unknown"
}

// Emitter selects an output format, mirroring C ucl_emitter_t. As with Type,
// the numeric values are part of the compatibility contract.
type Emitter int

const (
	EmitJSON        Emitter = iota // formatted JSON
	EmitJSONCompact                // compact JSON
	EmitConfig                     // human readable config format
	EmitYAML                       // embedded YAML
	EmitMsgPack                    // MessagePack
)

// ParserFlag controls parser behaviour, mirroring C ucl_parser_flags_t.
type ParserFlag int

const (
	ParserDefault          ParserFlag = 0
	ParserKeyLowercase     ParserFlag = 1 << 0 // convert all keys to lower case
	ParserZeroCopy         ParserFlag = 1 << 1 // parse in zero-copy mode where possible
	ParserNoTime           ParserFlag = 1 << 2 // treat time values as strings
	ParserNoImplicitArrays ParserFlag = 1 << 3 // explicit arrays instead of implicit ones
	ParserSaveComments     ParserFlag = 1 << 4 // retain comments in the parser context
	ParserDisableMacro     ParserFlag = 1 << 5 // do not expand macros
	ParserNoFilevars       ParserFlag = 1 << 6 // do not set file variables
)

// ObjectFlag is a per-object flag, mirroring C ucl_object_flags_t.
//
// Only the low 12 bits hold flags; the high 4 bits hold the priority. Use
// Priority and SetPriority rather than manipulating those bits directly.
type ObjectFlag uint16

const (
	FlagAllocatedKey   ObjectFlag = 1 << 0 // key is owned by the object
	FlagAllocatedValue ObjectFlag = 1 << 1 // string value is owned by the object
	FlagNeedKeyEscape  ObjectFlag = 1 << 2 // key must be escaped on output
	FlagEphemeral      ObjectFlag = 1 << 3 // temporary object; Ref deep-copies it
	FlagMultiline      ObjectFlag = 1 << 4 // emit as a multiline string
	FlagMultivalue     ObjectFlag = 1 << 5 // key with multiple values
	FlagInherited      ObjectFlag = 1 << 6 // inherited from another object
	FlagBinary         ObjectFlag = 1 << 7 // value holds raw binary data
	FlagSquoted        ObjectFlag = 1 << 8 // value was single-quoted in the source
)

// prioBits is C PRIOBITS: the number of high flag bits reserved for priority.
const prioBits = 4

// flagMask covers the bits of Flags that hold actual flags rather than
// priority: the low 16-prioBits == 12 bits.
const flagMask = ObjectFlag(1<<(16-prioBits)) - 1

// StringFlag controls FromStringCommon, mirroring C ucl_string_flags_t.
type StringFlag int

const (
	StringRaw          StringFlag = 0
	StringEscape       StringFlag = 1 << 0 // perform JSON escaping
	StringTrim         StringFlag = 1 << 1 // trim surrounding whitespace
	StringParseBoolean StringFlag = 1 << 2 // detect booleans
	StringParseInt     StringFlag = 1 << 3 // detect integers
	StringParseDouble  StringFlag = 1 << 4 // detect floats
	StringParseTime    StringFlag = 1 << 5 // detect time values
	StringParseBytes   StringFlag = 1 << 6 // treat numeric suffixes as byte sizes

	StringParseNumber = StringParseInt | StringParseDouble | StringParseTime
	StringParse       = StringParseBoolean | StringParseNumber
)

// DuplicateStrategy selects how a duplicate key is resolved, mirroring
// C enum ucl_duplicate_strategy.
type DuplicateStrategy int

const (
	DuplicateAppend  DuplicateStrategy = iota // merge based on priorities
	DuplicateMerge                            // merge the new object into the old
	DuplicateRewrite                          // overwrite the old key
	DuplicateError                            // fail on a duplicate
)

// ParseType selects the input format, mirroring C enum ucl_parse_type.
type ParseType int

const (
	ParseUCL     ParseType = iota // UCL (and JSON, which it is a superset of)
	ParseMsgPack                  // MessagePack
	ParseCSexp                    // canonical S-expressions
	ParseAuto                     // detect
)

// Object is a node in a UCL document, mirroring C ucl_object_t.
//
// Exactly one field of the value group is meaningful, selected by ObjType.
// The layout follows the C struct rather than a Go sum type on purpose: the C
// API mutates an object's type in place (a string becomes an int, a null
// becomes an object), which a set of interface implementations makes awkward.
//
// Objects sharing a key form an implicit array threaded through Next/Prev.
type Object struct {
	// Value union: exactly one is meaningful, selected by ObjType.
	iv        int64     // TypeInt; TypeBoolean as 0 or 1
	dv        float64   // TypeFloat, TypeTime
	sv        []byte    // TypeString; raw bytes, which may embed NUL
	av        []*Object // TypeArray; elements may be nil
	ov        *hash     // TypeObject; nil until the first insert
	ud        any       // TypeUserdata payload
	udDtor    func(any) // userdata destructor
	udEmitter func(any) string

	// key is the object's key, or nil. Compared by exact bytes.
	key []byte

	// Next and Prev thread the implicit array formed by duplicate keys.
	// The invariants match utlist.h's doubly-linked list: for a chain head,
	// Prev points at the tail; the tail's Next is nil; a lone element points
	// its Prev at itself.
	Next *Object
	Prev *Object

	// length carries per-type semantics and is tracked rather than derived.
	// Deriving it from len(av) or the hash size diverges from the C in
	// Compare and in the array-merge path, both of which read it directly.
	length uint32

	// flags holds ObjectFlag bits in the low 12 bits and the priority in the
	// high 4. Use Priority/SetPriority for the latter.
	flags ObjectFlag

	// ObjType selects which value field is live.
	ObjType Type

	// valueTrash caches the string rendering produced by ToStringForced.
	// The caching is observable: a re-render must return the cached bytes,
	// and the first render of a non-string value overwrites length.
	valueTrash    []byte
	hasValueTrash bool
}

// Type returns the object's type. Mirrors ucl_object_type; a nil object
// reports TypeNull, matching the C's NULL handling.
func (o *Object) Type() Type {
	if o == nil {
		return TypeNull
	}
	return o.ObjType
}

// Flags returns the object's flag bits with the priority nibble masked off.
func (o *Object) Flags() ObjectFlag {
	if o == nil {
		return 0
	}
	return o.flags & flagMask
}

// Priority returns the object's priority, 0 to 15.
//
// Mirrors ucl_object_get_priority: the priority lives in the top four bits of
// the flags word. A nil object has priority 0.
func (o *Object) Priority() uint {
	if o == nil {
		return 0
	}
	return uint(o.flags >> (16 - prioBits))
}

// SetPriority sets the object's priority, mirroring ucl_object_set_priority.
//
// The value is masked to 0..15 rather than rejected, and the low 12 flag bits
// are preserved. Setting the priority of a nil object is a no-op.
func (o *Object) SetPriority(priority uint) {
	if o == nil {
		return
	}
	p := ObjectFlag(priority&(1<<prioBits-1)) << (16 - prioBits)
	o.flags = p | (o.flags & flagMask)
}
