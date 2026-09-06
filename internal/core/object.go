package core

// The object API: construction, keys, lookup, mutation, arrays, iteration and
// copying. Mirrors the ucl_object_* family in src/ucl_util.c one-to-one; see
// docs/reference/object-model.md section 4.
//
// Deliberate faithfulness notes appear at each site where the C does something
// surprising. Where the C dereferences a NULL pointer (it is inconsistent
// about guarding), the Go returns the same zero value it would return for a
// rejected argument instead of panicking; those places are called out in the
// doc comments and in NOTES-object-api.md.

// SortFlag selects how SortKeys orders an object's keys, mirroring
// C enum ucl_object_keys_sort_flags.
type SortFlag int

const (
	SortKeysDefault   SortFlag = 0      // case-sensitive, non-recursive
	SortKeysICase     SortFlag = 1 << 0 // fold ASCII case when comparing keys
	SortKeysRecursive SortFlag = 1 << 1 // also sort nested objects
)

// IterateType selects what IterateFull walks, mirroring
// C enum ucl_iterate_type.
type IterateType int

const (
	IterateExplicit IterateType = 1 << 0 // container children
	IterateImplicit IterateType = 1 << 1 // implicit-array (duplicate key) chain
	IterateBoth     IterateType = IterateExplicit | IterateImplicit
)

// ArrayIndexNone is what ArrayIndexOf returns when the element is not present,
// mirroring the C's (unsigned int)-1.
const ArrayIndexNone = ^uint32(0)

// Errno values reported by IterateWithError, matching the C's errno constants
// on Linux. They exist because the safe iterator turns a non-zero error into
// its exception state, which IterChkExcpn exposes.
const (
	iterErrInval = 22 // EINVAL: the object has no hash at all
)

// setInt makes o a TypeInt holding v, clearing the rest of the value union.
//
// The C stores every value in one union, so assigning the integer slot
// destroys the string/array/object slot. The Go struct has a field per type,
// so the union has to be emulated explicitly or a stale slice would stay
// reachable.
func (o *Object) setInt(v int64) {
	o.clearValue()
	o.ObjType = TypeInt
	o.iv = v
}

// setBool makes o a TypeBoolean holding v.
func (o *Object) setBool(v bool) {
	o.clearValue()
	o.ObjType = TypeBoolean
	if v {
		o.iv = 1
	}
}

// setFloat makes o a TypeFloat holding v.
func (o *Object) setFloat(v float64) {
	o.clearValue()
	o.ObjType = TypeFloat
	o.dv = v
}

// setTime makes o a TypeTime holding v seconds.
func (o *Object) setTime(v float64) {
	o.clearValue()
	o.ObjType = TypeTime
	o.dv = v
}

// clearValue zeroes the value union. It deliberately leaves length, flags and
// the value trash alone: reclassifying a string as a number keeps both the
// original string length and the cached original text (section 2.4/2.8).
func (o *Object) clearValue() {
	o.iv = 0
	o.dv = 0
	o.sv = nil
	o.av = nil
	o.ov = nil
	o.ud = nil
}

// --- Construction (section 4.1) -------------------------------------------

// NewObject returns a new TypeNull object, mirroring ucl_object_new.
func NewObject() *Object { return TypedNew(TypeNull) }

// TypedNew returns a new object of the given type, mirroring
// ucl_object_typed_new.
func TypedNew(t Type) *Object { return NewFull(t, 0) }

// NewFull returns a new object of the given type and priority, mirroring
// ucl_object_new_full.
//
// A type outside the enum silently becomes TypeNull. A TypeArray gets its
// backing storage preallocated to capacity 8; a TypeObject does not get a hash
// until its first insert, which is observable through the iterator (see
// IterateFull).
func NewFull(t Type, priority uint) *Object {
	if t == TypeUserdata {
		o := NewUserdata(nil, nil, nil)
		o.SetPriority(priority)
		return o
	}

	o := &Object{}
	if t <= TypeNull {
		o.ObjType = t
	} else {
		o.ObjType = TypeNull
	}
	o.Next = nil
	o.Prev = o
	o.SetPriority(priority)

	if t == TypeArray {
		o.av = make([]*Object, 0, 8)
	}
	return o
}

// NewUserdata returns a new TypeUserdata object wrapping ptr, mirroring
// ucl_object_new_userdata.
//
// dtor and emitter are carried by value; a copy shares ptr with the original,
// exactly as in the C.
func NewUserdata(dtor func(any), emitter func(any) string, ptr any) *Object {
	o := &Object{ObjType: TypeUserdata, ud: ptr, udDtor: dtor, udEmitter: emitter}
	o.Next = nil
	o.Prev = o
	return o
}

