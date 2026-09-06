package goucl

import (
	"encoding"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// DecodeError reports a reflect-level failure decoding a Node into a Go
// value: a type mismatch, an unsupported target kind, or a strict-mode
// unknown key. It is distinct from *SyntaxError (see DESIGN.md §6.5), which
// covers a document that failed to parse in the first place -- this covers a
// document that parsed fine but does not fit the shape asked of it.
type DecodeError struct {
	// Path names where the failure occurred, in the same dotted spelling
	// Node.Get accepts ("server.host", "servers.0.port"), relative to the
	// value Decode was called on. Empty for a failure at the root.
	Path string

	// Message describes the failure.
	Message string

	// Err is the underlying cause, if any, reachable through errors.Unwrap.
	Err error
}

func (e *DecodeError) Error() string {
	if e.Path == "" {
		return "goucl: " + e.Message
	}
	return "goucl: " + e.Path + ": " + e.Message
}

func (e *DecodeError) Unwrap() error { return e.Err }

func newDecodeError(path, format string, args ...any) *DecodeError {
	return &DecodeError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// Unmarshal parses a UCL document and stores the result in the value pointed
// to by v.
//
// opts is the same Option Parse takes -- WithVariable and WithMacro apply to
// the parse, and DisallowUnknownFields to the decode that follows it.
//
// A parse failure returns the *SyntaxError Parse would have produced, so
// errors.As against it still succeeds. A failure decoding an otherwise valid
// document into v returns a *DecodeError instead.
func Unmarshal(data []byte, v any, opts ...Option) error {
	n, err := Parse(data, opts...)
	if err != nil {
		return err
	}
	_, err = decodeInto(n, v, newParseConfig(opts).strict)
	return err
}

// Decode stores the node's value in the value pointed to by v.
//
// opts is the same Option Parse and Unmarshal take, for symmetry, but a Node
// has already been parsed, so WithVariable and WithMacro have nothing left
// to act on and are silently ignored here; only DisallowUnknownFields has an
// effect.
func (n *Node) Decode(v any, opts ...Option) error {
	_, err := decodeInto(n, v, newParseConfig(opts).strict)
	return err
}

// decodeInto decodes n into v. When strict, an unknown key is an error;
// otherwise unknown keys are collected and returned.
func decodeInto(n *Node, v any, strict bool) (undecoded []string, err error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, newDecodeError("", "Decode target must be a non-nil pointer, got %T", v)
	}
	d := &decoder{strict: strict}
	err = d.decode("", n, rv.Elem())
	sort.Strings(d.undecoded)
	return d.undecoded, err
}

// decoder carries the state threaded through one Decode call: the strict
// flag and the unknown keys collected along the way.
type decoder struct {
	strict    bool
	undecoded []string
}

// joinPath extends a dotted path with the next segment, matching the
// spelling Node.Get accepts.
func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

// decode stores n's value into rv, which must be settable.
//
// Nothing here may panic on malformed input or a hostile target: every
// reflect operation that could fail on an unsupported shape is guarded and
// turned into a *DecodeError instead.
func (d *decoder) decode(path string, n *Node, rv reflect.Value) error {
	if n == nil || n.Kind() == KindInvalid {
		// Nothing to store; leave the target as it is. Defensive only --
		// every caller here reaches decode with a node a Get/All/Values/
		// Elements call already found.
		return nil
	}

	// A UCL null is a no-op for anything but a pointer, interface, map or
	// slice, matching encoding/json: those are set to their zero value, and
	// everything else keeps whatever it already held.
	if n.Kind() == KindNull && !n.multi() {
		switch rv.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice:
			rv.Set(reflect.Zero(rv.Type()))
		}
		return nil
	}

	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			if !rv.CanSet() {
				return newDecodeError(path, "cannot allocate through a non-settable %s", rv.Type())
			}
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Interface && rv.NumMethod() == 0 {
		val, err := d.decodeAny(path, n)
		if err != nil {
			return err
		}
		rv.Set(reflect.ValueOf(val))
		return nil
	}

	if !rv.CanSet() {
		return newDecodeError(path, "cannot decode into unsettable %s", rv.Type())
	}

	// encoding.TextUnmarshaler is honoured for string values only: it is
	// meant for a scalar like a UCL string holding a duration or an IP
	// address, not for the container kinds.
	if n.Kind() == KindString && !n.multi() {
		if tu, ok := textUnmarshaler(rv); ok {
			s, _ := n.String("")
			if err := tu.UnmarshalText([]byte(s)); err != nil {
				return &DecodeError{Path: path, Message: "UnmarshalText failed", Err: err}
			}
			return nil
		}
	}

	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return d.decodeSequence(path, n, rv)
	}

	// Past this point the target is a single Go value, so a key holding more
	// than one value cannot be decoded into it: taking the first would be
	// indistinguishable from a correct read of a single-valued key, which is
	// exactly the wrong answer the typed accessors on Node were designed to
	// refuse. See the "Multi-valued keys" comment in node.go.
	if n.multi() {
		return newDecodeError(path, "key holds %d values, but target is %s, not a slice or array", n.chainLen(), rv.Kind())
	}

	switch rv.Kind() {
	case reflect.Struct:
		return d.decodeStruct(path, n, rv)
	case reflect.Map:
		return d.decodeMap(path, n, rv)
	case reflect.Bool:
		v, ok := n.Bool("")
		if !ok {
			return newDecodeError(path, "cannot decode %s into bool", n.Kind())
		}
		rv.SetBool(v)
		return nil
	case reflect.String:
		v, ok := n.String("")
		if !ok {
			return newDecodeError(path, "cannot decode %s into string", n.Kind())
		}
		rv.SetString(v)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if rv.Type() == durationType {
			dur, ok := n.Duration("")
			if !ok {
				return newDecodeError(path, "cannot decode %s into time.Duration", n.Kind())
			}
			rv.SetInt(int64(dur))
			return nil
		}
		v, ok := n.Int("")
		if !ok {
			return newDecodeError(path, "cannot decode %s into %s", n.Kind(), rv.Kind())
		}
		if rv.OverflowInt(v) {
			return newDecodeError(path, "%d overflows %s", v, rv.Kind())
		}
		rv.SetInt(v)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v, ok := n.Int("")
		if !ok {
			return newDecodeError(path, "cannot decode %s into %s", n.Kind(), rv.Kind())
		}
		if v < 0 {
			return newDecodeError(path, "%d is negative, cannot decode into %s", v, rv.Kind())
		}
		if rv.OverflowUint(uint64(v)) {
			return newDecodeError(path, "%d overflows %s", v, rv.Kind())
		}
		rv.SetUint(uint64(v))
		return nil
	case reflect.Float32, reflect.Float64:
		v, ok := n.Float("")
		if !ok {
			return newDecodeError(path, "cannot decode %s into %s", n.Kind(), rv.Kind())
		}
		rv.SetFloat(v)
		return nil
	default:
		return newDecodeError(path, "unsupported target kind %s", rv.Kind())
	}
}

