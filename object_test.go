package goucl

import (
	"testing"
)

// objKeys returns an object's keys in iteration (emission) order.
func objKeys(o *Object) []string {
	var got []string
	var it Iter
	for cur := Iterate(o, &it, true); cur != nil; cur = Iterate(o, &it, true) {
		got = append(got, string(cur.Key()))
	}
	return got
}

// insert is the common "build an object" shorthand used by these tests.
func insert(t *testing.T, top *Object, key string, elt *Object) {
	t.Helper()
	if !top.InsertKey(elt, []byte(key), true) {
		t.Fatalf("InsertKey(%q) returned false", key)
	}
}

func wantKeys(t *testing.T, o *Object, want ...string) {
	t.Helper()
	got := objKeys(o)
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

// --- Construction ---------------------------------------------------------

func TestConstruction(t *testing.T) {
	if got := NewObject().Type(); got != TypeNull {
		t.Errorf("NewObject type = %v, want TypeNull", got)
	}
	if got := TypedNew(TypeArray).Type(); got != TypeArray {
		t.Errorf("TypedNew(TypeArray) type = %v", got)
	}
	// An out-of-range type silently becomes null rather than being rejected.
	if got := TypedNew(Type(99)).Type(); got != TypeNull {
		t.Errorf("TypedNew(99) type = %v, want TypeNull", got)
	}

	o := NewFull(TypeInt, 7)
	if o.Type() != TypeInt || o.Priority() != 7 {
		t.Errorf("NewFull = (%v, %d), want (TypeInt, 7)", o.Type(), o.Priority())
	}
	if o.Prev != o || o.Next != nil {
		t.Error("a fresh object must be its own Prev with a nil Next")
	}

	// An object gets no hash until its first insert, which the iterator can
	// see (see TestIterateEmptyObjectIsAnException).
	if TypedNew(TypeObject).ov != nil {
		t.Error("TypedNew(TypeObject) must not allocate a hash")
	}
	// An array does get its backing storage, and a nil vector is a different
	// state that ArrayMerge is sensitive to.
	if TypedNew(TypeArray).av == nil {
		t.Error("TypedNew(TypeArray) must allocate the element vector")
	}

	ud := NewUserdata(nil, nil, "payload")
	if ud.Type() != TypeUserdata || ud.ud != "payload" {
		t.Error("NewUserdata did not store the payload")
	}
}

func TestScalarConstructors(t *testing.T) {
	if o := FromInt(-5); o.Type() != TypeInt || ToInt(o) != -5 {
		t.Errorf("FromInt = (%v, %d)", o.Type(), ToInt(o))
	}
	if o := FromDouble(1.5); o.Type() != TypeFloat || ToDouble(o) != 1.5 {
		t.Errorf("FromDouble = (%v, %v)", o.Type(), ToDouble(o))
	}
	if o := FromBool(true); o.Type() != TypeBoolean || !ToBoolean(o) {
		t.Errorf("FromBool = (%v, %v)", o.Type(), ToBoolean(o))
	}
	o := FromString("hi")
	if o.Type() != TypeString || ToString(o) != "hi" || o.length != 2 {
		t.Errorf("FromString = (%v, %q, len %d)", o.Type(), ToString(o), o.length)
	}
	if FromLString(nil) != nil {
		t.Error("FromLString(nil) must return nil, mirroring the C's NULL guard")
	}
}

func TestTypeString(t *testing.T) {
	tests := []struct {
		t    Type
		want string
	}{
		{TypeObject, "object"},
		{TypeArray, "array"},
		{TypeInt, "integer"},
		// Both float and time are called "number": the C has no name for a time.
		{TypeFloat, "number"},
		{TypeTime, "number"},
		{TypeString, "string"},
		{TypeBoolean, "boolean"},
		{TypeUserdata, "userdata"},
		{TypeNull, "null"},
	}
	for _, tc := range tests {
		if got := (&Object{ObjType: tc.t}).TypeString(); got != tc.want {
			t.Errorf("TypeString(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
	if got := (&Object{ObjType: Type(42)}).TypeString(); got != "unknown" {
		t.Errorf("TypeString(42) = %q, want \"unknown\"", got)
	}
}

func TestStringToType(t *testing.T) {
	tests := []struct {
		in   string
		want Type
		ok   bool
	}{
		{"object", TypeObject, true},
		{"ARRAY", TypeArray, true},
		{"Integer", TypeInt, true},
		// "number" is float; there is no spelling that produces a time.
		{"number", TypeFloat, true},
		{"string", TypeString, true},
		{"boolean", TypeBoolean, true},
		{"null", TypeNull, true},
		{"userdata", TypeUserdata, true},
		{"time", TypeNull, false},
		{"float", TypeNull, false},
		{"", TypeNull, false},
	}
	for _, tc := range tests {
		got, ok := StringToType(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("StringToType(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// --- FromStringCommon -----------------------------------------------------

func TestFromStringCommon(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		flags    StringFlag
		wantType Type
		wantStr  string // for strings
		wantInt  int64
		wantDbl  float64
		wantBool bool
		wantLen  uint32
	}{
		{"raw", "hello", StringRaw, TypeString, "hello", 0, 0, false, 5},
		{"trim", "  hello  ", StringTrim, TypeString, "hello", 0, 0, false, 5},
		{"trim all whitespace", " \t\n ", StringTrim, TypeString, "", 0, 0, false, 0},
		{"escape", "a\"b", StringEscape, TypeString, `a\"b`, 0, 0, false, 4},

		{"parse int", "10", StringParse, TypeInt, "", 10, 0, false, 2},
		{"parse float", "10.5", StringParse, TypeFloat, "", 0, 10.5, false, 4},
		{"parse time", "10s", StringParse, TypeTime, "", 0, 10, false, 3},
		{"parse boolean", "yes", StringParse, TypeBoolean, "", 0, 0, true, 3},
		{"unparseable stays a string", "10abc", StringParse, TypeString, "10abc", 0, 0, false, 5},

		// Without the double bit a fractional literal is rejected outright and
		// the value stays a string.
		{"int only rejects a float", "10.5", StringParseInt, TypeString, "10.5", 0, 0, false, 4},
		{"int only accepts an int", "10", StringParseInt, TypeInt, "", 10, 0, false, 2},

		// The boolean bit is what enables word matching; without it "yes" is
		// just a string.
		{"no boolean bit", "yes", StringParseNumber, TypeString, "yes", 0, 0, false, 3},

		// PARSE_BYTES is not part of PARSE, so on its own nothing is parsed.
		{"bytes alone parses nothing", "10", StringParseBytes, TypeString, "10", 0, 0, false, 2},
		{"bytes with int", "10k", StringParseInt | StringParseBytes, TypeInt, "", 10240, 0, false, 3},

		{"trim then parse", "  10  ", StringTrim | StringParse, TypeInt, "", 10, 0, false, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := FromStringCommon([]byte(tc.in), tc.flags)
			if o.Type() != tc.wantType {
				t.Fatalf("type = %v, want %v", o.Type(), tc.wantType)
			}
			if o.length != tc.wantLen {
				t.Errorf("length = %d, want %d", o.length, tc.wantLen)
			}
			switch tc.wantType {
			case TypeString:
				if got := ToString(o); got != tc.wantStr {
					t.Errorf("value = %q, want %q", got, tc.wantStr)
				}
			case TypeInt:
				if got := ToInt(o); got != tc.wantInt {
					t.Errorf("value = %d, want %d", got, tc.wantInt)
				}
			case TypeFloat, TypeTime:
				if got := ToDouble(o); got != tc.wantDbl {
					t.Errorf("value = %v, want %v", got, tc.wantDbl)
				}
			case TypeBoolean:
				if got := ToBoolean(o); got != tc.wantBool {
					t.Errorf("value = %v, want %v", got, tc.wantBool)
				}
			}
		})
	}
}

// A reclassified string keeps both the original string length and the cached
// original text, so ToStringForced returns what was parsed rather than a
// rendering of the value. Fixing this would be a silent divergence (checklist
// item 13).
func TestFromStringCommonKeepsTheOriginalText(t *testing.T) {
	o := FromStringCommon([]byte("0x10"), StringParse)
	if o.Type() != TypeInt || ToInt(o) != 16 {
		t.Fatalf("expected TypeInt 16, got %v %d", o.Type(), ToInt(o))
	}
	if o.length != 4 {
		t.Errorf("length = %d, want 4 (the original string length)", o.length)
	}
	if got := ToStringForced(o); got != "0x10" {
		t.Errorf("ToStringForced = %q, want the cached original text %q", got, "0x10")
	}

	// Same for a boolean word that does not spell its own value.
	b := FromStringCommon([]byte("yes"), StringParse)
	if !ToBoolean(b) {
		t.Fatal("expected a true boolean")
	}
	if got := ToStringForced(b); got != "yes" {
		t.Errorf("ToStringForced = %q, want %q", got, "yes")
	}
}

// --- Insertion ------------------------------------------------------------

func TestInsertKey(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "a", FromInt(1))
	insert(t, top, "b", FromInt(2))

	if top.length != 2 {
		t.Errorf("length = %d, want 2", top.length)
	}
	wantKeys(t, top, "a", "b")
	if ToInt(top.Lookup("a")) != 1 {
		t.Error("lookup of a returned the wrong value")
	}
}

// A null is converted into an object in place; nothing else is.
func TestInsertKeyConvertsNull(t *testing.T) {
	top := NewObject()
	if !top.InsertKey(FromInt(1), []byte("a"), true) {
		t.Fatal("InsertKey into a null object must succeed")
	}
	if top.Type() != TypeObject {
		t.Errorf("type = %v, want TypeObject", top.Type())
	}

	for _, ty := range []Type{TypeInt, TypeString, TypeArray, TypeBoolean} {
		if TypedNew(ty).InsertKey(FromInt(1), []byte("a"), true) {
			t.Errorf("InsertKey into a %v must fail", ty)
		}
	}
}

func TestInsertKeyGuards(t *testing.T) {
	top := TypedNew(TypeObject)
	if top.InsertKey(nil, []byte("a"), true) {
		t.Error("a nil element must be rejected")
	}
	if top.InsertKey(FromInt(1), nil, true) {
		t.Error("a nil key must be rejected")
	}
	var nilTop *Object
	if nilTop.InsertKey(FromInt(1), []byte("a"), true) {
		t.Error("a nil object must be rejected")
	}
}

// A duplicate key builds an implicit array rather than replacing, and does not
// bump length -- only a genuinely new key does.
func TestInsertKeyDuplicateBuildsImplicitArray(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "k", FromInt(1))
	insert(t, top, "k", FromInt(2))
	insert(t, top, "k", FromInt(3))

	if top.length != 1 {
		t.Errorf("length = %d, want 1: only a new key increments it", top.length)
	}
	head := top.Lookup("k")
	if dlLen(head) != 3 {
		t.Fatalf("chain length = %d, want 3", dlLen(head))
	}
	if ToInt(head) != 1 || ToInt(head.Next) != 2 || ToInt(head.Next.Next) != 3 {
		t.Error("chain values are out of order")
	}
	// Iteration with expandValues visits chain heads only.
	wantKeys(t, top, "k")
}

// The whole of the inserted element's chain is spliced on, not just the head.
func TestInsertKeyConcatsWholeChain(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "k", FromInt(1))

	second := FromInt(2)
	dlAppend(second, FromInt(3))
	insert(t, top, "k", second)

	if got := dlLen(top.Lookup("k")); got != 3 {
		t.Errorf("chain length = %d, want 3", got)
	}
}

// A key byte in the UCL-unsafe class marks the element for escaping on output.
func TestInsertKeySetsEscapeFlag(t *testing.T) {
	top := TypedNew(TypeObject)

	plain := FromInt(1)
	insert(t, top, "plain", plain)
	if plain.Flags()&FlagNeedKeyEscape != 0 {
		t.Error("a safe key must not be flagged for escaping")
	}

	spaced := FromInt(2)
	insert(t, top, "needs escape", spaced)
	if spaced.Flags()&FlagNeedKeyEscape == 0 {
		t.Error("a key containing a space must be flagged for escaping")
	}
}

// Replacing keeps the key where it is in the emission order. Delete+insert
// would move it to the tail and change emitted output (checklist item 3).
func TestReplaceKeyKeepsPosition(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "a", FromInt(1))
	insert(t, top, "b", FromInt(2))
	insert(t, top, "c", FromInt(3))

	if !top.ReplaceKey(FromInt(99), []byte("a"), true) {
		t.Fatal("ReplaceKey returned false")
	}
	wantKeys(t, top, "a", "b", "c")
	if ToInt(top.Lookup("a")) != 99 {
		t.Error("ReplaceKey did not store the new value")
	}
	if top.length != 3 {
		t.Errorf("length = %d, want 3: replacing must not change it", top.length)
	}

	// Replacing an absent key is an ordinary insert, and does bump length.
	if !top.ReplaceKey(FromInt(4), []byte("d"), true) {
		t.Fatal("ReplaceKey of a new key returned false")
	}
	wantKeys(t, top, "a", "b", "c", "d")
	if top.length != 4 {
		t.Errorf("length = %d, want 4", top.length)
	}
}

func TestInsertKeyMerged(t *testing.T) {
	t.Run("two scalars form a chain", func(t *testing.T) {
		top := TypedNew(TypeObject)
		insert(t, top, "k", FromInt(1))
		if !top.InsertKeyMerged(FromInt(2), []byte("k"), true) {
			t.Fatal("InsertKeyMerged returned false")
		}
		if got := dlLen(top.Lookup("k")); got != 2 {
			t.Errorf("chain length = %d, want 2", got)
		}
	})

	t.Run("scalar found and object element moves the key to the tail", func(t *testing.T) {
		top := TypedNew(TypeObject)
		insert(t, top, "a", FromInt(1))
		insert(t, top, "b", FromInt(2))

		elt := TypedNew(TypeObject)
		if !top.InsertKeyMerged(elt, []byte("a"), true) {
			t.Fatal("InsertKeyMerged returned false")
		}
		// The C implements this as delete + insert, so unlike every other
		// merge path the key moves to the end of the emission order.
		wantKeys(t, top, "b", "a")
		if got := top.Lookup("a"); got != elt {
			t.Fatal("the object element should now be stored under the key")
		}
		if ToInt(elt.Lookup("a")) != 1 {
			t.Error("the old scalar should have been absorbed into the new object")
		}
	})

	t.Run("object found and scalar element", func(t *testing.T) {
		top := TypedNew(TypeObject)
		inner := TypedNew(TypeObject)
		insert(t, inner, "x", FromInt(1))
		insert(t, top, "a", inner)
		insert(t, top, "b", FromInt(0))

		if !top.InsertKeyMerged(FromInt(5), []byte("a"), true) {
			t.Fatal("InsertKeyMerged returned false")
		}
		wantKeys(t, top, "a", "b") // no reordering on this path
		if ToInt(inner.Lookup("a")) != 5 {
			t.Error("the scalar should have been inserted into the found object")
		}
	})

	t.Run("two objects are mixed", func(t *testing.T) {
		top := TypedNew(TypeObject)
		found := TypedNew(TypeObject)
		insert(t, found, "x", FromInt(1))
		insert(t, top, "a", found)

		elt := TypedNew(TypeObject)
		insert(t, elt, "y", FromInt(2))
		if !top.InsertKeyMerged(elt, []byte("a"), true) {
			t.Fatal("InsertKeyMerged returned false")
		}
		if top.Lookup("a") != found {
			t.Error("the found object must stay in place")
		}
		wantKeys(t, found, "x", "y")
	})
}

// --- Deletion -------------------------------------------------------------

func TestDeleteAndPopKey(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "a", FromInt(1))
	insert(t, top, "b", FromInt(2))
	insert(t, top, "c", FromInt(3))

	if !top.DeleteKey([]byte("b")) {
		t.Fatal("DeleteKey returned false")
	}
	wantKeys(t, top, "a", "c")
	if top.length != 2 {
		t.Errorf("length = %d, want 2", top.length)
	}
	if top.DeleteKey([]byte("b")) {
		t.Error("deleting an absent key must return false")
	}
	if top.DeleteKey(nil) {
		t.Error("deleting a nil key must return false")
	}

	// Re-inserting a deleted key puts it at the tail, not back where it was.
	insert(t, top, "b", FromInt(9))
	wantKeys(t, top, "a", "c", "b")

	popped := top.PopKey([]byte("a"))
	if popped == nil || ToInt(popped) != 1 {
		t.Fatal("PopKey did not return the value")
	}
	wantKeys(t, top, "c", "b")
	if top.PopKey([]byte("zzz")) != nil {
		t.Error("popping an absent key must return nil")
	}
}

// Delete and pop remove the whole implicit array; there is no API for removing
// one element of it. A popped head still carries its siblings.
func TestPopKeyKeepsTheChain(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "k", FromInt(1))
	insert(t, top, "k", FromInt(2))

	popped := top.PopKey([]byte("k"))
	if dlLen(popped) != 2 {
		t.Errorf("popped chain length = %d, want 2", dlLen(popped))
	}
	if top.Lookup("k") != nil {
		t.Error("the key must be gone from the object")
	}
}

