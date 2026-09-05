package goucl

import (
	"bytes"
	"sort"
	"strconv"
)

// Value conversion, comparison and sorting. Mirrors the ucl_object_to* family,
// ucl_object_compare, ucl_object_array_sort and ucl_object_sort_keys; see
// docs/reference/object-model.md section 6.
//
// The conversions are deliberately narrow, exactly as in the C: there is no
// implicit string-to-number or number-to-string conversion anywhere in this
// family. The only string parsing entry point is FromStringCommon.

// --- The value trash (section 2.8) ----------------------------------------

// copyValueTrash mirrors ucl_copy_value_trash: it returns the object's cached
// string rendering, creating it on first use.
//
// Two effects of the C are observable and are reproduced:
//
//   - Rendering a non-string *overwrites* length with the length of the
//     rendering, destroying whatever the field held.
//   - The rendering is cached. An object that was built by FromStringCommon
//     and then reclassified as a number already has a trash, so it keeps
//     returning the original text rather than a rendering of its new value.
func copyValueTrash(o *Object) []byte {
	if o == nil {
		return nil
	}
	if o.hasValueTrash {
		return o.valueTrash
	}

	if o.ObjType == TypeString {
		// The C copies exactly len bytes out of value.sv. length can exceed
		// the bytes actually owned after a copy of a binary string, where the
		// C reads past the allocation; clamp instead.
		n := int(o.length)
		if n > len(o.sv) {
			n = len(o.sv)
		}
		o.valueTrash = append([]byte(nil), o.sv[:n]...)
		o.sv = o.valueTrash
	} else {
		o.valueTrash = emitSingleJSONValue(o)
		o.length = uint32(len(o.valueTrash))
	}

	o.hasValueTrash = true
	o.flags |= FlagAllocatedValue
	return o.valueTrash
}

// emitSingleJSONValue mirrors ucl_object_emit_single_json, the renderer the
// value trash uses for non-strings.
func emitSingleJSONValue(o *Object) []byte {
	if o == nil {
		return nil
	}
	switch o.ObjType {
	case TypeObject:
		return []byte("object")
	case TypeArray:
		return []byte("array")
	case TypeInt:
		return []byte(strconv.FormatInt(o.iv, 10))
	case TypeFloat, TypeTime:
		return []byte(emitDouble(o.dv))
	case TypeNull:
		return []byte("null")
	case TypeBoolean:
		if o.iv != 0 {
			return []byte("true")
		}
		return []byte("false")
	case TypeString:
		n := int(o.length)
		if n > len(o.sv) {
			n = len(o.sv)
		}
		return append([]byte(nil), o.sv[:n]...)
	case TypeUserdata:
		return []byte("userdata")
	}
	return nil
}

// --- Conversions (section 6.1) --------------------------------------------

// ToDoubleSafe converts o to a float64, mirroring ucl_object_todouble_safe.
//
// Only TypeInt, TypeFloat and TypeTime convert; booleans and strings do not.
// An integer above 2^53 loses precision, exactly as in the C.
func ToDoubleSafe(o *Object) (float64, bool) {
	if o == nil {
		return 0, false
	}
	switch o.ObjType {
	case TypeInt:
		return float64(o.iv), true
	case TypeFloat, TypeTime:
		return o.dv, true
	}
	return 0, false
}

// ToDouble converts o to a float64, mirroring ucl_object_todouble. A rejected
// conversion yields 0, which is indistinguishable from a real zero.
func ToDouble(o *Object) float64 {
	v, _ := ToDoubleSafe(o)
	return v
}

// ToIntSafe converts o to an int64, mirroring ucl_object_toint_safe.
//
// TypeFloat and TypeTime truncate toward zero. Only TypeInt, TypeFloat and
// TypeTime convert; a boolean does not.
func ToIntSafe(o *Object) (int64, bool) {
	if o == nil {
		return 0, false
	}
	switch o.ObjType {
	case TypeInt:
		return o.iv, true
	case TypeFloat, TypeTime:
		return int64(o.dv), true
	}
	return 0, false
}

// ToInt converts o to an int64, mirroring ucl_object_toint. A rejected
// conversion yields 0.
func ToInt(o *Object) int64 {
	v, _ := ToIntSafe(o)
	return v
}

