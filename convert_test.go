package goucl

import (
	"testing"
)

// The conversion matrix is deliberately narrow: there is no implicit
// string-to-number or number-to-string conversion anywhere in the to* family,
// and a boolean is not a number.
func TestConversionMatrix(t *testing.T) {
	str := FromString("42")
	binary := FromString("bin")
	binary.flags |= FlagBinary
	tm := TypedNew(TypeTime)
	tm.setTime(1.5)

	tests := []struct {
		name string
		obj  *Object

		wantDouble   float64
		wantDoubleOK bool
		wantInt      int64
		wantIntOK    bool
		wantBool     bool
		wantBoolOK   bool
		wantStr      string
		wantStrOK    bool
	}{
		{
			name: "int", obj: FromInt(7),
			wantDouble: 7, wantDoubleOK: true,
			wantInt: 7, wantIntOK: true,
		},
		{
			name: "float truncates toward zero", obj: FromDouble(-1.9),
			wantDouble: -1.9, wantDoubleOK: true,
			wantInt: -1, wantIntOK: true,
		},
		{
			name: "time", obj: tm,
			wantDouble: 1.5, wantDoubleOK: true,
			wantInt: 1, wantIntOK: true,
		},
		{
			// A boolean is not a number, in either direction.
			name: "boolean", obj: FromBool(true),
			wantBool: true, wantBoolOK: true,
		},
		{
			// A numeric string is still just a string.
			name: "string", obj: str,
			wantStr: "42", wantStrOK: true,
		},
		{
			name: "null", obj: TypedNew(TypeNull),
		},
		{
			name: "nil", obj: nil,
		},
		{
			// The wart: a binary string reports success but yields nothing.
			name: "binary string", obj: binary,
			wantStr: "", wantStrOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if v, ok := ToDoubleSafe(tc.obj); ok != tc.wantDoubleOK || v != tc.wantDouble {
				t.Errorf("ToDoubleSafe = (%v, %v), want (%v, %v)", v, ok, tc.wantDouble, tc.wantDoubleOK)
			}
			if v, ok := ToIntSafe(tc.obj); ok != tc.wantIntOK || v != tc.wantInt {
				t.Errorf("ToIntSafe = (%v, %v), want (%v, %v)", v, ok, tc.wantInt, tc.wantIntOK)
			}
			if v, ok := ToBooleanSafe(tc.obj); ok != tc.wantBoolOK || v != tc.wantBool {
				t.Errorf("ToBooleanSafe = (%v, %v), want (%v, %v)", v, ok, tc.wantBool, tc.wantBoolOK)
			}
			if v, ok := ToStringSafe(tc.obj); ok != tc.wantStrOK || v != tc.wantStr {
				t.Errorf("ToStringSafe = (%q, %v), want (%q, %v)", v, ok, tc.wantStr, tc.wantStrOK)
			}

			// The unsafe wrappers return the zero value on refusal, which is
			// indistinguishable from a real zero.
			if got := ToDouble(tc.obj); got != tc.wantDouble {
				t.Errorf("ToDouble = %v, want %v", got, tc.wantDouble)
			}
			if got := ToInt(tc.obj); got != tc.wantInt {
				t.Errorf("ToInt = %v, want %v", got, tc.wantInt)
			}
			if got := ToBoolean(tc.obj); got != tc.wantBool {
				t.Errorf("ToBoolean = %v, want %v", got, tc.wantBool)
			}
			if got := ToString(tc.obj); got != tc.wantStr {
				t.Errorf("ToString = %q, want %q", got, tc.wantStr)
			}
		})
	}
}

// An integer 0 or 1, and the strings "true"/"false", are not booleans: only a
// real TypeBoolean converts.
func TestToBooleanRejectsLookalikes(t *testing.T) {
	for _, o := range []*Object{FromInt(1), FromInt(0), FromString("true"), FromString("false")} {
		if _, ok := ToBooleanSafe(o); ok {
			t.Errorf("%v must not convert to a boolean", o.Type())
		}
	}
}

func TestToLString(t *testing.T) {
	o := FromString("abc")
	b, n, ok := ToLStringSafe(o)
	if !ok || string(b) != "abc" || n != 3 {
		t.Errorf("ToLStringSafe = (%q, %d, %v)", b, n, ok)
	}

	// Unlike ToStringSafe, this accepts binary strings.
	bin := FromLString([]byte("a\x00b"))
	bin.flags |= FlagBinary
	b, n, ok = ToLStringSafe(bin)
	if !ok || string(b) != "a\x00b" || n != 3 {
		t.Errorf("ToLStringSafe(binary) = (%q, %d, %v)", b, n, ok)
	}

	if _, _, ok := ToLStringSafe(FromInt(1)); ok {
		t.Error("ToLStringSafe must reject a non-string")
	}
	if b, n := ToLString(FromInt(1)); b != nil || n != 0 {
		t.Errorf("ToLString of a non-string = (%q, %d), want (nil, 0)", b, n)
	}
}

