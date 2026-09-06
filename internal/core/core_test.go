package core

import "testing"

// keysOf returns the hash's keys in iteration order.
func keysOf(h *hash) []string {
	var got []string
	h.Iterate(func(o *Object) bool {
		got = append(got, string(o.key))
		return true
	})
	return got
}

func put(h *hash, key string) *Object {
	o := &Object{key: []byte(key), ObjType: TypeString, sv: []byte(key)}
	h.Insert([]byte(key), o)
	return o
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}

// Emission order is insertion order. A Go map would randomise it, which would
// break every golden.
func TestHashPreservesInsertionOrder(t *testing.T) {
	h := newHash(false)
	for _, k := range []string{"zebra", "apple", "mango", "banana"} {
		put(h, k)
	}
	eq(t, "order", keysOf(h), []string{"zebra", "apple", "mango", "banana"})
}

// Replace must keep the key where it is. Implementing it as delete+insert
// would move the key to the tail and change emitted output.
func TestHashReplaceKeepsPosition(t *testing.T) {
	h := newHash(false)
	put(h, "a")
	put(h, "b")
	put(h, "c")

	repl := &Object{key: []byte("a"), ObjType: TypeInt, iv: 42}
	h.Replace([]byte("a"), repl)

	eq(t, "order after replace", keysOf(h), []string{"a", "b", "c"})
	if got := h.Lookup([]byte("a")); got != repl {
		t.Fatalf("lookup after replace returned %v, want the replacement", got)
	}
}

// Insert on an existing key is a silent no-op, not a replace (ucl_hash_insert).
func TestHashInsertExistingIsNoOp(t *testing.T) {
	h := newHash(false)
	first := put(h, "k")
	second := &Object{key: []byte("k"), ObjType: TypeInt, iv: 9}
	h.Insert([]byte("k"), second)

	if got := h.Lookup([]byte("k")); got != first {
		t.Error("Insert overwrote an existing key; it must be a no-op")
	}
	if h.Len() != 1 {
		t.Errorf("Len = %d, want 1", h.Len())
	}
}

func TestHashDeletePreservesOrderOfSurvivors(t *testing.T) {
	h := newHash(false)
	for _, k := range []string{"a", "b", "c", "d"} {
		put(h, k)
	}
	h.Delete([]byte("b"))
	eq(t, "order after delete", keysOf(h), []string{"a", "c", "d"})

	// The idx fix-up must leave later deletes correct too.
	h.Delete([]byte("d"))
	eq(t, "order after second delete", keysOf(h), []string{"a", "c"})
}

// The caseless hash folds ASCII only. strings.ToLower is Unicode-aware and
// would merge keys libucl keeps distinct.
func TestHashCaselessFoldsASCIIOnly(t *testing.T) {
	h := newHash(true)
	put(h, "Key")
	if h.Lookup([]byte("KEY")) == nil || h.Lookup([]byte("key")) == nil {
		t.Error("caseless hash failed to match an ASCII case variant")
	}

	// U+212A KELVIN SIGN folds to 'k' under Unicode rules but must not here.
	caseless := newHash(true)
	put(caseless, "K")
	if caseless.Lookup([]byte("k")) != nil {
		t.Error("caseless hash applied Unicode folding; it must fold ASCII only")
	}
}

func TestHashCaseSensitiveByDefault(t *testing.T) {
	h := newHash(false)
	put(h, "Key")
	if h.Lookup([]byte("key")) != nil {
		t.Error("case-sensitive hash matched a differently-cased key")
	}
}

// Priority occupies the top four bits of the flags word and must not disturb
// the low twelve.
func TestPriorityPacking(t *testing.T) {
	o := &Object{}
	o.flags = FlagMultiline | FlagBinary

	o.SetPriority(5)
	if got := o.Priority(); got != 5 {
		t.Errorf("Priority = %d, want 5", got)
	}
	if got := o.Flags(); got != FlagMultiline|FlagBinary {
		t.Errorf("flags = %#x, want %#x", got, FlagMultiline|FlagBinary)
	}

	// Out-of-range priorities are masked to 0..15, not rejected.
	o.SetPriority(0xFF)
	if got := o.Priority(); got != 15 {
		t.Errorf("Priority after masking = %d, want 15", got)
	}
	if got := o.Flags(); got != FlagMultiline|FlagBinary {
		t.Errorf("masking priority disturbed flags: %#x", got)
	}
}