// --- Lookup ---------------------------------------------------------------

func TestLookup(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "a", FromInt(1))

	if top.Lookup("a") == nil {
		t.Error("Lookup missed a present key")
	}
	if top.Lookup("A") != nil {
		t.Error("the object API always builds a case-sensitive hash")
	}
	if top.LookupLen(nil) != nil {
		t.Error("a nil key must return nil")
	}
	// There is no strlen fallback here: an empty key looks up the empty key.
	if top.LookupLen([]byte{}) != nil {
		t.Error("an empty key must look up the empty key, not fall back to strlen")
	}

	var nilObj *Object
	if nilObj.Lookup("a") != nil {
		t.Error("Lookup on a nil object must return nil")
	}
	if FromInt(1).Lookup("a") != nil {
		t.Error("Lookup on a non-object must return nil")
	}
}

func TestLookupAny(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "second", FromInt(2))

	if got := top.LookupAny("first", "second", "third"); ToInt(got) != 2 {
		t.Error("LookupAny did not return the first present key")
	}
	if top.LookupAny("nope", "nada") != nil {
		t.Error("LookupAny must return nil when nothing matches")
	}
	if top.LookupAny() != nil {
		t.Error("LookupAny with no keys must return nil")
	}
}

func TestLookupPath(t *testing.T) {
	root := TypedNew(TypeObject)
	mid := TypedNew(TypeObject)
	leaf := TypedNew(TypeObject)
	insert(t, leaf, "c", FromInt(42))
	insert(t, mid, "b", leaf)
	insert(t, root, "a", mid)

	arr := TypedNew(TypeArray)
	ArrayAppend(arr, FromInt(10))
	ArrayAppend(arr, FromInt(20))
	insert(t, root, "arr", arr)

	tests := []struct {
		path string
		want int64
		nil_ bool
	}{
		{"a.b.c", 42, false},
		{".a.b.c", 42, false}, // leading separators are skipped
		{"a..b.c", 42, false}, // so are empty segments
		{"a.b.c.", 42, false}, // and a trailing one
		{"arr.1", 20, false},  // numeric indexing inside an array
		{"arr.0", 10, false},
		{"arr.5", 0, true},  // out of range
		{"arr.x", 0, true},  // not an index
		{"arr.1x", 0, true}, // strtoul must stop at the separator or the end
		{"a.b.missing", 0, true},
		{"", 0, true}, // an empty path yields nil, not the root
		{"...", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := root.LookupPath(tc.path)
			if tc.nil_ {
				if got != nil {
					t.Errorf("LookupPath(%q) = %v, want nil", tc.path, got.Type())
				}
				return
			}
			if got == nil {
				t.Fatalf("LookupPath(%q) = nil", tc.path)
			}
			if got.Type() == TypeInt && ToInt(got) != tc.want {
				t.Errorf("LookupPath(%q) = %d, want %d", tc.path, ToInt(got), tc.want)
			}
		})
	}

	// A different separator, and a key containing the default one.
	flat := TypedNew(TypeObject)
	insert(t, flat, "a.b", FromInt(7))
	if got := flat.LookupPathChar("a.b", '/'); ToInt(got) != 7 {
		t.Error("LookupPathChar with a different separator failed")
	}
}