// ToBooleanSafe converts o to a bool, mirroring ucl_object_toboolean_safe.
//
// Only TypeBoolean converts. TypeInt 0/1 and the strings "true"/"false" do
// not: turning text into a boolean is FromStringCommon's job.
func ToBooleanSafe(o *Object) (bool, bool) {
	if o == nil {
		return false, false
	}
	if o.ObjType == TypeBoolean {
		return o.iv == 1, true
	}
	return false, false
}

// ToBoolean converts o to a bool, mirroring ucl_object_toboolean. A rejected
// conversion yields false.
func ToBoolean(o *Object) bool {
	v, _ := ToBooleanSafe(o)
	return v
}

// ToStringSafe returns o's string value, mirroring ucl_object_tostring_safe.
//
// Only TypeString converts. Faithfulness wart: for a string flagged
// FlagBinary the C reports success but never writes the out-parameter, so the
// caller is left holding whatever it had -- reproduced here as "ok with an
// empty result".
func ToStringSafe(o *Object) (string, bool) {
	if o == nil {
		return "", false
	}
	if o.ObjType != TypeString {
		return "", false
	}
	if o.flags&FlagBinary != 0 {
		return "", true
	}
	return string(copyValueTrash(o)), true
}

// ToString returns o's string value, mirroring ucl_object_tostring. A rejected
// conversion yields "" (the C's NULL).
func ToString(o *Object) string {
	v, _ := ToStringSafe(o)
	return v
}

// ToStringForced renders o as a string whatever its type, mirroring
// ucl_object_tostring_forced.
//
// Non-strings get the JSON rendering from section 2.8, which also overwrites
// length. A binary string yields "" (the C's NULL). Note that the rendering is
// cached: an object FromStringCommon reclassified as a number returns the
// original *text*, not a rendering of the number.
//
// The C dereferences its argument without a NULL check; a nil object yields ""
// here.
func ToStringForced(o *Object) string {
	if o == nil {
		return ""
	}
	if o.flags&FlagBinary != 0 {
		return ""
	}
	return string(copyValueTrash(o))
}

// ToLStringSafe returns o's raw string bytes and its length, mirroring
// ucl_object_tolstring_safe.
//
// Unlike ToStringSafe this accepts binary strings and returns the bytes
// without forcing a trash copy.
//
// Deliberate divergence from the C. The tracked length can exceed the bytes
// actually present: Copy duplicates a value with the equivalent of strdup,
// which stops at the first NUL, while carrying the original length over
// unchanged. The C then reads past the end of the allocation and returns
// whatever follows it. Go cannot reproduce that without lying about the slice,
// and a caller writing the natural b[:n] would panic with an index out of
// range, so the reported length is clamped to the bytes that exist. See
// ToStringForced, which clamps the same way.
func ToLStringSafe(o *Object) ([]byte, uint32, bool) {
	if o == nil {
		return nil, 0, false
	}
	if o.ObjType != TypeString {
		return nil, 0, false
	}
	n := o.length
	if int(n) > len(o.sv) {
		n = uint32(len(o.sv))
	}
	return o.sv, n, true
}

// ToLString returns o's raw string bytes and length, mirroring
// ucl_object_tolstring. A rejected conversion yields nil and 0.
func ToLString(o *Object) ([]byte, uint32) {
	b, n, _ := ToLStringSafe(o)
	return b, n
}

// --- Comparison (section 6.2) ---------------------------------------------