// FromInt returns a new TypeInt object, mirroring ucl_object_fromint.
func FromInt(v int64) *Object {
	o := NewObject()
	o.setInt(v)
	return o
}

// FromDouble returns a new TypeFloat object, mirroring ucl_object_fromdouble.
func FromDouble(v float64) *Object {
	o := NewObject()
	o.setFloat(v)
	return o
}

// FromBool returns a new TypeBoolean object, mirroring ucl_object_frombool.
func FromBool(v bool) *Object {
	o := NewObject()
	o.setBool(v)
	return o
}

// FromString returns a new TypeString object holding s, mirroring
// ucl_object_fromstring: no escaping, no trimming, no value parsing.
func FromString(s string) *Object {
	b := make([]byte, len(s))
	copy(b, s)
	return FromStringCommon(b, StringRaw)
}

// FromLString returns a new TypeString object holding b, mirroring
// ucl_object_fromlstring.
//
// Divergence: the C treats len == 0 as "call strlen", so it cannot build a
// deliberately empty string this way. A Go slice carries its own length, so an
// empty slice really does produce an empty string.
func FromLString(b []byte) *Object { return FromStringCommon(b, StringRaw) }

// FromStringCommon builds a string object from b, optionally trimming,
// escaping, and reclassifying it as a boolean or number. It mirrors
// ucl_object_fromstring_common; see docs/reference/object-model.md section 5.1.
//
// Three consequences of the C worth remembering:
//
//   - The object keeps its string rendering as the cached value trash even
//     after being reclassified, so ToStringForced returns the original text
//     rather than a rendering of the number.
//   - length keeps the *string* length after reclassification.
//   - The parse consumes a prefix: "10;xyz" becomes TypeInt 10 because ';'
//     ends an atom, while "10abc" stays a string.
//
// A nil b returns nil, mirroring the C's NULL guard.
func FromStringCommon(b []byte, flags StringFlag) *Object {
	if b == nil {
		return nil
	}

	o := NewObject()

	start, end := 0, len(b)
	if flags&StringTrim != 0 {
		for start < len(b) && isWhitespaceUnsafe(b[start]) {
			start++
		}
		// The trailing scan stops at start, so an all-whitespace input trims
		// to the empty string rather than underflowing.
		e := len(b) - 1
		for e > start && isWhitespaceUnsafe(b[e]) {
			e--
		}
		end = e + 1
		if end < start {
			end = start
		}
	}

	o.ObjType = TypeString
	var dst []byte
	if flags&StringEscape != 0 {
		dst = escapeJSONString(b[start:end])
	} else {
		dst = make([]byte, end-start)
		copy(dst, b[start:end])
	}
	o.sv = dst
	o.length = uint32(len(dst))
	o.valueTrash = dst
	o.hasValueTrash = true

	// StringParseBytes on its own is not part of StringParse, so it never
	// triggers a parse by itself.
	if flags&StringParse != 0 {
		allowDouble := flags&StringParseDouble != 0
		numberBytes := flags&StringParseBytes != 0
		allowTime := flags&StringParseTime != 0

		if flags&StringParseBoolean != 0 {
			if !maybeParseBoolean(o, dst) && flags&StringParseNumber != 0 {
				maybeParseNumber(o, dst, allowDouble, numberBytes, allowTime)
			}
		} else {
			maybeParseNumber(o, dst, allowDouble, numberBytes, allowTime)
		}
	}

	return o
}

// --- Type and identity (section 4.2) --------------------------------------

// TypeString returns the name of the object's type, mirroring
// ucl_object_type_to_string(ucl_object_type(obj)).
//
// Note that both TypeFloat and TypeTime are called "number": the C has no way
// to name a time. This is not the same mapping as Type.String in types.go,
// which names each type distinctly.
func (o *Object) TypeString() string {
	switch o.Type() {
	case TypeObject:
		return "object"
	case TypeArray:
		return "array"
	case TypeInt:
		return "integer"
	case TypeFloat, TypeTime:
		return "number"
	case TypeString:
		return "string"
	case TypeBoolean:
		return "boolean"
	case TypeUserdata:
		return "userdata"
	case TypeNull:
		return "null"
	}
	return "unknown"
}

// StringToType maps a type name to its Type, mirroring
// ucl_object_string_to_type. The match is ASCII case-insensitive, and
// "number" maps to TypeFloat -- there is no spelling for TypeTime.
func StringToType(in string) (Type, bool) {
	switch {
	case asciiEqualFold([]byte(in), "object"):
		return TypeObject, true
	case asciiEqualFold([]byte(in), "array"):
		return TypeArray, true
	case asciiEqualFold([]byte(in), "integer"):
		return TypeInt, true
	case asciiEqualFold([]byte(in), "number"):
		return TypeFloat, true
	case asciiEqualFold([]byte(in), "string"):
		return TypeString, true
	case asciiEqualFold([]byte(in), "boolean"):
		return TypeBoolean, true
	case asciiEqualFold([]byte(in), "null"):
		return TypeNull, true
	case asciiEqualFold([]byte(in), "userdata"):
		return TypeUserdata, true
	}
	return TypeNull, false
}