// ToStringForced renders any type, and rendering a non-string destroys length
// by overwriting it with the length of the rendering.
func TestToStringForced(t *testing.T) {
	tests := []struct {
		name string
		obj  *Object
		want string
	}{
		{"int", FromInt(-12), "-12"},
		{"integral float", FromDouble(1), "1.0"},
		{"fractional float", FromDouble(0.5), "0.500000"},
		{"true", FromBool(true), "true"},
		{"false", FromBool(false), "false"},
		{"null", TypedNew(TypeNull), "null"},
		{"object", TypedNew(TypeObject), "object"},
		{"array", TypedNew(TypeArray), "array"},
		{"userdata", TypedNew(TypeUserdata), "userdata"},
		{"string", FromString("hi"), "hi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToStringForced(tc.obj); got != tc.want {
				t.Fatalf("ToStringForced = %q, want %q", got, tc.want)
			}
			if tc.obj.Type() != TypeString && tc.obj.length != uint32(len(tc.want)) {
				t.Errorf("length = %d, want %d: rendering a non-string overwrites it",
					tc.obj.length, len(tc.want))
			}
		})
	}

	// A binary string refuses, and a nil object yields the empty string where
	// the C would crash.
	bin := FromString("x")
	bin.flags |= FlagBinary
	if got := ToStringForced(bin); got != "" {
		t.Errorf("ToStringForced(binary) = %q, want \"\"", got)
	}
	if got := ToStringForced(nil); got != "" {
		t.Errorf("ToStringForced(nil) = %q, want \"\"", got)
	}
}

// The rendering is cached, so a value that changes after it was first rendered
// keeps handing out the stale text.
func TestValueTrashIsCached(t *testing.T) {
	o := FromInt(1)
	if got := ToStringForced(o); got != "1" {
		t.Fatalf("first render = %q", got)
	}
	o.setInt(2)
	if got := ToStringForced(o); got != "1" {
		t.Errorf("second render = %q, want the cached %q", got, "1")
	}
}

// --- Compare --------------------------------------------------------------

func TestCompareTypes(t *testing.T) {
	// Mismatched types compare by type tag, in the enum's numeric order.
	if got := Compare(TypedNew(TypeObject), TypedNew(TypeArray)); got != -1 {
		t.Errorf("object vs array = %d, want -1", got)
	}
	if got := Compare(FromString("x"), FromInt(1)); got != int(TypeString)-int(TypeInt) {
		t.Errorf("string vs int = %d", got)
	}
	// Null and userdata always compare equal to their own kind.
	if got := Compare(TypedNew(TypeNull), TypedNew(TypeNull)); got != 0 {
		t.Errorf("null vs null = %d, want 0", got)
	}
	if got := Compare(TypedNew(TypeUserdata), TypedNew(TypeUserdata)); got != 0 {
		t.Errorf("userdata vs userdata = %d, want 0", got)
	}
}