// --- Merge ----------------------------------------------------------------

func TestMergeObjects(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "keep", FromInt(1))
	insert(t, top, "scalar", FromInt(2))

	elt := TypedNew(TypeObject)
	insert(t, elt, "scalar", FromInt(20)) // replaces, keeping position
	insert(t, elt, "fresh", FromInt(30))  // appended at the tail

	if !top.Merge(elt, false) {
		t.Fatal("Merge returned false")
	}
	wantKeys(t, top, "keep", "scalar", "fresh")
	if ToInt(top.Lookup("scalar")) != 20 {
		t.Error("a scalar collision must be replaced, not chained")
	}
	if dlLen(top.Lookup("scalar")) != 1 {
		t.Error("Merge must never build an implicit array")
	}
	if top.length != 3 {
		t.Errorf("length = %d, want 3", top.length)
	}
}

func TestMergeRecursesIntoContainers(t *testing.T) {
	inner := TypedNew(TypeObject)
	insert(t, inner, "x", FromInt(1))
	top := TypedNew(TypeObject)
	insert(t, top, "sub", inner)

	otherInner := TypedNew(TypeObject)
	insert(t, otherInner, "y", FromInt(2))
	elt := TypedNew(TypeObject)
	insert(t, elt, "sub", otherInner)

	if !top.Merge(elt, false) {
		t.Fatal("Merge returned false")
	}
	wantKeys(t, inner, "x", "y")
}