// Key returns the object's key bytes, or nil if it has none, mirroring
// ucl_object_key.
//
// The C's key trash (a NUL-terminated library-owned copy) has no Go analogue:
// a Go slice is already an owned, length-carrying value. A keyless object
// returns nil, and a present but empty key returns a non-nil empty slice, so
// the two stay distinguishable exactly as NULL and "" are in the C.
func (o *Object) Key() []byte {
	if o == nil {
		return nil
	}
	return o.key
}

// KeyL returns the object's key bytes and their length, mirroring
// ucl_object_keyl. Unlike Key it is documented as zero-copy in the C; both are
// zero-copy here.
func (o *Object) KeyL() ([]byte, uint32) {
	if o == nil {
		return nil, 0
	}
	return o.key, uint32(len(o.key))
}

// --- Object insertion (section 4.3) ---------------------------------------

// InsertKey stores elt in the object under key, mirroring
// ucl_object_insert_key.
//
// A duplicate key does not replace: elt's whole chain is spliced onto the
// existing value's chain, forming an implicit array, and length is not
// incremented (only a genuinely new key bumps it). A TypeNull receiver is
// converted to a TypeObject in place; any other non-object type is rejected.
//
// InsertKey takes ownership of elt, as in the C: pass Ref(elt) to keep your
// own reference.
//
// Note the C's "keylen == 0 means strlen" fallback has no analogue here: the
// slice length is authoritative, so an empty key really is an empty key.
func (top *Object) InsertKey(elt *Object, key []byte, copyKey bool) bool {
	return top.insertKeyCommon(elt, key, copyKey, false, false)
}

// InsertKeyMerged stores elt under key, merging into any existing value,
// mirroring ucl_object_insert_key_merged.
//
// Merging is per-type: two objects are mixed key by key, an object absorbs a
// scalar (and vice versa), and two scalars form an implicit array.
func (top *Object) InsertKeyMerged(elt *Object, key []byte, copyKey bool) bool {
	return top.insertKeyCommon(elt, key, copyKey, true, false)
}

// ReplaceKey stores elt under key, replacing any existing value, mirroring
// ucl_object_replace_key.
//
// The replaced key keeps its position in the emission order; the whole
// existing chain is dropped, and length is unchanged.
func (top *Object) ReplaceKey(elt *Object, key []byte, copyKey bool) bool {
	return top.insertKeyCommon(elt, key, copyKey, false, true)
}

func (top *Object) insertKeyCommon(elt *Object, key []byte, copyKey, merge, replace bool) bool {
	if elt == nil || key == nil {
		return false
	}
	if top == nil {
		return false
	}
	if top.ObjType != TypeObject {
		if top.ObjType == TypeNull {
			// A null can be converted into an object in place.
			top.ObjType = TypeObject
		} else {
			return false
		}
	}
	if top.ov == nil {
		// The object API always builds a case-sensitive hash; only the parser
		// ever asks for a caseless one.
		top.ov = newHash(false)
	}

	for _, ch := range key {
		if isUCLUnsafe(ch) {
			elt.flags |= FlagNeedKeyEscape
			break
		}
	}

	elt.key = key
	if copyKey {
		elt.key = append([]byte(nil), key...)
	}

	found := top.ov.Lookup(elt.key)
	if found == nil {
		top.ov.Insert(elt.key, elt)
		top.length++
		// The C returns true here regardless of the replace flag.
		return true
	}

	switch {
	case replace:
		top.ov.Replace(elt.key, elt)
		Unref(found)

	case merge:
		switch {
		case found.ObjType != TypeObject && elt.ObjType == TypeObject:
			// Absorb the old scalar into the new object, then re-insert.
			// This is delete + insert rather than replace, so it is the one
			// merge path that moves the key to the tail of the emission
			// order. Reproduced deliberately (section 7.6 item 4).
			elt.insertKeyCommon(found, found.key, copyKey, false, false)
			top.ov.Delete(found.key)
			top.ov.Insert(elt.key, elt)

		case found.ObjType == TypeObject && elt.ObjType != TypeObject:
			found.insertKeyCommon(elt, elt.key, copyKey, false, false)

		case found.ObjType == TypeObject && elt.ObjType == TypeObject:
			var it Iter
			for cur := Iterate(elt, &it, true); cur != nil; cur = Iterate(elt, &it, true) {
				found.insertKeyCommon(Ref(cur), cur.key, copyKey, true, false)
			}
			Unref(elt)

		default:
			// Two scalars: build an implicit array.
			dlConcat(found, elt)
		}

	default:
		dlConcat(found, elt)
	}

	return true
}