// Compare orders two objects, mirroring ucl_object_compare. It returns a
// negative number, zero, or a positive number.
//
// Every one of the following is the C's behaviour, not an oversight:
//
//   - Objects of different types compare by type tag, in Type's numeric order.
//   - Numbers are compared as the *int truncation* of their difference as
//     doubles, so 1.0 and 1.4 compare equal, and so do two int64s less than
//     one apart.
//   - Strings of different lengths compare by length difference alone, never
//     by content, so "b" sorts before "aa". Equal-length strings go through
//     strcmp, which stops at an embedded NUL.
//   - Object comparison is asymmetric: it only checks that every key of o1 is
//     present in o2 with an equal value. Extra keys in o2 are invisible except
//     through the initial length check, and implicit-array siblings are never
//     compared.
//   - Arrays compare element by element only when their tracked lengths agree;
//     since ArrayMerge can make length disagree with the real element count,
//     the C can index out of bounds here. Out-of-range elements are treated as
//     nil instead.
//   - TypeNull and TypeUserdata always compare equal.
//
// The C dereferences both arguments without a NULL check; nil is treated as
// TypeNull here.
func Compare(o1, o2 *Object) int {
	if o1 == nil || o2 == nil {
		switch {
		case o1 == nil && o2 == nil:
			return 0
		case o1 == nil:
			return int(TypeNull) - int(o2.ObjType)
		default:
			return int(o1.ObjType) - int(TypeNull)
		}
	}

	if o1.ObjType != o2.ObjType {
		return int(o1.ObjType) - int(o2.ObjType)
	}

	ret := 0
	switch o1.ObjType {
	case TypeString:
		if o1.length == o2.length && o1.length > 0 {
			ret = cStrcmp(toStringBytes(o1), toStringBytes(o2))
		} else {
			ret = lenDiff(o1.length, o2.length)
		}

	case TypeFloat, TypeInt, TypeTime:
		// The C assigns a double difference to an int, truncating toward zero:
		// 1.0 and 1.4 compare equal, and so do two int64 values less than one
		// apart in double space. A difference beyond int range is undefined in
		// C, so it is clamped rather than left to Go's unspecified conversion.
		d := ToDouble(o1) - ToDouble(o2)
		switch {
		case d != d: // NaN
			ret = 0
		case d >= 2147483647:
			ret = 2147483647
		case d <= -2147483648:
			ret = -2147483648
		default:
			ret = int(int32(d))
		}

	case TypeBoolean:
		ret = boolToInt(ToBoolean(o1)) - boolToInt(ToBoolean(o2))

	case TypeArray:
		if o1.length == o2.length && o1.length > 0 {
			for i := range o1.av {
				it1 := o1.av[i]
				var it2 *Object
				if i < len(o2.av) {
					it2 = o2.av[i]
				}
				switch {
				case it1 == nil && it2 != nil:
					return -1
				case it2 == nil && it1 != nil:
					return 1
				case it1 != nil && it2 != nil:
					ret = Compare(it1, it2)
					if ret != 0 {
						return ret
					}
				}
			}
		} else {
			ret = lenDiff(o1.length, o2.length)
		}

	case TypeObject:
		if o1.length == o2.length && o1.length > 0 {
			var it Iter
			for it1 := Iterate(o1, &it, true); it1 != nil; it1 = Iterate(o1, &it, true) {
				// The C looks up by ucl_object_key, a NUL-terminated string,
				// so a key with an embedded NUL is truncated here.
				var it2 *Object
				if it1.key != nil {
					it2 = o2.LookupLen(cStr(it1.key))
				}
				if it2 == nil {
					ret = 1
					break
				}
				ret = Compare(it1, it2)
				if ret != 0 {
					break
				}
			}
		} else {
			ret = lenDiff(o1.length, o2.length)
		}

	default:
		ret = 0
	}

	return ret
}

// CompareQsort is the default comparator for ArraySort, mirroring
// ucl_object_compare_qsort. The C's extra pointer indirection is meaningless
// in Go, so this is Compare.
func CompareQsort(o1, o2 *Object) int { return Compare(o1, o2) }

// toStringBytes is ToString without the string conversion, for Compare.
func toStringBytes(o *Object) []byte {
	if o == nil || o.ObjType != TypeString {
		return nil
	}
	if o.flags&FlagBinary != 0 {
		// The C hands strcmp a NULL here and crashes; compare as empty.
		return nil
	}
	return copyValueTrash(o)
}