func TestMergeArrays(t *testing.T) {
	top := TypedNew(TypeArray)
	ArrayAppend(top, FromInt(1))

	other := TypedNew(TypeArray)
	ArrayAppend(other, FromInt(2))

	if !top.Merge(other, false) {
		t.Fatal("Merge of two arrays returned false")
	}
	if ArraySize(top) != 2 {
		t.Errorf("size = %d, want 2", ArraySize(top))
	}

	// A non-array merged into an array is simply appended.
	if !top.Merge(FromInt(3), false) {
		t.Fatal("Merge of a scalar into an array returned false")
	}
	if ArraySize(top) != 3 {
		t.Errorf("size = %d, want 3", ArraySize(top))
	}
}

func TestMergeGuards(t *testing.T) {
	if TypedNew(TypeObject).Merge(nil, false) {
		t.Error("merging nil must fail")
	}
	if FromInt(1).Merge(FromInt(2), false) {
		t.Error("merging into a scalar must fail")
	}
}

// With deep false the trees genuinely alias afterwards; the port must not
// helpfully copy (checklist item: aliasing vs copying).
func TestMergeAliasesWhenNotCopying(t *testing.T) {
	child := FromInt(1)
	elt := TypedNew(TypeObject)
	insert(t, elt, "k", child)

	top := TypedNew(TypeObject)
	if !top.Merge(elt, false) {
		t.Fatal("Merge returned false")
	}
	if top.Lookup("k") != child {
		t.Error("a non-copying merge must share the child object")
	}

	deep := TypedNew(TypeObject)
	if !deep.Merge(elt, true) {
		t.Fatal("Merge returned false")
	}
	if deep.Lookup("k") == child {
		t.Error("a copying merge must not share the child object")
	}
}