// --- Object deletion (section 4.4) ----------------------------------------

// DeleteKey removes key and everything on its implicit-array chain, mirroring
// ucl_object_delete_keyl. It reports whether the key was present.
//
// length is decremented unconditionally on success, so an object whose length
// was inflated by the parser can underflow it -- as in the C, where it is an
// unsigned counter.
func (top *Object) DeleteKey(key []byte) bool {
	if top == nil || key == nil {
		return false
	}
	found := top.LookupLen(key)
	if found == nil {
		return false
	}
	// The C deletes by the *stored* object's key bytes, not by the search key.
	top.ov.Delete(found.key)
	Unref(found)
	top.length--
	return true
}

// PopKey removes key and returns the value, mirroring ucl_object_pop_keyl.
//
// The caller takes ownership of the returned object, which is still the head
// of its implicit-array chain: Next and Prev are left untouched.
func (top *Object) PopKey(key []byte) *Object {
	if top == nil || key == nil {
		return nil
	}
	found := top.LookupLen(key)
	if found == nil {
		return nil
	}
	top.ov.Delete(found.key)
	top.length--
	return found
}

// --- Lookup (section 4.5) -------------------------------------------------

// LookupLen returns the value stored under exactly these key bytes, or nil,
// mirroring ucl_object_lookup_len.
//
// It returns the head of the implicit-array chain; walk Next for the rest.
// A nil key is rejected; an empty non-nil key looks up the empty key, since
// there is no strlen fallback on this path.
func (o *Object) LookupLen(key []byte) *Object {
	if o == nil || o.ObjType != TypeObject || key == nil {
		return nil
	}
	return o.ov.Lookup(key)
}

// Lookup returns the value stored under key, or nil, mirroring
// ucl_object_lookup.
func (o *Object) Lookup(key string) *Object {
	b := make([]byte, len(key))
	copy(b, key)
	return o.LookupLen(b)
}

// LookupAny returns the value of the first key that is present, or nil,
// mirroring the variadic ucl_object_lookup_any.
func (o *Object) LookupAny(keys ...string) *Object {
	if o == nil || len(keys) == 0 {
		return nil
	}
	for _, k := range keys {
		if ret := o.Lookup(k); ret != nil {
			return ret
		}
	}
	return nil
}

// LookupPath resolves a '.'-separated path, mirroring ucl_object_lookup_path.
func (o *Object) LookupPath(path string) *Object {
	return o.LookupPathChar(path, '.')
}

// LookupPathChar resolves a path separated by sep, mirroring
// ucl_object_lookup_path_char.
//
// Leading separators and empty segments are skipped. A segment is read as a
// decimal index only when the *current* node is an array; for anything else it
// is a key lookup, which yields nil unless the node is an object. An empty
// path, or one made only of separators, returns nil.
func (o *Object) LookupPathChar(path string, sep byte) *Object {
	top := o
	if top == nil {
		return nil
	}

	var found *Object
	p := 0
	for p < len(path) && path[p] == sep {
		p++
	}

	c := p
	for p < len(path) {
		p++
		if p == len(path) || path[p] == sep {
			if p > c {
				if top.ObjType == TypeArray {
					// strtoul reads from the segment start to wherever it
					// stops, which may be past the segment; the C then insists
					// the stopping byte is the separator or the terminator.
					rest := path[c:]
					idx, n := cStrtoul(rest)
					if n < len(rest) && rest[n] != sep {
						return nil
					}
					found = ArrayFindIndex(top, uint32(idx))
				} else {
					found = top.LookupLen([]byte(path[c:p]))
				}
				if found == nil {
					return nil
				}
				top = found
			}
			if p != len(path) {
				c = p + 1
			}
		}
	}
	return found
}

// --- Merging (section 4.6) ------------------------------------------------

