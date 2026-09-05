package goucl

import (
	"io"
	"testing"
)

// A nil *Object is a normal value in this API: the C reaches most of these
// entry points with a NULL ucl_object_t, and lookups that miss return one. So
// nothing exported may panic on nil. These call the surface with nil
// receivers, nil arguments and zero values, and assert only that nothing
// blows up -- semantics are covered by the tests for each component.
//
// This exists because a nil dereference in a config parser is a crash in
// whatever embeds it, and a panic reached through a lookup that returned
// nothing is the easiest kind to ship by accident.
func TestNilObjectDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a nil object: %v", r)
		}
	}()

	var o *Object

	// Type and flags.
	_ = o.Type()
	_ = o.Flags()
	_ = o.Priority()
	o.SetPriority(3)
	_ = o.Key()
	_, _ = o.KeyL()

	// Conversions, both plain and _safe.
	_ = ToInt(o)
	_, _ = ToIntSafe(o)
	_ = ToDouble(o)
	_, _ = ToDoubleSafe(o)
	_ = ToBoolean(o)
	_, _ = ToBooleanSafe(o)
	_ = ToString(o)
	_, _ = ToStringSafe(o)
	_ = ToStringForced(o)
	_, _ = ToLString(o)
	_, _, _ = ToLStringSafe(o)

	// Lookups.
	_ = o.Lookup("k")
	_ = o.LookupLen([]byte("k"))
	_ = o.LookupAny("a", "b")
	_ = o.LookupPath("a.b")
	_ = o.LookupPathChar("a/b", '/')

	// Arrays, with nil in every position.
	_ = ArraySize(o)
	_ = ArrayAppend(o, nil)
	_ = ArrayPrepend(o, nil)
	_ = ArrayMerge(o, nil, false)
	_ = ArrayIndexOf(o, nil)
	ArraySort(o, func(a, b *Object) int { return 0 })

	// Comparison and sorting.
	_ = Compare(o, nil)
	_ = CompareQsort(nil, o)
	SortKeys(o, 0)
	_ = Reserve(o, 4)

	// Reference handling.
	Free(o)
	Unref(o)

	// Iteration over nothing.
	var it Iter
	for cur := Iterate(o, &it, true); cur != nil; cur = Iterate(o, &it, true) {
		_ = cur
	}
	IterateEnd(o, &it)
}

func TestNilObjectEmittersDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked emitting a nil object: %v", r)
		}
	}()

	for _, f := range []Emitter{EmitConfig, EmitJSON, EmitJSONCompact, EmitYAML, EmitMsgPack} {
		_, _ = EmitText(nil, f)
		_, _ = EmitTextString(nil, f)
		_ = EmitTextTo(io.Discard, nil, f)
		_ = EmitTextFull(io.Discard, nil, f, nil)
	}
	_, _ = EncodeMsgPack(nil)
	_ = EncodeMsgPackTo(io.Discard, nil)

	// An emitter value outside the known range must be refused, not panic.
	_, err := EmitText(FromInt(1), Emitter(99))
	if err == nil {
		t.Error("an out-of-range emitter was accepted")
	}
}

// The compatibility surface is the one applications call, and go-libucl's
// contract is that a missing key yields a nil *Object which is then used.
func TestNilCompatObjectDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a nil CompatObject: %v", r)
		}
	}()

	var o *CompatObject
	_ = o.Type()
	_ = o.Key()
	_ = o.Len()
	_ = o.ToBool()
	_ = o.ToInt()
	_ = o.ToFloat()
	_ = o.ToString()
	_ = o.Ref()
	_ = o.Close()
	o.Delete("k")
	_ = o.Get("missing")
	_, _ = o.Emit(EmitConfig)

	it := o.Iterate(true)
	if it != nil {
		for e := it.Next(); e != nil; e = it.Next() {
			_ = e
		}
		it.Close()
	}

}

// Empty and malformed input through the public parser entry points.
func TestParserEdgeInputsDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on edge input: %v", r)
		}
	}()

	for _, in := range []string{"", " ", "\x00", "{", "[", "\"", "'", "="} {
		p := NewParser(0)
		p.AddString(in)
		obj := p.GetObject()
		if obj != nil {
			_, _ = EmitTextString(obj, EmitConfig)
		}
	}

	// Nil and empty byte slices through every decoder.
	p := NewParser(0)
	p.AddChunk(nil)
	_, _ = DecodeMsgPack(nil)
	_, _ = DecodeMsgPack([]byte{})
	_, _ = DecodeCSexp(nil)
	_, _ = DecodeCSexp([]byte{})
}
