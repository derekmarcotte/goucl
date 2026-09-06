package goucl

import (
	"reflect"
	"strings"
	"sync"
)

// field describes one struct field the way both directions need to see it:
// its resolved UCL name, its index chain for reflect.Value.FieldByIndex, and
// the marshal-only omitempty option.
//
// This is the one place either direction learns "which field does this key
// name". Marshal and Unmarshal each used to compute this independently; two
// copies of a computation this intricate will drift into disagreeing about
// which field a key names, which is silent data corruption rather than a
// compile error, so there is exactly one implementation here and both files
// call it.
type field struct {
	name      string // resolved name: tag name, or the Go field name
	tag       bool   // name came from an explicit ucl/json tag
	index     []int  // field index chain, for reflect.Value.FieldByIndex
	omitempty bool
}

// fieldCache memoizes typeFields per struct type, the same sync.Map-keyed-
// by-reflect.Type shape encoding/json uses: field enumeration is worth
// caching rather than repeating per call, since struct types are reused
// across many values.
var fieldCache sync.Map // map[reflect.Type][]field

// cachedTypeFields returns t's fields, computing and caching them on first
// use. t must be a struct type.
func cachedTypeFields(t reflect.Type) []field {
	if f, ok := fieldCache.Load(t); ok {
		return f.([]field)
	}
	f, _ := fieldCache.LoadOrStore(t, typeFields(t))
	return f.([]field)
}

// typeFields enumerates t's encodable/decodable fields, in declaration
// order. Unexported fields are skipped, never panicked on.
func typeFields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, omitempty, tagged, skip := parseFieldTag(sf)
		if skip {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		out = append(out, field{
			name:      name,
			tag:       tagged,
			index:     []int{i},
			omitempty: omitempty,
		})
	}
	return out
}

// parseFieldTag reads a struct field's ucl tag, falling back to its json tag
// when there is no ucl one, and reports whether the field should be skipped
// entirely. compat/libucl's libucl tag is a different, frozen API and is not
// consulted here.
func parseFieldTag(f reflect.StructField) (name string, omitempty, tagged, skip bool) {
	tag, ok := f.Tag.Lookup("ucl")
	if !ok {
		tag, ok = f.Tag.Lookup("json")
	}
	if !ok {
		return "", false, false, false
	}
	if tag == "-" {
		return "", false, false, true
	}
	name, rest, _ := strings.Cut(tag, ",")
	for _, opt := range strings.Split(rest, ",") {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, name != "", false
}

// fieldByIndexRead reads the field named by index out of rv for encoding.
func fieldByIndexRead(rv reflect.Value, index []int) (reflect.Value, bool) {
	for _, x := range index {
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return reflect.Value{}, false
			}
			rv = rv.Elem()
		}
		rv = rv.Field(x)
	}
	return rv, true
}

// fieldByIndexAlloc walks index through rv for decoding, allocating any nil
// pointer it passes through along the way.
func fieldByIndexAlloc(rv reflect.Value, index []int) reflect.Value {
	for _, x := range index {
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				rv.Set(reflect.New(rv.Type().Elem()))
			}
			rv = rv.Elem()
		}
		rv = rv.Field(x)
	}
	return rv
}