// --- Arrays ---------------------------------------------------------------

func TestArrayBasics(t *testing.T) {
	ar := TypedNew(TypeArray)
	a, b, c := FromInt(1), FromInt(2), FromInt(3)

	if !ArrayAppend(ar, a) || !ArrayAppend(ar, b) {
		t.Fatal("ArrayAppend returned false")
	}
	if !ArrayPrepend(ar, c) {
		t.Fatal("ArrayPrepend returned false")
	}
	if ArraySize(ar) != 3 || ar.length != 3 {
		t.Fatalf("size = %d, length = %d, want 3 and 3", ArraySize(ar), ar.length)
	}
	if ArrayHead(ar) != c || ArrayTail(ar) != b {
		t.Error("head/tail are wrong after the prepend")
	}
	if ArrayFindIndex(ar, 1) != a {
		t.Error("ArrayFindIndex returned the wrong element")
	}
	if ArrayFindIndex(ar, 3) != nil {
		t.Error("an out-of-range index must return nil")
	}
	if got := ArrayIndexOf(ar, b); got != 2 {
		t.Errorf("ArrayIndexOf = %d, want 2", got)
	}
	if got := ArrayIndexOf(ar, FromInt(99)); got != ArrayIndexNone {
		t.Errorf("ArrayIndexOf of an absent element = %d, want ArrayIndexNone", got)
	}

	if got := ArrayDelete(ar, a); got != a {
		t.Error("ArrayDelete did not return the element")
	}
	if ArraySize(ar) != 2 || ar.length != 2 {
		t.Error("ArrayDelete did not update the counts")
	}
	if ArrayDelete(ar, a) != nil {
		t.Error("deleting an absent element must return nil")
	}

	if got := ArrayPopFirst(ar); got != c {
		t.Error("ArrayPopFirst returned the wrong element")
	}
	if got := ArrayPopLast(ar); got != b {
		t.Error("ArrayPopLast returned the wrong element")
	}
	if ArrayPopLast(ar) != nil || ArrayPopFirst(ar) != nil {
		t.Error("popping an empty array must return nil")
	}
}

