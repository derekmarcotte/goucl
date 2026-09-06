package goucl

import (
	"encoding"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derekmarcotte/goucl/internal/core"
)

// Marshal returns the UCL encoding of v, in UCL's own configuration syntax.
func Marshal(v any) ([]byte, error) {
	n, err := toNode(v)
	if err != nil {
		return nil, err
	}
	return n.Bytes(FormatConfig)
}

// toNode builds a Node from a Go value.
//
// The returned Node carries no comment table: a tree built from a Go value
// has no comments to attach, unlike one produced by Parse.
func toNode(v any) (*Node, error) {
	obj, err := new(marshaler).build(reflect.ValueOf(v), "")
	if err != nil {
		return nil, err
	}
	return &Node{obj: obj}, nil
}

// UnsupportedTypeError is returned when Marshal is asked to encode a Go type
// with no UCL representation: a channel, a function, a complex number, or an
// unsafe pointer.
type UnsupportedTypeError struct {
	Type reflect.Type
	Path string
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("goucl: unsupported type %s at %s", e.Type, e.Path)
}

// CycleError is returned when Marshal finds a value reachable from itself.
// Walking such a structure would never terminate, so Marshal reports it as an
// error rather than hanging.
type CycleError struct {
	Path string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("goucl: cycle at %s", e.Path)
}

// durationType and textMarshalerType are compared against by reflect.Type
// equality/Implements, rather than by name, so they are computed once.
var (
	durationType      = reflect.TypeOf(time.Duration(0))
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// visitKey identifies one reference value -- a pointer, map or slice -- for
// cycle detection.
type visitKey struct {
	ptr uintptr
	typ reflect.Type
}

// marshaler carries the state for one Marshal call: the set of reference
// values currently being walked, so that a value reachable from itself is
// reported rather than recursed into forever.
type marshaler struct {
	seen map[visitKey]bool
}

// push records v as being visited. It applies only to the three kinds that
// can genuinely cycle back on themselves through Go's aliasing -- a pointer,
// a map, or a slice -- and it is a per-branch check, not a whole-tree one:
// pop removes the entry once that branch's recursion returns, so a value
// reached twice by non-cyclic sharing (the same slice used in two fields, say)
// is marshaled twice rather than rejected.
func (m *marshaler) push(v reflect.Value) (visitKey, bool) {
	key := visitKey{v.Pointer(), v.Type()}
	if m.seen == nil {
		m.seen = make(map[visitKey]bool)
	}
	if m.seen[key] {
		return key, false
	}
	m.seen[key] = true
	return key, true
}

// pop ends a value's visit, started by push.
func (m *marshaler) pop(key visitKey) {
	delete(m.seen, key)
}

// pathOrRoot renders path for an error message, naming the root value when
// path is empty.
func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

// pathField appends a struct field or map key to path.
func pathField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// pathIndex appends an array or slice index to path.
func pathIndex(path string, i int) string {
	return path + "[" + strconv.Itoa(i) + "]"
}

// build converts one Go value to an engine object.
//
// Pointers and interfaces are unwound first, so that the type-specific checks
// below -- time.Duration, encoding.TextMarshaler -- see the concrete value
// they need to look at rather than a wrapper around it.
func (m *marshaler) build(v reflect.Value, path string) (*core.Object, error) {
	if !v.IsValid() {
		return core.TypedNew(core.TypeNull), nil
	}

	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return core.TypedNew(core.TypeNull), nil
		}
		return m.build(v.Elem(), path)

	case reflect.Ptr:
		if v.IsNil() {
			return core.TypedNew(core.TypeNull), nil
		}
		key, ok := m.push(v)
		if !ok {
			return nil, &CycleError{Path: pathOrRoot(path)}
		}
		defer m.pop(key)
		return m.build(v.Elem(), path)
	}

	// time.Duration must encode as a UCL time node, which is the engine's
	// only representation for a number that means seconds. The engine has no
	// exported constructor for one -- setTime is a private setter used only
	// by the parser's own number scanner -- so a duration is rendered as the
	// literal text that scanner already knows how to turn into TypeTime
	// ("<seconds>s") and handed through FromStringCommon with the same parse
	// flags the parser itself uses for a suffixed number. This reuses the
	// public API rather than reaching into the engine, and it is exactly the
	// code path "12.5s" in a document would take.
	if v.Type() == durationType {
		return durationObject(time.Duration(v.Int())), nil
	}

	if tm, ok := textMarshaler(v); ok {
		text, err := tm.MarshalText()
		if err != nil {
			return nil, fmt.Errorf("goucl: marshaling %s at %s: %w", v.Type(), pathOrRoot(path), err)
		}
		return stringObject(text), nil
	}

	switch v.Kind() {
	case reflect.Bool:
		return core.FromBool(v.Bool()), nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return core.FromInt(v.Int()), nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return core.FromInt(int64(v.Uint())), nil

	case reflect.Float32, reflect.Float64:
		return core.FromDouble(v.Float()), nil

	case reflect.String:
		return stringObject([]byte(v.String())), nil

	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// []byte holds raw bytes, and so does a UCL string -- see
			// core.FromLString / core.StringRaw. Base64 is a JSON
			// convention, not this format's, so the bytes go in untouched.
			var b []byte
			if !v.IsNil() {
				b = v.Bytes()
			}
			return stringObject(b), nil
		}
		if v.IsNil() {
			// A nil slice and an empty slice both encode as an empty array:
			// neither carries information a UCL document has a way to hold.
			return core.TypedNew(core.TypeArray), nil
		}
		key, ok := m.push(v)
		if !ok {
			return nil, &CycleError{Path: pathOrRoot(path)}
		}
		defer m.pop(key)
		return m.buildArray(v, path)

	case reflect.Array:
		return m.buildArray(v, path)

	case reflect.Map:
		if v.IsNil() {
			return core.TypedNew(core.TypeObject), nil
		}
		key, ok := m.push(v)
		if !ok {
			return nil, &CycleError{Path: pathOrRoot(path)}
		}
		defer m.pop(key)
		return m.buildMap(v, path)

	case reflect.Struct:
		return m.buildStruct(v, path)

	default:
		// Chan, Func, Complex64, Complex128, UnsafePointer: nothing in UCL
		// can hold one.
		return nil, &UnsupportedTypeError{Type: v.Type(), Path: pathOrRoot(path)}
	}
}