// Numbers are compared as the int truncation of their difference as doubles,
// so values less than one apart compare equal. Comparing them exactly would be
// a silent divergence (checklist item 8).
func TestCompareNumbersTruncate(t *testing.T) {
	tests := []struct {
		name string
		a, b *Object
		want int
	}{
		{"equal", FromInt(5), FromInt(5), 0},
		{"greater", FromInt(9), FromInt(5), 4},
		{"less", FromInt(5), FromInt(9), -4},
		// 1.4 - 1.0 truncates to 0.
		{"fraction truncates to equal", FromDouble(1.4), FromDouble(1.0), 0},
		{"just under one apart", FromDouble(1.99), FromDouble(1.0), 0},
		{"exactly one apart", FromDouble(2.0), FromDouble(1.0), 1},
		{"negative fraction", FromDouble(1.0), FromDouble(1.4), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compare(tc.a, tc.b); got != tc.want {
				t.Errorf("Compare = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCompareBooleans(t *testing.T) {
	if got := Compare(FromBool(true), FromBool(false)); got != 1 {
		t.Errorf("true vs false = %d, want 1", got)
	}
	if got := Compare(FromBool(false), FromBool(true)); got != -1 {
		t.Errorf("false vs true = %d, want -1", got)
	}
	if got := Compare(FromBool(true), FromBool(true)); got != 0 {
		t.Errorf("true vs true = %d, want 0", got)
	}
}

// Strings of different lengths compare by length difference alone -- never by
// content -- so "b" sorts before "aa".
func TestCompareStringsAreLengthMajor(t *testing.T) {
	if got := Compare(FromString("b"), FromString("aa")); got >= 0 {
		t.Errorf("\"b\" vs \"aa\" = %d, want negative: shorter strings compare first", got)
	}
	if got := Compare(FromString("abc"), FromString("abd")); got >= 0 {
		t.Errorf("equal-length strings must compare by content, got %d", got)
	}
	if got := Compare(FromString("abc"), FromString("abc")); got != 0 {
		t.Errorf("identical strings = %d, want 0", got)
	}
	// Two empty strings take the length branch and compare equal.
	if got := Compare(FromString(""), FromString("")); got != 0 {
		t.Errorf("two empty strings = %d, want 0", got)
	}
}

func TestCompareArrays(t *testing.T) {
	mk := func(vals ...int64) *Object {
		ar := TypedNew(TypeArray)
		for _, v := range vals {
			ArrayAppend(ar, FromInt(v))
		}
		return ar
	}

	if got := Compare(mk(1, 2), mk(1, 2)); got != 0 {
		t.Errorf("identical arrays = %d, want 0", got)
	}
	if got := Compare(mk(1, 3), mk(1, 2)); got <= 0 {
		t.Errorf("differing arrays = %d, want positive", got)
	}
	// Different lengths short-circuit on the length difference.
	if got := Compare(mk(1), mk(1, 2)); got != -1 {
		t.Errorf("length mismatch = %d, want -1", got)
	}
	// Two empty arrays hit the length branch and compare equal.
	if got := Compare(mk(), mk()); got != 0 {
		t.Errorf("two empty arrays = %d, want 0", got)
	}

	// A nil slot is ordered before a real element.
	a, b := mk(1, 2), mk(1, 2)
	ArrayReplaceIndex(a, nil, 1)
	if got := Compare(a, b); got != -1 {
		t.Errorf("nil slot vs element = %d, want -1", got)
	}
	if got := Compare(b, a); got != 1 {
		t.Errorf("element vs nil slot = %d, want 1", got)
	}
}

// Object comparison is asymmetric: it only checks that every key of the first
// object is present in the second with an equal value.
func TestCompareObjectsAreAsymmetric(t *testing.T) {
	a := TypedNew(TypeObject)
	insert(t, a, "x", FromInt(1))
	b := TypedNew(TypeObject)
	insert(t, b, "x", FromInt(1))

	if got := Compare(a, b); got != 0 {
		t.Errorf("identical objects = %d, want 0", got)
	}

	// Give b an extra key so the lengths differ; that alone decides it.
	insert(t, b, "y", FromInt(2))
	if got := Compare(a, b); got != -1 {
		t.Errorf("length mismatch = %d, want -1", got)
	}

	// Same length, disjoint keys: a's key is missing from b, so a is greater.
	c := TypedNew(TypeObject)
	insert(t, c, "z", FromInt(1))
	insert(t, c, "w", FromInt(2))
	if got := Compare(b, c); got != 1 {
		t.Errorf("a missing key = %d, want 1", got)
	}
}

func TestCompareNil(t *testing.T) {
	if got := Compare(nil, nil); got != 0 {
		t.Errorf("Compare(nil, nil) = %d, want 0", got)
	}
	if got := Compare(nil, FromInt(1)); got != int(TypeNull)-int(TypeInt) {
		t.Errorf("Compare(nil, int) = %d", got)
	}
}

func TestCompareQsortMatchesCompare(t *testing.T) {
	a, b := FromInt(1), FromInt(2)
	if CompareQsort(a, b) != Compare(a, b) {
		t.Error("CompareQsort must be Compare")
	}
}

// --- Sorting --------------------------------------------------------------

func TestArraySort(t *testing.T) {
	ar := TypedNew(TypeArray)
	for _, v := range []int64{3, 1, 2} {
		ArrayAppend(ar, FromInt(v))
	}
	ArraySort(ar, CompareQsort)

	var got []int64
	for i := uint32(0); i < ArraySize(ar); i++ {
		got = append(got, ToInt(ArrayFindIndex(ar, i)))
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("sorted = %v, want [1 2 3]", got)
	}

	// Guards: a nil comparator, a nil object and a non-array are all no-ops.
	ArraySort(ar, nil)
	ArraySort(nil, CompareQsort)
	ArraySort(FromInt(1), CompareQsort)
}

// Key sorting is length-major: shorter keys first, and only keys of equal
// length compare byte by byte. Plain lexicographic order would be a silent
// divergence (checklist item 6).
func TestSortKeysIsLengthMajor(t *testing.T) {
	o := TypedNew(TypeObject)
	for _, k := range []string{"bbb", "z", "aa", "a"} {
		insert(t, o, k, FromInt(1))
	}
	SortKeys(o, SortKeysDefault)
	wantKeys(t, o, "a", "z", "aa", "bbb")
}

func TestSortKeysRecursive(t *testing.T) {
	inner := TypedNew(TypeObject)
	insert(t, inner, "y", FromInt(1))
	insert(t, inner, "x", FromInt(2))

	arr := TypedNew(TypeArray)
	ArrayAppend(arr, FromInt(1))

	o := TypedNew(TypeObject)
	insert(t, o, "b", inner)
	insert(t, o, "a", arr)

	SortKeys(o, SortKeysDefault)
	wantKeys(t, o, "a", "b")
	if objKeys(inner)[0] != "y" {
		t.Error("a non-recursive sort must leave nested objects alone")
	}

	SortKeys(o, SortKeysRecursive)
	wantKeys(t, inner, "x", "y")
}

// The case-insensitive comparator compares four bytes at a time by
// reinterpreting them as a little-endian uint32, so within a four-byte group
// the LAST byte is the most significant. That is not lexicographic order, and
// "fixing" it would change emitted output (checklist item 7).
func TestSortKeysICaseLittleEndianQuirk(t *testing.T) {
	o := TypedNew(TypeObject)
	insert(t, o, "aaab", FromInt(1))
	insert(t, o, "bbba", FromInt(2))

	SortKeys(o, SortKeysDefault)
	wantKeys(t, o, "aaab", "bbba") // ordinary byte order

	SortKeys(o, SortKeysICase)
	// 'a' in the most significant position beats 'b', so "bbba" comes first.
	wantKeys(t, o, "bbba", "aaab")

	if got := lcCmp([]byte("bbba"), []byte("aaab")); got >= 0 {
		t.Errorf("lcCmp = %d, want negative", got)
	}
}

// The leftover tail detects inequality on the folded bytes but returns the
// difference of the raw ones, so a lowercase letter can sort after an
// uppercase letter that folds to a later value.
func TestSortKeysICaseTailQuirk(t *testing.T) {
	if got := lcCmp([]byte("a"), []byte("B")); got != 'a'-'B' {
		t.Errorf("lcCmp(a, B) = %d, want %d (raw byte difference)", got, 'a'-'B')
	}

	o := TypedNew(TypeObject)
	insert(t, o, "a", FromInt(1))
	insert(t, o, "B", FromInt(2))
	SortKeys(o, SortKeysICase)
	// A correct caseless comparison would put "a" first.
	wantKeys(t, o, "B", "a")
}

func TestLcCmpFoldsASCIIOnly(t *testing.T) {
	if got := lcCmp([]byte("ABCD"), []byte("abcd")); got != 0 {
		t.Errorf("lcCmp of case variants = %d, want 0", got)
	}
	if got := lcCmp([]byte("aaaaZ"), []byte("aaaaz")); got != 0 {
		t.Errorf("lcCmp of a case variant in the tail = %d, want 0", got)
	}
	// The raw-byte difference is taken as a signed char, so a high byte sorts
	// before an ASCII one.
	if got := lcCmp([]byte{0x80}, []byte{'a'}); got >= 0 {
		t.Errorf("lcCmp(0x80, 'a') = %d, want negative (signed char difference)", got)
	}
}

func TestSortKeysGuards(t *testing.T) {
	SortKeys(nil, SortKeysDefault)
	SortKeys(FromInt(1), SortKeysDefault)
	// An object that never got a hash must not panic.
	SortKeys(TypedNew(TypeObject), SortKeysRecursive)
}

// Sorting reorders emission only; lookups go through the map and are
// unaffected.
func TestSortKeysDoesNotBreakLookup(t *testing.T) {
	o := TypedNew(TypeObject)
	for _, k := range []string{"ccc", "a", "bb"} {
		insert(t, o, k, FromString(k))
	}
	SortKeys(o, SortKeysDefault)
	for _, k := range []string{"ccc", "a", "bb"} {
		if got := o.Lookup(k); got == nil || ToString(got) != k {
			t.Errorf("lookup of %q broke after sorting", k)
		}
	}
	// Deleting after a sort must still keep the survivors in order.
	if !o.DeleteKey([]byte("bb")) {
		t.Fatal("DeleteKey after sorting failed")
	}
	wantKeys(t, o, "a", "ccc")
}