func TestPriorityNilObject(t *testing.T) {
	var o *Object
	if o.Priority() != 0 {
		t.Error("nil Priority must be 0")
	}
	o.SetPriority(3) // must not panic
	if o.Type() != TypeNull {
		t.Error("nil Type must be TypeNull")
	}
}

// The chain invariants come from utlist.h: head.Prev is the tail, the tail's
// Next is nil, and a lone element points its Prev at itself.
func TestChainAppendInvariants(t *testing.T) {
	a := &Object{key: []byte("a")}
	head := dlAppend(nil, a)
	if head != a || a.Prev != a || a.Next != nil {
		t.Fatal("single element must be its own Prev with a nil Next")
	}

	b := &Object{key: []byte("b")}
	c := &Object{key: []byte("c")}
	head = dlAppend(head, b)
	head = dlAppend(head, c)

	if head != a {
		t.Error("append must not change the head")
	}
	if head.Prev != c {
		t.Error("head.Prev must be the tail")
	}
	if c.Next != nil {
		t.Error("tail.Next must be nil")
	}
	if dlLen(head) != 3 {
		t.Errorf("chain length = %d, want 3", dlLen(head))
	}
}

func TestChainDelete(t *testing.T) {
	a, b, c := &Object{key: []byte("a")}, &Object{key: []byte("b")}, &Object{key: []byte("c")}
	head := dlAppend(dlAppend(dlAppend(nil, a), b), c)

	head = dlDelete(head, b)
	if dlLen(head) != 2 || head != a || a.Next != c {
		t.Fatal("deleting a middle element did not relink the chain")
	}
	if head.Prev != c {
		t.Error("head.Prev must still be the tail")
	}

	head = dlDelete(head, c)
	if dlLen(head) != 1 || head.Prev != a {
		t.Fatal("deleting the tail did not fix up head.Prev")
	}

	if got := dlDelete(head, a); got != nil {
		t.Error("deleting the sole element must leave an empty chain")
	}
}

func TestChainDeleteHead(t *testing.T) {
	a, b, c := &Object{key: []byte("a")}, &Object{key: []byte("b")}, &Object{key: []byte("c")}
	head := dlAppend(dlAppend(dlAppend(nil, a), b), c)

	head = dlDelete(head, a)
	if head != b {
		t.Fatal("deleting the head must promote the next element")
	}
	if head.Prev != c {
		t.Error("promoted head must inherit the tail as its Prev")
	}
	if dlLen(head) != 2 {
		t.Errorf("chain length = %d, want 2", dlLen(head))
	}
}

func TestChainConcat(t *testing.T) {
	a, b := &Object{key: []byte("a")}, &Object{key: []byte("b")}
	c, d := &Object{key: []byte("c")}, &Object{key: []byte("d")}
	first := dlAppend(dlAppend(nil, a), b)
	second := dlAppend(dlAppend(nil, c), d)

	head := dlConcat(first, second)
	if head != a || dlLen(head) != 4 {
		t.Fatalf("concat produced a chain of %d, want 4", dlLen(head))
	}
	if head.Prev != d {
		t.Error("head.Prev must be the tail of the second chain")
	}
	if d.Next != nil {
		t.Error("tail.Next must be nil")
	}
}

func TestChainReplaceElem(t *testing.T) {
	a, b, c := &Object{key: []byte("a")}, &Object{key: []byte("b")}, &Object{key: []byte("c")}
	head := dlAppend(dlAppend(dlAppend(nil, a), b), c)

	x := &Object{key: []byte("x")}
	head = dlReplaceElem(head, b, x)
	if head != a || a.Next != x || x.Next != c || c.Prev != x {
		t.Fatal("replacing a middle element did not relink the chain")
	}
	if dlLen(head) != 3 {
		t.Errorf("chain length = %d, want 3", dlLen(head))
	}

	// Replacing the tail must move head.Prev with it.
	y := &Object{key: []byte("y")}
	head = dlReplaceElem(head, c, y)
	if head.Prev != y {
		t.Error("replacing the tail must update head.Prev")
	}
	if y.Next != nil {
		t.Error("new tail must have a nil Next")
	}
}

func TestChainReplaceSoleElement(t *testing.T) {
	a := &Object{key: []byte("a")}
	head := dlAppend(nil, a)

	x := &Object{key: []byte("x")}
	head = dlReplaceElem(head, a, x)
	if head != x {
		t.Fatal("replacing the sole element must return it as the head")
	}
	if x.Prev != x || x.Next != nil {
		t.Error("sole element must be its own Prev with a nil Next")
	}
}