// Merge folds elt into top, mirroring ucl_object_merge.
//
// When deep is false the two trees genuinely alias afterwards, and a later
// mutation through either is visible through both; when it is true, elt's
// children are deep-copied first.
//
// Merge is not InsertKeyMerged: it never builds implicit arrays. A duplicate
// key whose existing value is a container is merged recursively, and one whose
// value is a scalar is replaced in place. An unsuccessful recursive merge
// aborts immediately, leaving the partial result in place (as in the C, which
// leaks the remainder).
func (top *Object) Merge(elt *Object, deep bool) bool {
	if top == nil || elt == nil {
		return false
	}

	switch top.ObjType {
	case TypeArray:
		if elt.ObjType == TypeArray {
			return ArrayMerge(top, elt, deep)
		}
		if deep {
			ArrayAppend(top, Copy(elt))
		} else {
			ArrayAppend(top, Ref(elt))
		}
		return true

	case TypeObject:
		if elt.ObjType == TypeObject {
			// Chain heads only, in insertion order.
			var cps []*Object
			elt.ov.Iterate(func(cur *Object) bool {
				cps = append(cps, cur)
				return true
			})
			for _, cur := range cps {
				if !top.mergeOne(cur, deep) {
					return false
				}
			}
			return true
		}
		// A non-object is merged under its own key, which may be absent --
		// then the lookup is on a zero-length key.
		return top.mergeOne(elt, deep)

	default:
		// Cannot merge trivial objects.
		return false
	}
}

// mergeOne applies the found/insert/replace logic of ucl_object_merge to a
// single source value.
func (top *Object) mergeOne(cur *Object, deep bool) bool {
	var cp *Object
	if deep {
		cp = Copy(cur)
	} else {
		cp = Ref(cur)
	}

	var found *Object
	if top.ov != nil {
		found = top.ov.Lookup(cp.key)
	}

	if found == nil {
		if top.ov == nil {
			top.ov = newHash(false)
		}
		top.ov.Insert(cp.key, cp)
		top.length++
		return true
	}

	if found.ObjType == TypeObject || found.ObjType == TypeArray {
		if !found.Merge(cp, deep) {
			return false
		}
		Unref(cp)
		return true
	}

	top.ov.Replace(found.key, cp)
	Unref(found)
	return true
}

// --- Arrays (section 4.7) -------------------------------------------------

// ArrayAppend appends elt to the array, mirroring ucl_array_append. It takes
// ownership of elt.
//
// The C dereferences top before its NULL check; a nil top returns false here.
func ArrayAppend(top, elt *Object) bool {
	if top == nil || top.ObjType != TypeArray || elt == nil {
		return false
	}
	top.av = append(top.av, elt)
	top.length++
	return true
}

// ArrayPrepend inserts elt at the front of the array, mirroring
// ucl_array_prepend. It is O(n), as in the C.
func ArrayPrepend(top, elt *Object) bool {
	if top == nil || top.ObjType != TypeArray || elt == nil {
		return false
	}
	top.av = append(top.av, nil)
	copy(top.av[1:], top.av)
	top.av[0] = elt
	top.length++
	return true
}

// ArrayMerge appends elt's elements to top, mirroring ucl_array_merge.
//
// With deep false the two arrays share element pointers afterwards. Both must
// be arrays; anything else returns false.
//
// The length accounting is wrong upstream and is reproduced here: the C's
// counting loop starts at the *source* length instead of the destination's old
// length, so top's length and ArraySize(top) disagree unless the two arrays
// were the same size. Compare reads length for arrays, so this is observable.
func ArrayMerge(top, elt *Object, deep bool) bool {
	if elt == nil || top == nil || top.ObjType != TypeArray || elt.ObjType != TypeArray {
		return false
	}

	var cp *Object
	if deep {
		cp = Copy(elt)
	} else {
		cp = Ref(elt)
	}

	// A nil backing slice means the C's kvec pointer was NULL, and the C skips
	// the concat -- and, importantly, the buggy counting loop -- entirely.
	if top.av != nil && cp.av != nil {
		n2 := len(cp.av)
		top.av = append(top.av, cp.av...)
		for i := n2; i < len(top.av); i++ {
			if top.av[i] == nil {
				continue
			}
			top.length++
		}
	}
	return true
}

// ArrayDelete removes elt from the array by pointer identity and returns it,
// or nil if it is absent, mirroring ucl_array_delete. The caller takes
// ownership of the result.
func ArrayDelete(top, elt *Object) *Object {
	if top == nil || top.ObjType != TypeArray || top.av == nil {
		return nil
	}
	for i, e := range top.av {
		if e == elt {
			top.av = append(top.av[:i], top.av[i+1:]...)
			top.length--
			return elt
		}
	}
	return nil
}

// ArrayHead returns the first element of the array, or nil, mirroring
// ucl_array_head.
func ArrayHead(top *Object) *Object {
	if top == nil || top.ObjType != TypeArray || top.av == nil || len(top.av) == 0 {
		return nil
	}
	return top.av[0]
}

// ArrayTail returns the last element of the array, or nil, mirroring
// ucl_array_tail.
func ArrayTail(top *Object) *Object {
	if top == nil || top.ObjType != TypeArray || top.av == nil || len(top.av) == 0 {
		return nil
	}
	return top.av[len(top.av)-1]
}

