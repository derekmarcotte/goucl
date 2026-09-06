package goucl

import (
	"fmt"
	"reflect"
	"sort"
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

// typeFields enumerates t's encodable/decodable fields, promoting the
// exported fields of anonymous struct fields into the outer struct. This
// reproduces encoding/json's typeFields algorithm (see its doc comment in
// encoding/json/encode.go), adapted to this package's own tag resolution
// (the ucl tag first, the json tag as a fallback) since that part is not
// encoding/json's rule to apply. The promotion rules being followed:
//
//   - An anonymous struct field with no tag has its exported fields
//     promoted into the outer struct.
//   - An anonymous field with an explicit tag name is an ordinary named
//     field, not promoted.
//   - An anonymous field of non-struct type is an ordinary named field,
//     using its type name.
//   - An anonymous pointer to struct is promoted the same as an anonymous
//     struct.
//   - Exported fields of an unexported embedded struct type are still
//     promoted; an unexported embedded field of non-struct type is ignored.
//   - Conflicts: a field at shallower depth wins over one at deeper depth.
//     At equal depth, if exactly one of the conflicting fields is tagged,
//     it wins; otherwise all of them are dropped (neither encoded nor
//     decoded).
//
// The breadth-first walk (current/next, keyed by type) is what makes a
// cyclic embedding graph terminate: a type already visited at an earlier
// depth is never scanned again, so a type that embeds itself (directly or
// through another type) runs out of new types to queue rather than
// recursing forever.
func typeFields(t reflect.Type) []field {
	// current and next hold, for one breadth-first depth, the anonymous
	// struct types still to scan; queueField's index is the path from the
	// root to that embedded struct.
	type queueField struct {
		typ   reflect.Type
		index []int
	}
	current := []queueField{}
	next := []queueField{{typ: t}}

	// count/nextCount track how many distinct embedding paths reach a given
	// type at one depth. More than one path reaching the same type is what
	// makes that type's fields conflict with each other, so a second
	// occurrence of the field is appended for the conflict pass below to see
	// -- matching encoding/json, only the distinction between 1 and 2
	// occurrences matters, so no more than one duplicate is ever needed.
	var count, nextCount map[reflect.Type]int

	visited := map[reflect.Type]bool{}
	var fields []field

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, qf := range current {
			if visited[qf.typ] {
				continue
			}
			visited[qf.typ] = true

			for i := 0; i < qf.typ.NumField(); i++ {
				sf := qf.typ.Field(i)
				ft := sf.Type

				if sf.Anonymous {
					if ft.Kind() == reflect.Ptr {
						ft = ft.Elem()
					}
					if !sf.IsExported() && ft.Kind() != reflect.Struct {
						// An embedded field of unexported non-struct type
						// has no fields of its own to promote.
						continue
					}
					// An embedded unexported struct type is scanned anyway:
					// its exported fields are still promoted.
				} else if !sf.IsExported() {
					continue
				}

				name, omitempty, tagged, skip := parseFieldTag(sf)
				if skip {
					continue
				}

				index := make([]int, len(qf.index)+1)
				copy(index, qf.index)
				index[len(qf.index)] = i

				if name == "" && sf.Anonymous && ft.Kind() == reflect.Struct {
					// An untagged anonymous struct (or pointer to one) is
					// not itself a field: queue it so its own fields are
					// promoted at the next depth.
					nextCount[ft]++
					if nextCount[ft] == 1 {
						next = append(next, queueField{typ: ft, index: index})
					}
					continue
				}

				if name == "" {
					name = sf.Name
				}
				fields = append(fields, field{
					name:      name,
					tag:       tagged,
					index:     index,
					omitempty: omitempty,
				})
				if count[qf.typ] > 1 {
					// Duplicate the entry so the conflict pass below sees
					// more than one field at this depth and drops them,
					// matching encoding/json's handling of the same struct
					// type reachable by more than one embedding path at one
					// level.
					fields = append(fields, fields[len(fields)-1])
				}
			}
		}
	}

	return resolveFieldConflicts(fields)
}

// resolveFieldConflicts sorts fields by name and depth and picks the single
// dominant field for each name, exactly as encoding/json's typeFields does
// after its breadth-first scan: shallower beats deeper, and at equal depth
// an explicitly tagged field beats an untagged one -- with all fields
// dropped when that still leaves more than one candidate.
func resolveFieldConflicts(fields []field) []field {
	sort.Slice(fields, func(i, j int) bool {
		a, b := fields[i], fields[j]
		if a.name != b.name {
			return a.name < b.name
		}
		if len(a.index) != len(b.index) {
			return len(a.index) < len(b.index)
		}
		if a.tag != b.tag {
			return a.tag // tagged sorts first
		}
		return lessIndex(a.index, b.index)
	})

	out := fields[:0]
	for i := 0; i < len(fields); {
		j := i + 1
		for j < len(fields) && fields[j].name == fields[i].name {
			j++
		}
		if j-i == 1 {
			out = append(out, fields[i])
		} else if dominant, ok := dominantField(fields[i:j]); ok {
			out = append(out, dominant)
		}
		i = j
	}

	fields = out
	sort.Slice(fields, func(i, j int) bool { return lessIndex(fields[i].index, fields[j].index) })
	return fields
}

// dominantField looks through fields, all sharing one name, for the single
// field that wins under Go's embedding rules as modified by an explicit tag.
// The fields arrive sorted by increasing index length and then by tag, so
// the first is the candidate; the only thing left to check is the error
// case encoding/json also rejects outright: two fields at the same depth,
// neither breaking the tie by being the only tagged one.
func dominantField(fields []field) (field, bool) {
	if len(fields) > 1 && len(fields[0].index) == len(fields[1].index) && fields[0].tag == fields[1].tag {
		return field{}, false
	}
	return fields[0], true
}

// lessIndex orders two field index chains lexicographically, which is also
// declaration order: a field's own position sorts before any field promoted
// from something embedded after it, and a promoted field sorts at the
// position of the embed it came from.
func lessIndex(a, b []int) bool {
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k] != b[k] {
			return a[k] < b[k]
		}
	}
	return len(a) < len(b)
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
// Unlike the decode-side walk, it never allocates: an embedded pointer that
// is nil along the way means there is nothing to read, so the field is
// reported absent rather than materialized just to encode a zero value.
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
// embedded pointer it passes through. It is only ever called once a
// document key naming a field reached through that pointer has actually
// been seen, so a promoted field with no key present in the document never
// causes the pointer to be allocated.
//
// An embedded pointer to an unexported struct type cannot be allocated this
// way -- the field holding the pointer is itself unexported, so reflect
// refuses to Set it even though the struct it points to has exported fields
// of its own -- and that is reported as an error rather than panicking,
// matching encoding/json (https://golang.org/issue/21357).
func fieldByIndexAlloc(rv reflect.Value, index []int) (reflect.Value, error) {
	for _, x := range index {
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				if !rv.CanSet() {
					return reflect.Value{}, fmt.Errorf("cannot allocate embedded pointer to unexported struct type %s", rv.Type().Elem())
				}
				rv.Set(reflect.New(rv.Type().Elem()))
			}
			rv = rv.Elem()
		}
		rv = rv.Field(x)
	}
	return rv, nil
}