// lenDiff mirrors the C's `int ret = o1->len - o2->len`: unsigned subtraction
// reinterpreted as a signed 32-bit value.
func lenDiff(a, b uint32) int { return int(int32(a - b)) }

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// cStr truncates b at the first NUL, the way a C string ends.
func cStr(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// cStrcmp compares two byte slices as C strings: the comparison stops at the
// first NUL in either operand.
func cStrcmp(a, b []byte) int { return bytes.Compare(cStr(a), cStr(b)) }

// --- Sorting (section 3.4 and 6.2) ----------------------------------------

// ArraySort sorts an array in place with cmp, mirroring
// ucl_object_array_sort. Pass CompareQsort for the default ordering.
//
// Divergence: the C uses qsort, which is not stable, so ties may come out in a
// different order. This uses a stable sort, which is deterministic; a ported
// test that depends on the ordering of ties would have been depending on the
// C library's internals anyway.
func ArraySort(ar *Object, cmp func(o1, o2 *Object) int) {
	if cmp == nil || ar == nil || ar.ObjType != TypeArray {
		return
	}
	sort.SliceStable(ar.av, func(i, j int) bool {
		return cmp(ar.av[i], ar.av[j]) < 0
	})
}

// SortKeys reorders an object's keys, mirroring ucl_object_sort_keys.
//
// Only the emission/iteration order changes; lookups are unaffected. It is a
// no-op for anything that is not an object.
func SortKeys(o *Object, how SortFlag) {
	if o == nil || o.ObjType != TypeObject {
		return
	}
	hashSort(o.ov, how)
}

// hashSort mirrors ucl_hash_sort: a stable sort of the ordered list, then an
// optional recursion into object-typed values.
//
// The C's DL_SORT is a stable bottom-up mergesort, so sort.SliceStable matches
// it. Only objects are recursed into -- not arrays, and not implicit-array
// siblings.
func hashSort(h *hash, fl SortFlag) {
	if h == nil {
		return
	}

	cmp := cmpKeysCaseSens
	if fl&SortKeysICase != 0 {
		cmp = cmpKeysICase
	}
	sort.SliceStable(h.order, func(i, j int) bool {
		return cmp(h.order[i].obj, h.order[j].obj) < 0
	})
	for i, e := range h.order {
		e.idx = i
	}

	if fl&SortKeysRecursive != 0 {
		for _, e := range h.order {
			if e.obj.Type() == TypeObject {
				hashSort(e.obj.ov, fl)
			}
		}
	}
}

// cmpKeysCaseSens mirrors ucl_hash_cmp_case_sens.
//
// The ordering is length-major: shorter keys sort first, and only keys of
// equal length are compared byte by byte. This is not lexicographic order --
// "z" sorts before "aa".
func cmpKeysCaseSens(a, b *Object) int {
	if len(a.key) == len(b.key) {
		return bytes.Compare(a.key, b.key)
	}
	return lenDiff(uint32(len(a.key)), uint32(len(b.key)))
}

// cmpKeysICase mirrors ucl_hash_cmp_icase: the same length-major rule, then
// ucl_lc_cmp.
func cmpKeysICase(a, b *Object) int {
	if len(a.key) == len(b.key) {
		return lcCmp(a.key, b.key)
	}
	return lenDiff(uint32(len(a.key)), uint32(len(b.key)))
}

// lcCmp mirrors ucl_lc_cmp, quirks and all.
//
// It folds ASCII case and compares four bytes at a time -- but it does so by
// reinterpreting each folded group as a native uint32. On the little-endian
// hardware libucl is built for, that makes the *last* byte of a group the most
// significant, so the ordering within a group is byte i+3, then i+2, i+1, i:
// not lexicographic. The uint32 difference is then assigned to an int and can
// wrap.
//
// The leftover tail has a second quirk: inequality is detected on the *folded*
// bytes, but what is returned is the difference of the *raw* bytes, as signed
// chars (which is what a plain `char` is on x86).
func lcCmp(s, d []byte) int {
	l := len(s)
	leftover := l % 4
	fp := l - leftover

	i := 0
	for ; i != fp; i += 4 {
		n1 := uint32(asciiLower(s[i])) | uint32(asciiLower(s[i+1]))<<8 |
			uint32(asciiLower(s[i+2]))<<16 | uint32(asciiLower(s[i+3]))<<24
		n2 := uint32(asciiLower(d[i])) | uint32(asciiLower(d[i+1]))<<8 |
			uint32(asciiLower(d[i+2]))<<16 | uint32(asciiLower(d[i+3]))<<24
		if n1 != n2 {
			return int(int32(n1 - n2))
		}
	}

	for leftover > 0 {
		if asciiLower(s[i]) != asciiLower(d[i]) {
			return int(int8(s[i])) - int(int8(d[i]))
		}
		leftover--
		i++
	}
	return 0
}