// ArrayPopLast removes and returns the last element, mirroring
// ucl_array_pop_last. The caller takes ownership; the element's own chain is
// left untouched.
func ArrayPopLast(top *Object) *Object {
	if top == nil || top.ObjType != TypeArray || len(top.av) == 0 {
		return nil
	}
	ret := top.av[len(top.av)-1]
	top.av = top.av[:len(top.av)-1]
	top.length--
	return ret
}

// ArrayPopFirst removes and returns the first element, mirroring
// ucl_array_pop_first. It is O(n).
func ArrayPopFirst(top *Object) *Object {
	if top == nil || top.ObjType != TypeArray || len(top.av) == 0 {
		return nil
	}
	ret := top.av[0]
	top.av = append(top.av[:0], top.av[1:]...)
	top.length--
	return ret
}

// ArraySize returns the number of slots in the array, mirroring
// ucl_array_size.
//
// This is the backing vector's length, which is not the same as the tracked
// length after ArrayMerge; see the note there.
func ArraySize(top *Object) uint32 {
	if top == nil || top.ObjType != TypeArray {
		return 0
	}
	return uint32(len(top.av))
}

// ArrayFindIndex returns the element at index, or nil if the index is out of
// range, mirroring ucl_array_find_index. The element itself may be nil.
func ArrayFindIndex(top *Object, index uint32) *Object {
	if top == nil || top.ObjType != TypeArray {
		return nil
	}
	if index >= uint32(len(top.av)) {
		return nil
	}
	return top.av[index]
}

// ArrayIndexOf returns the index of elt by pointer identity, or
// ArrayIndexNone, mirroring ucl_array_index_of.
func ArrayIndexOf(top, elt *Object) uint32 {
	if top == nil || top.ObjType != TypeArray || top.av == nil {
		return ArrayIndexNone
	}
	for i, e := range top.av {
		if e == elt {
			return uint32(i)
		}
	}
	return ArrayIndexNone
}

// ArrayReplaceIndex stores elt at index and returns the previous occupant,
// mirroring ucl_array_replace_index.
//
// The caller takes ownership of the returned object; length is unchanged, and
// elt may be nil, which leaves a hole that iteration skips and Compare treats
// specially.
func ArrayReplaceIndex(top, elt *Object, index uint32) *Object {
	if top == nil || top.ObjType != TypeArray {
		return nil
	}
	if index >= uint32(len(top.av)) {
		return nil
	}
	ret := top.av[index]
	top.av[index] = elt
	return ret
}

// EltAppend appends elt to the implicit-array chain headed by head and returns
// the head, mirroring ucl_elt_append.
//
// No container is involved: this only manipulates Next/Prev. The C
// special-cases a TypeUserdata head, but that branch is byte-identical to the
// normal one, so both are the same code here.
func EltAppend(head, elt *Object) *Object {
	return dlAppend(head, elt)
}

// Reserve grows an array's capacity or hints an object's, mirroring
// ucl_object_reserve. It is a no-op for other types and, in Go, for objects.
func Reserve(o *Object, n int) bool {
	if o == nil {
		return true
	}
	if o.ObjType == TypeArray {
		if o.av == nil {
			o.av = make([]*Object, 0, n)
		} else if cap(o.av) < n {
			grown := make([]*Object, len(o.av), n)
			copy(grown, o.av)
			o.av = grown
		}
	}
	return true
}

// --- Iteration (section 4.8) ----------------------------------------------

// Iter is the cursor threaded through Iterate, mirroring the C's opaque
// ucl_object_iter_t.
//
// The zero value is the C's NULL iterator, i.e. "start from the beginning".
// The C smuggles three different things through one void*: a heap cursor for
// objects, an integer index for arrays, and an object pointer for the linear
// chain walk. Which one is live is decided by the object being iterated, so
// they are kept as separate fields here.
type Iter struct {
	hashPos int     // next index into the hash's order slice
	hashSet bool    // the hash cursor exists (the C's non-NULL heap cursor)
	arrIdx  uint32  // array cursor
	linear  *Object // implicit-chain cursor; nil means "not started"
}

// Iterate returns the next child of obj, or nil when the iteration is done,
// mirroring ucl_object_iterate.
//
// With expandValues true, an object yields its values (chain *heads* only --
// implicit-array siblings are not visited) and an array yields its non-nil
// elements. With expandValues false, or for a scalar, it walks the object's
// own implicit-array chain starting with obj itself.
//
// Usage mirrors the C loop:
//
//	var it Iter
//	for cur := Iterate(obj, &it, true); cur != nil; cur = Iterate(obj, &it, true) {
//	}
func Iterate(obj *Object, iter *Iter, expandValues bool) *Object {
	return IterateWithError(obj, iter, expandValues, nil)
}