func TestArrayGuards(t *testing.T) {
	notArray := FromInt(1)
	if ArrayAppend(notArray, FromInt(2)) {
		t.Error("appending to a non-array must fail")
	}
	if ArrayAppend(TypedNew(TypeArray), nil) {
		t.Error("appending nil must fail")
	}
	if ArraySize(notArray) != 0 || ArrayHead(notArray) != nil || ArrayTail(notArray) != nil {
		t.Error("array accessors on a non-array must be empty")
	}
	if ArrayIndexOf(notArray, nil) != ArrayIndexNone {
		t.Error("ArrayIndexOf on a non-array must report not-found")
	}
}

func TestArrayReplaceIndex(t *testing.T) {
	ar := TypedNew(TypeArray)
	a, b := FromInt(1), FromInt(2)
	ArrayAppend(ar, a)

	if got := ArrayReplaceIndex(ar, b, 0); got != a {
		t.Error("ArrayReplaceIndex must hand back the previous occupant")
	}
	if ArrayFindIndex(ar, 0) != b {
		t.Error("the new element was not stored")
	}
	if ar.length != 1 {
		t.Errorf("length = %d, want 1: replacing must not change it", ar.length)
	}
	if ArrayReplaceIndex(ar, a, 9) != nil {
		t.Error("an out-of-range replace must return nil")
	}

	// A nil element leaves a hole that iteration skips.
	ArrayReplaceIndex(ar, nil, 0)
	var it Iter
	if Iterate(ar, &it, true) != nil {
		t.Error("iteration must skip nil slots")
	}
}

// The counting loop in ucl_array_merge starts at the source's length instead
// of the destination's old length, so length and ArraySize disagree unless the
// two arrays were the same size. Reproduced deliberately (checklist item 12).
func TestArrayMergeLengthBug(t *testing.T) {
	top := TypedNew(TypeArray)
	for i := 0; i < 3; i++ {
		ArrayAppend(top, FromInt(int64(i)))
	}
	other := TypedNew(TypeArray)
	for i := 0; i < 2; i++ {
		ArrayAppend(other, FromInt(int64(i)))
	}

	if !ArrayMerge(top, other, false) {
		t.Fatal("ArrayMerge returned false")
	}
	if got := ArraySize(top); got != 5 {
		t.Errorf("ArraySize = %d, want 5", got)
	}
	// The loop runs over [n2, n1+n2) == [2, 5), i.e. three times.
	if top.length != 6 {
		t.Errorf("length = %d, want 6 (the upstream miscount)", top.length)
	}

	// Equal sizes are the one case where the bug is invisible.
	x, y := TypedNew(TypeArray), TypedNew(TypeArray)
	ArrayAppend(x, FromInt(1))
	ArrayAppend(y, FromInt(2))
	ArrayMerge(x, y, false)
	if uint32(ArraySize(x)) != x.length {
		t.Errorf("equal-size merge: size %d != length %d", ArraySize(x), x.length)
	}
}

func TestArrayMergeGuards(t *testing.T) {
	if ArrayMerge(TypedNew(TypeArray), FromInt(1), false) {
		t.Error("merging a non-array must fail")
	}
	if ArrayMerge(FromInt(1), TypedNew(TypeArray), false) {
		t.Error("merging into a non-array must fail")
	}
	if ArrayMerge(TypedNew(TypeArray), nil, false) {
		t.Error("merging nil must fail")
	}
}

// A non-copying merge shares element pointers with the source.
func TestArrayMergeAliases(t *testing.T) {
	top := TypedNew(TypeArray)
	ArrayAppend(top, FromInt(0))
	other := TypedNew(TypeArray)
	shared := FromInt(1)
	ArrayAppend(other, shared)

	ArrayMerge(top, other, false)
	if ArrayFindIndex(top, 1) != shared {
		t.Error("a non-copying array merge must share elements")
	}
}