// durationObject renders d as a UCL time node. See the comment in build for
// why it goes through the string parser rather than a direct constructor.
func durationObject(d time.Duration) *core.Object {
	lit := strconv.FormatFloat(d.Seconds(), 'g', -1, 64) + "s"
	return core.FromStringCommon([]byte(lit), core.StringParse)
}

// stringObject builds a UCL string node holding b's raw bytes.
//
// core.FromLString treats a nil slice as "no input" and returns nil (a
// faithful divergence from the C's "NULL means call strlen", recorded in its
// own doc comment), so a nil b is turned into a non-nil empty slice first --
// a Go nil byte slice and an empty one both mean the same empty UCL string.
func stringObject(b []byte) *core.Object {
	if b == nil {
		b = []byte{}
	}
	return core.FromLString(b)
}

// buildArray converts a slice or array's elements into a UCL array.
func (m *marshaler) buildArray(v reflect.Value, path string) (*core.Object, error) {
	obj := core.TypedNew(core.TypeArray)
	for i := 0; i < v.Len(); i++ {
		child, err := m.build(v.Index(i), pathIndex(path, i))
		if err != nil {
			return nil, err
		}
		core.ArrayAppend(obj, child)
	}
	return obj, nil
}

// buildMap converts a map into a UCL object, sorting its keys first so the
// output is deterministic. Go's map iteration order is randomised per run,
// and this project has already been bitten once by a test that depended on
// it, so this is not an optional nicety.
func (m *marshaler) buildMap(v reflect.Value, path string) (*core.Object, error) {
	type entry struct {
		name string
		val  reflect.Value
	}

	keys := v.MapKeys()
	entries := make([]entry, 0, len(keys))
	for _, k := range keys {
		name, err := mapKeyString(k)
		if err != nil {
			return nil, fmt.Errorf("goucl: %w at %s", err, pathOrRoot(path))
		}
		entries = append(entries, entry{name, v.MapIndex(k)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	obj := core.TypedNew(core.TypeObject)
	for _, e := range entries {
		child, err := m.build(e.val, pathField(path, e.name))
		if err != nil {
			return nil, err
		}
		obj.InsertKey(child, []byte(e.name), false)
	}
	return obj, nil
}

// mapKeyString renders a map key as the string a UCL key must be. A string,
// an integer, or a type implementing encoding.TextMarshaler all render; any
// other key kind is rejected.
func mapKeyString(k reflect.Value) (string, error) {
	if tm, ok := textMarshaler(k); ok {
		b, err := tm.MarshalText()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	switch k.Kind() {
	case reflect.String:
		return k.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10), nil
	}
	return "", fmt.Errorf("unsupported map key type %s", k.Type())
}

// buildStruct converts a struct's fields into a UCL object, in field order.
func (m *marshaler) buildStruct(v reflect.Value, path string) (*core.Object, error) {
	obj := core.TypedNew(core.TypeObject)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		name, omitempty, skip := fieldTag(f)
		if skip {
			continue
		}
		fv := v.Field(i)
		if omitempty && isEmptyValue(fv) {
			continue
		}
		child, err := m.build(fv, pathField(path, name))
		if err != nil {
			return nil, err
		}
		obj.InsertKey(child, []byte(name), false)
	}
	return obj, nil
}

// fieldTag reads a struct field's ucl tag, falling back to its json tag when
// there is no ucl one. compat/libucl's libucl tag is a different, frozen API
// and is not consulted here.
func fieldTag(f reflect.StructField) (name string, omitempty, skip bool) {
	tag, ok := f.Tag.Lookup("ucl")
	if !ok {
		tag, ok = f.Tag.Lookup("json")
	}
	if !ok {
		return f.Name, false, false
	}
	if tag == "-" {
		return "", false, true
	}
	name, rest, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	for _, opt := range strings.Split(rest, ",") {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

// isEmptyValue reports whether v is the zero value omitempty skips, matching
// encoding/json's meaning of "empty".
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}

// textMarshaler reports whether v (or, if v is addressable, a pointer to it)
// implements encoding.TextMarshaler, and returns that implementation.
func textMarshaler(v reflect.Value) (encoding.TextMarshaler, bool) {
	if v.Type().Implements(textMarshalerType) {
		if v.Kind() == reflect.Ptr && v.IsNil() {
			return nil, false
		}
		tm, _ := v.Interface().(encoding.TextMarshaler)
		return tm, true
	}
	if v.CanAddr() {
		pv := v.Addr()
		if pv.Type().Implements(textMarshalerType) {
			tm, _ := pv.Interface().(encoding.TextMarshaler)
			return tm, true
		}
	}
	return nil, false
}