// IterateWithError is Iterate with an out-parameter for the hash iterator's
// error, mirroring ucl_object_iterate_with_error.
//
// ep receives EINVAL when an object has no hash at all (which is the state of
// a freshly created, never-inserted-into TypeObject) and 0 otherwise. The
// array and linear paths leave it untouched. The safe iterator turns a
// non-zero value into its exception state.
func IterateWithError(obj *Object, iter *Iter, expandValues bool, ep *int) *Object {
	if obj == nil || iter == nil {
		return nil
	}

	if expandValues {
		switch obj.ObjType {
		case TypeObject:
			return hashIterate2(obj.ov, iter, ep)
		case TypeArray:
			var elt *Object
			if obj.av != nil {
				idx := iter.arrIdx
				for idx < uint32(len(obj.av)) {
					if e := obj.av[idx]; e != nil {
						elt = e
						idx++
						break
					}
					idx++
				}
				iter.arrIdx = idx
			}
			return elt
		}
	}

	// Everything else is a linear walk of the implicit-array chain. The first
	// call yields obj itself; the cursor parks on obj to signal completion.
	elt := iter.linear
	if elt == nil {
		elt = obj
	} else if elt == obj {
		return nil
	}
	if elt.Next != nil {
		iter.linear = elt.Next
	} else {
		iter.linear = obj
	}
	return elt
}

// hashIterate2 mirrors ucl_hash_iterate2, including its restart behaviour: on
// exhaustion the C frees the cursor and stores NULL back, so calling again
// starts over from the head of the list.
func hashIterate2(h *hash, iter *Iter, ep *int) *Object {
	if h == nil {
		if ep != nil {
			*ep = iterErrInval
		}
		return nil
	}
	if !iter.hashSet {
		iter.hashSet = true
		iter.hashPos = 0
	}
	if ep != nil {
		*ep = 0
	}
	if iter.hashPos < len(h.order) {
		ret := h.order[iter.hashPos].obj
		iter.hashPos++
		return ret
	}
	iter.hashSet = false
	iter.hashPos = 0
	return nil
}

// IterateEnd abandons an in-progress iteration, mirroring
// ucl_object_iterate_end.
//
// In the C this frees the heap cursor an object iteration allocated; here it
// only resets the cursor, and calling it is optional.
func IterateEnd(obj *Object, iter *Iter) {
	if iter == nil {
		return
	}
	*iter = Iter{}
}

// Safe-iterator state, mirroring enum ucl_safe_iter_flags.
const (
	safeIterUndefined = iota
	safeIterInsideArray
	safeIterInsideObject
	safeIterImplicit
	safeIterException
)

// SafeIter walks a value and its implicit-array chain together, mirroring the
// C's struct ucl_object_safe_iter.
type SafeIter struct {
	flags  int
	implIt *Object
	explIt Iter
}

// IterateNew returns a safe iterator positioned at obj, mirroring
// ucl_object_iterate_new.
func IterateNew(obj *Object) *SafeIter {
	return &SafeIter{flags: safeIterUndefined, implIt: obj}
}

// IterateReset re-points a safe iterator at obj, mirroring
// ucl_object_iterate_reset.
func IterateReset(it *SafeIter, obj *Object) *SafeIter {
	if it == nil {
		return nil
	}
	it.implIt = obj
	it.explIt = Iter{}
	it.flags = safeIterUndefined
	return it
}

// IterateSafe returns the next value from a safe iterator, mirroring
// ucl_object_iterate_safe.
func IterateSafe(it *SafeIter, expandValues bool) *Object {
	if expandValues {
		return IterateFull(it, IterateBoth)
	}
	return IterateFull(it, IterateImplicit)
}