func TestEltAppend(t *testing.T) {
	a, b := FromInt(1), FromInt(2)
	head := EltAppend(nil, a)
	if head != a || a.Prev != a {
		t.Fatal("EltAppend with a nil head must make the element its own head")
	}
	head = EltAppend(head, b)
	if head != a || a.Next != b || head.Prev != b {
		t.Error("EltAppend did not link the element at the tail")
	}
}

func TestReserve(t *testing.T) {
	ar := TypedNew(TypeArray)
	if !Reserve(ar, 64) || cap(ar.av) < 64 {
		t.Error("Reserve did not grow the array's capacity")
	}
	// Non-arrays are a silent no-op that still reports success.
	if !Reserve(FromInt(1), 10) {
		t.Error("Reserve on a scalar must still report success")
	}
}

// --- Iteration ------------------------------------------------------------

func TestIterateObject(t *testing.T) {
	top := TypedNew(TypeObject)
	insert(t, top, "a", FromInt(1))
	insert(t, top, "b", FromInt(2))
	insert(t, top, "b", FromInt(3)) // an implicit-array sibling

	// expandValues visits chain heads only: the sibling is not returned.
	var it Iter
	var got []int64
	for cur := Iterate(top, &it, true); cur != nil; cur = Iterate(top, &it, true) {
		got = append(got, ToInt(cur))
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("values = %v, want [1 2]", got)
	}

	// Exhausting the iterator resets it, exactly as the C frees the cursor and
	// stores NULL back, so a further call starts over.
	if cur := Iterate(top, &it, false); cur != top {
		t.Error("a fresh linear walk must start with the object itself")
	}
}

func TestIterateArraySkipsHoles(t *testing.T) {
	ar := TypedNew(TypeArray)
	ArrayAppend(ar, FromInt(1))
	ArrayAppend(ar, FromInt(2))
	ArrayAppend(ar, FromInt(3))
	ArrayReplaceIndex(ar, nil, 1)

	var it Iter
	var got []int64
	for cur := Iterate(ar, &it, true); cur != nil; cur = Iterate(ar, &it, true) {
		got = append(got, ToInt(cur))
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("values = %v, want [1 3]", got)
	}
}

// Without expandValues the walk is over the object's own duplicate chain, and
// it starts with the object itself.
func TestIterateImplicitChain(t *testing.T) {
	head := FromInt(1)
	dlAppend(head, FromInt(2))
	dlAppend(head, FromInt(3))

	var it Iter
	var got []int64
	for cur := Iterate(head, &it, false); cur != nil; cur = Iterate(head, &it, false) {
		got = append(got, ToInt(cur))
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("values = %v, want [1 2 3]", got)
	}

	// A scalar falls through to the linear walk even with expandValues set.
	var it2 Iter
	if cur := Iterate(FromInt(7), &it2, true); ToInt(cur) != 7 {
		t.Error("a scalar must yield itself")
	}
}

func TestIterateGuards(t *testing.T) {
	var it Iter
	if Iterate(nil, &it, true) != nil {
		t.Error("iterating nil must return nil")
	}
	if Iterate(FromInt(1), nil, true) != nil {
		t.Error("a nil iterator must return nil")
	}
	IterateEnd(nil, nil) // must not panic
}

// A TypeObject that has never had anything inserted has no hash at all, and
// the C reports EINVAL for it -- which the safe iterator turns into its
// exception state.
func TestIterateEmptyObjectIsAnException(t *testing.T) {
	empty := TypedNew(TypeObject)

	var it Iter
	ern := -1
	if Iterate2 := IterateWithError(empty, &it, true, &ern); Iterate2 != nil {
		t.Error("iterating a hashless object must yield nothing")
	}
	if ern != iterErrInval {
		t.Errorf("error = %d, want EINVAL (%d)", ern, iterErrInval)
	}

	sit := IterateNew(empty)
	if IterateSafe(sit, true) != nil {
		t.Error("the safe iterator must yield nothing")
	}
	if !IterChkExcpn(sit) {
		t.Error("the safe iterator must report an exception")
	}

	// One insert gives it a hash, and the exception goes away.
	insert(t, empty, "a", FromInt(1))
	sit = IterateNew(empty)
	if IterateSafe(sit, true) == nil {
		t.Error("the safe iterator must now yield the value")
	}
	if IterChkExcpn(sit) {
		t.Error("no exception is expected now")
	}
	IterateFree(sit)
}