// textUnmarshaler reports whether rv, or a pointer to it, implements
// encoding.TextUnmarshaler.
func textUnmarshaler(rv reflect.Value) (encoding.TextUnmarshaler, bool) {
	if rv.CanAddr() {
		if pv := rv.Addr(); pv.Type().Implements(textUnmarshalerType) {
			return pv.Interface().(encoding.TextUnmarshaler), true
		}
	}
	if rv.Type().Implements(textUnmarshalerType) {
		if tu, ok := rv.Interface().(encoding.TextUnmarshaler); ok {
			return tu, true
		}
	}
	return nil, false
}

// decodeSequence fills a slice or array target.
//
// The two ways a UCL document holds more than one value stay separate here,
// matching Values and Elements on Node: a repeated key collects its whole
// chain, and a single key whose value is an array expands that array. A
// single scalar value has nothing to expand, so it is an error rather than a
// silently-wrapped one-element slice.
func (d *decoder) decodeSequence(path string, n *Node, rv reflect.Value) error {
	var items []*Node
	switch {
	case n.multi():
		for v := range n.Values() {
			items = append(items, v)
		}
	case n.Kind() == KindArray:
		for e := range n.Elements() {
			items = append(items, e)
		}
	default:
		return newDecodeError(path, "cannot decode %s into %s", n.Kind(), rv.Kind())
	}

	switch rv.Kind() {
	case reflect.Slice:
		out := reflect.MakeSlice(rv.Type(), len(items), len(items))
		for i, item := range items {
			if err := d.decode(fmt.Sprintf("%s.%d", path, i), item, out.Index(i)); err != nil {
				return err
			}
		}
		rv.Set(out)
	case reflect.Array:
		limit := rv.Len()
		if len(items) < limit {
			limit = len(items)
		}
		for i := 0; i < limit; i++ {
			if err := d.decode(fmt.Sprintf("%s.%d", path, i), items[i], rv.Index(i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeStruct fills the exported fields of a struct target from an object
// node's keys.
func (d *decoder) decodeStruct(path string, n *Node, rv reflect.Value) error {
	if n.Kind() != KindObject {
		return newDecodeError(path, "cannot decode %s into struct", n.Kind())
	}

	fields := collectFields(rv.Type())
	exact := make(map[string]int, len(fields))
	lower := make(map[string]int, len(fields))
	for i, f := range fields {
		exact[f.name] = i
		lc := strings.ToLower(f.name)
		if _, ok := lower[lc]; !ok {
			lower[lc] = i
		}
	}

	type kv struct {
		key  string
		node *Node
	}
	var keys []kv
	// A field claimed by an exact key match is not also filled by a
	// case-insensitive one, regardless of which key comes first in the
	// document -- otherwise which value wins would depend on write order
	// rather than on which match is the better one.
	exactClaimed := make(map[int]bool, len(fields))
	for key, kn := range n.All() {
		keys = append(keys, kv{key, kn})
		if idx, ok := exact[key]; ok {
			exactClaimed[idx] = true
		}
	}

	for _, e := range keys {
		idx, ok := exact[e.key]
		if !ok {
			idx, ok = lower[strings.ToLower(e.key)]
			if ok && exactClaimed[idx] {
				ok = false
			}
		}
		if !ok {
			upath := joinPath(path, e.key)
			if d.strict {
				return newDecodeError(upath, "unknown field")
			}
			d.undecoded = append(d.undecoded, upath)
			continue
		}
		fv := rv.FieldByIndex(fields[idx].index)
		if err := d.decode(joinPath(path, e.key), e.node, fv); err != nil {
			return err
		}
	}
	return nil
}

// fieldSpec is one decodable struct field: its name, resolved from the ucl
// tag, the json tag, or the field name itself, and its index for
// reflect.Value.FieldByIndex.
type fieldSpec struct {
	name  string
	index []int
}

// collectFields lists a struct type's decodable fields, in declaration
// order. Unexported fields are skipped, never panicked on.
func collectFields(t reflect.Type) []fieldSpec {
	var out []fieldSpec
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, skip := fieldName(f)
		if skip {
			continue
		}
		out = append(out, fieldSpec{name: name, index: f.Index})
	}
	return out
}

// fieldName resolves a struct field's decoded name: the ucl tag first, then
// the json tag, then the Go field name. A tag of "-" with nothing else skips
// the field. Any ",omitempty" or similar suffix is ignored -- it only
// affects encoding, not decoding.
func fieldName(f reflect.StructField) (name string, skip bool) {
	tag, ok := f.Tag.Lookup("ucl")
	if !ok {
		tag, ok = f.Tag.Lookup("json")
	}
	if ok {
		if tag == "-" {
			return "", true
		}
		head, _, _ := strings.Cut(tag, ",")
		if head != "" {
			return head, false
		}
	}
	return f.Name, false
}

// decodeMap fills a map target from an object node's keys. Only a string key
// type is supported, which covers the ordinary map[string]T configuration
// shape; anything else is a *DecodeError rather than a panic.
func (d *decoder) decodeMap(path string, n *Node, rv reflect.Value) error {
	if n.Kind() != KindObject {
		return newDecodeError(path, "cannot decode %s into map", n.Kind())
	}
	t := rv.Type()
	if t.Key().Kind() != reflect.String {
		return newDecodeError(path, "unsupported map key type %s", t.Key())
	}
	if rv.IsNil() {
		rv.Set(reflect.MakeMap(t))
	}
	for key, kn := range n.All() {
		elemVal := reflect.New(t.Elem()).Elem()
		if err := d.decode(joinPath(path, key), kn, elemVal); err != nil {
			return err
		}
		rv.SetMapIndex(reflect.ValueOf(key).Convert(t.Key()), elemVal)
	}
	return nil
}

// decodeAny decodes n without a target type, producing the plain Go values
// documented for Decoding into any: map[string]any, []any, int64, float64,
// string, bool, nil, and time.Duration for a time. A repeated key becomes a
// []any, one entry per value in the chain.
func (d *decoder) decodeAny(path string, n *Node) (any, error) {
	if n.multi() {
		var out []any
		i := 0
		for v := range n.Values() {
			val, err := d.decodeAny(fmt.Sprintf("%s.%d", path, i), v)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
			i++
		}
		return out, nil
	}

	switch n.Kind() {
	case KindObject:
		m := make(map[string]any)
		for key, kn := range n.All() {
			val, err := d.decodeAny(joinPath(path, key), kn)
			if err != nil {
				return nil, err
			}
			m[key] = val
		}
		return m, nil
	case KindArray:
		var out []any
		i := 0
		for e := range n.Elements() {
			val, err := d.decodeAny(fmt.Sprintf("%s.%d", path, i), e)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
			i++
		}
		return out, nil
	case KindInt:
		v, _ := n.Int("")
		return v, nil
	case KindFloat:
		v, _ := n.Float("")
		return v, nil
	case KindString:
		v, _ := n.String("")
		return v, nil
	case KindBool:
		v, _ := n.Bool("")
		return v, nil
	case KindTime:
		v, _ := n.Duration("")
		return v, nil
	case KindNull:
		return nil, nil
	default:
		return nil, newDecodeError(path, "cannot decode %s into interface{}", n.Kind())
	}
}
