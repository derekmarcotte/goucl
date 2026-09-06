package core

// hash is the key/value store behind a TypeObject, mirroring C ucl_hash.c.
//
// Insertion order is the whole point of this type. libucl emits an object's
// keys in the order they were first inserted, and the golden outputs depend on
// it, so the map is paired with an explicit order slice. Iterating a Go map
// directly would produce a different order on every run.
type hash struct {
	m        map[string]*hashElt
	order    []*hashElt
	caseless bool
}

// hashElt is one entry. idx is the element's position in order, kept current
// so replace and delete stay O(1) rather than scanning.
type hashElt struct {
	obj *Object
	idx int
}

// newHash returns an empty hash. When caseless is set, keys are matched after
// ASCII case folding, mirroring the UCL_PARSER_KEY_LOWERCASE hash variant.
func newHash(caseless bool) *hash {
	return &hash{m: make(map[string]*hashElt), caseless: caseless}
}

// foldASCII lowercases only 'A'-'Z'.
//
// This is deliberately not strings.ToLower: that is Unicode-aware and would
// fold characters the C never touches, silently merging keys libucl keeps
// distinct.
func foldASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// mapKey returns the map lookup key for the given raw key bytes.
func (h *hash) mapKey(key []byte) string {
	if h.caseless {
		return string(foldASCII(key))
	}
	return string(key)
}

// Len reports the number of distinct keys.
func (h *hash) Len() int {
	if h == nil {
		return 0
	}
	return len(h.m)
}

// Lookup returns the chain head stored under key, or nil.
//
// The caller walks Next to reach the remaining values of an implicit array.
func (h *hash) Lookup(key []byte) *Object {
	if h == nil {
		return nil
	}
	if e, ok := h.m[h.mapKey(key)]; ok {
		return e.obj
	}
	return nil
}

// Insert adds obj under key.
//
// Mirroring ucl_hash_insert, a key that is already present is left untouched:
// this is a silent no-op, not a replace. Callers that mean to overwrite go
// through Replace so the key keeps its position in the emission order.
func (h *hash) Insert(key []byte, obj *Object) {
	k := h.mapKey(key)
	if _, exists := h.m[k]; exists {
		return
	}
	e := &hashElt{obj: obj, idx: len(h.order)}
	h.m[k] = e
	h.order = append(h.order, e)
}

// Replace swaps the value stored under key, keeping its original position in
// the emission order.
//
// Implementing this as Delete followed by Insert would move the key to the end
// of the order and change emitted output.
func (h *hash) Replace(key []byte, obj *Object) {
	k := h.mapKey(key)
	if e, ok := h.m[k]; ok {
		e.obj = obj
		return
	}
	h.Insert(key, obj)
}

// Delete removes key, preserving the relative order of the survivors.
func (h *hash) Delete(key []byte) {
	k := h.mapKey(key)
	e, ok := h.m[k]
	if !ok {
		return
	}
	delete(h.m, k)
	h.order = append(h.order[:e.idx], h.order[e.idx+1:]...)
	for i := e.idx; i < len(h.order); i++ {
		h.order[i].idx = i
	}
}

// Iterate calls fn for each chain head in insertion order, stopping early if
// fn returns false.
func (h *hash) Iterate(fn func(obj *Object) bool) {
	if h == nil {
		return
	}
	for _, e := range h.order {
		if !fn(e.obj) {
			return
		}
	}
}