// The safe iterator walks the container and then continues along the implicit
// chain of the object it was created for.
func TestIterateSafeSpansTheChain(t *testing.T) {
	first := TypedNew(TypeObject)
	insert(t, first, "a", FromInt(1))
	second := TypedNew(TypeObject)
	insert(t, second, "b", FromInt(2))
	dlAppend(first, second)

	it := IterateNew(first)
	var got []int64
	for cur := IterateSafe(it, true); cur != nil; cur = IterateSafe(it, true) {
		got = append(got, ToInt(cur))
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("values = %v, want [1 2]", got)
	}

	IterateReset(it, first)
	if cur := IterateSafe(it, true); ToInt(cur) != 1 {
		t.Error("IterateReset did not rewind the iterator")
	}
}

// A container found on the implicit chain is expanded rather than returned,
// whatever the iterate type: the chain is dispatched on before the type flags
// are consulted, so the C's "flatten containers" branch in the scalar case can
// never fire (see the note on IterateFull).
func TestIterateFullExpandsContainersOnTheChain(t *testing.T) {
	head := FromInt(1)
	nested := TypedNew(TypeObject)
	insert(t, nested, "k", FromInt(2))
	dlAppend(head, nested)

	for _, typ := range []IterateType{IterateBoth, IterateImplicit} {
		it := IterateNew(head)
		var types []Type
		for cur := IterateFull(it, typ); cur != nil; cur = IterateFull(it, typ) {
			types = append(types, cur.Type())
		}
		// The nested object itself is never returned: only its child.
		if len(types) != 2 || types[0] != TypeInt || types[1] != TypeInt {
			t.Errorf("typ %d: types = %v, want two ints", typ, types)
		}
	}
}

// Without IterateImplicit the iterator stops at the end of the first
// container instead of moving on down the chain.
func TestIterateFullExplicitStopsAtTheFirstLink(t *testing.T) {
	first := TypedNew(TypeObject)
	insert(t, first, "a", FromInt(1))
	second := TypedNew(TypeObject)
	insert(t, second, "b", FromInt(2))
	dlAppend(first, second)

	it := IterateNew(first)
	var got []int64
	for cur := IterateFull(it, IterateExplicit); cur != nil; cur = IterateFull(it, IterateExplicit) {
		got = append(got, ToInt(cur))
	}
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("values = %v, want [1]", got)
	}
}

// --- Ref and copy ---------------------------------------------------------

// Ref is a no-op for ordinary objects but deep-copies an ephemeral one. Losing
// that branch is checklist item 14.
func TestRefEphemeralDeepCopies(t *testing.T) {
	plain := FromInt(1)
	if Ref(plain) != plain {
		t.Error("Ref of an ordinary object must return the same object")
	}
	if Ref(nil) != nil {
		t.Error("Ref(nil) must be nil")
	}

	eph := FromInt(1)
	eph.flags |= FlagEphemeral
	got := Ref(eph)
	if got == eph {
		t.Fatal("Ref of an ephemeral object must return a deep copy")
	}
	if got.Flags()&FlagEphemeral != 0 {
		t.Error("the copy must not itself be ephemeral")
	}
	got.setInt(99)
	if ToInt(eph) != 1 {
		t.Error("mutating the copy must not affect the source")
	}
}

func TestCopy(t *testing.T) {
	src := TypedNew(TypeObject)
	insert(t, src, "a", FromInt(1))
	sub := TypedNew(TypeArray)
	ArrayAppend(sub, FromString("x"))
	insert(t, src, "b", sub)

	cp := Copy(src)
	if cp == src {
		t.Fatal("Copy must return a new object")
	}
	wantKeys(t, cp, "a", "b")
	if cp.Lookup("a") == src.Lookup("a") {
		t.Error("children must be copied, not shared")
	}
	if ToString(ArrayHead(cp.Lookup("b"))) != "x" {
		t.Error("the nested array was not copied faithfully")
	}

	// Mutating the copy leaves the source alone.
	cp.Lookup("a").setInt(99)
	if ToInt(src.Lookup("a")) != 1 {
		t.Error("the copy still aliases the source")
	}

	if Copy(nil) != nil {
		t.Error("Copy(nil) must be nil")
	}
}

// A copy is rebuilt with InsertKey, which only counts new keys -- so a copy of
// an object holding an implicit array has a smaller length than the original
// once the parser's per-value counting is in play.
func TestCopyPreservesImplicitArrays(t *testing.T) {
	src := TypedNew(TypeObject)
	insert(t, src, "k", FromInt(1))
	insert(t, src, "k", FromInt(2))
	// Simulate the parser, which counts values rather than keys.
	src.length = 2

	cp := Copy(src)
	if got := dlLen(cp.Lookup("k")); got != 2 {
		t.Errorf("copied chain length = %d, want 2", got)
	}
	if cp.length != 1 {
		t.Errorf("copied length = %d, want 1: InsertKey only counts new keys", cp.length)
	}
}

// Unref and Free are documented no-ops; the contract survives so ported code
// reads the same.
func TestUnrefAndFreeAreNoOps(t *testing.T) {
	o := FromInt(1)
	Unref(o)
	Free(o)
	Unref(nil)
	Free(nil)
	if ToInt(o) != 1 {
		t.Error("Unref/Free must not disturb the object")
	}
}