// IterateFull returns the next value from a safe iterator, mirroring
// ucl_object_iterate_full.
//
// A container is walked child by child; when it is exhausted and typ includes
// IterateImplicit, the iterator moves on to the next link of the implicit
// chain. A scalar is returned directly.
//
// Two faithfulness notes, neither of which is observable but both of which are
// reproduced so this reads like the C:
//
//   - The C's "flatten containers away when typ includes IterateExplicit"
//     branch is dead code. It only runs in the scalar case, where the value it
//     tests is the scalar itself, so the test can never pass; a container on
//     the chain is expanded by the container branch above instead.
//   - The C recurses through ucl_object_iterate_safe(it, type), which coerces
//     the enum to that function's bool parameter, so every recursive step
//     silently upgrades typ to IterateBoth. Since IterateBoth and
//     IterateImplicit differ only in the dead branch, this changes nothing.
func IterateFull(it *SafeIter, typ IterateType) *Object {
	if it == nil || it.implIt == nil {
		return nil
	}

	switch it.implIt.ObjType {
	case TypeObject:
		it.flags = safeIterInsideObject
		var ern int
		ret := IterateWithError(it.implIt, &it.explIt, true, &ern)

		if ret == nil && ern != 0 {
			it.flags = safeIterException
			return nil
		}
		if ret == nil && typ&IterateImplicit != 0 {
			it.implIt = it.implIt.Next
			it.explIt = Iter{}
			return IterateSafe(it, typ != 0) // see the faithfulness note
		}
		return ret

	case TypeArray:
		it.flags = safeIterInsideArray
		ret := Iterate(it.implIt, &it.explIt, true)

		if ret == nil && typ&IterateImplicit != 0 {
			it.implIt = it.implIt.Next
			it.explIt = Iter{}
			return IterateSafe(it, typ != 0)
		}
		return ret

	default:
		it.flags = safeIterImplicit
		ret := it.implIt
		it.implIt = it.implIt.Next

		if typ&IterateExplicit != 0 {
			if ret.ObjType == TypeObject || ret.ObjType == TypeArray {
				return IterateSafe(it, typ != 0)
			}
		}
		return ret
	}
}

// IterChkExcpn reports whether the safe iterator stopped because of an
// internal error, mirroring ucl_object_iter_chk_excpn.
func IterChkExcpn(it *SafeIter) bool {
	return it != nil && it.flags == safeIterException
}

// IterateFree releases a safe iterator, mirroring ucl_object_iterate_free.
// It exists so ported C code reads the same; the GC does the work here.
func IterateFree(it *SafeIter) {}

// --- Refcounting and copying (section 4.9) --------------------------------

// Ref takes a reference to obj, mirroring ucl_object_ref.
//
// Refcounting itself is the GC's job here, but one branch is a real behaviour
// and must be kept: an object flagged FlagEphemeral is *deep copied* instead,
// so whatever stores the result holds an independent object.
func Ref(obj *Object) *Object {
	if obj == nil {
		return nil
	}
	if obj.flags&FlagEphemeral != 0 {
		return Copy(obj)
	}
	return obj
}

// Unref drops a reference to obj, mirroring ucl_object_unref.
//
// It is a no-op: the GC owns lifetime. It is kept so that the ownership
// contract stays documented and ported C code reads the same.
func Unref(obj *Object) {}

// Free releases obj regardless of its refcount, mirroring the deprecated
// ucl_object_free. It is a no-op here, and any test that depended on
// use-after-free behaviour has no Go equivalent.
func Free(obj *Object) {}

// Copy returns a deep copy of obj, mirroring ucl_object_copy.
func Copy(obj *Object) *Object {
	if obj == nil {
		return nil
	}
	return copyInternal(obj, true)
}

// copyInternal mirrors ucl_object_copy_internal. allowArray decides whether
// the source's implicit-array chain is copied too.
func copyInternal(other *Object, allowArray bool) *Object {
	n := new(Object)
	*n = *other // the C memcpy: type, flags, length, key and value all carry over

	// A copy is never ephemeral.
	n.flags &^= FlagEphemeral
	n.Next = nil
	n.Prev = n

	if other.key != nil {
		n.key = append([]byte(nil), other.key...)
	}

	if other.hasValueTrash {
		// The C duplicates the trash with strdup, which stops at the first NUL
		// -- so copying a binary string truncates it while length is carried
		// over unchanged.
		n.valueTrash = cstrdup(other.valueTrash)
		n.hasValueTrash = true
		if n.ObjType == TypeString {
			n.sv = n.valueTrash
		}
	}

	switch other.ObjType {
	case TypeArray, TypeObject:
		n.clearValue()
		n.length = 0

		var it Iter
		for cur := Iterate(other, &it, true); cur != nil; cur = Iterate(other, &it, true) {
			if other.ObjType == TypeArray {
				// Array elements never carry chains.
				ArrayAppend(n, copyInternal(cur, false))
			} else {
				cp := copyInternal(cur, true)
				// Because InsertKey only bumps length for a new key, a copy of
				// a parsed object containing implicit arrays ends up with a
				// smaller length than the original.
				n.InsertKey(cp, cp.key, false)
			}
		}

	default:
		if allowArray && other.Next != nil {
			for cur := other.Next; cur != nil; cur = cur.Next {
				dlAppend(n, copyInternal(cur, false))
			}
		}
	}

	return n
}

// cstrdup copies b the way strdup would: up to, but not including, the first
// NUL byte.
func cstrdup(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return append([]byte(nil), b[:i]...)
		}
	}
	return append([]byte(nil), b...)
}
