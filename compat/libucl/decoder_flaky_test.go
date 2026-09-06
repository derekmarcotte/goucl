package libucl

import (
	"reflect"
	"sort"
	"testing"
)

// TestObjectDecode_structKeys_Deterministic covers the same behaviour as the
// upstream TestObjectDecode_structKeys, without the flake.
//
// decodeIntoStruct builds decodedFields by ranging a map[*reflect.StructField
// ]reflect.Value, so the order it appends "Foo" and "Bar" in is whatever Go's
// map iteration hands back that run, not the field declaration order. The
// upstream test asserts a fixed slice, []string{"Foo", "Bar"}, so it fails
// whenever iteration comes back the other way round.
//
// The upstream test is left in place, unmodified, in decoder_test.go: that
// file is one of the four required to stay byte-identical to
// github.com/mitchellh/go-libucl, which is the proof that this package is a
// drop-in replacement. This test is the real assertion of what that test
// intended -- decodedFields is a set of which fields were populated, not a
// sequence -- checked in a way that does not depend on map iteration order.
func TestObjectDecode_structKeys_Deterministic(t *testing.T) {
	type Struct struct {
		Foo  []string
		Bar  string
		Baz  string
		Keys []string `libucl:",decodedFields"`
	}

	var result Struct

	obj := testParseString(t, "foo = [foo, bar, 12]; bar = baz;")
	defer obj.Close()

	if err := obj.Decode(&result); err != nil {
		t.Fatalf("err: %s", err)
	}

	if !reflect.DeepEqual([]string{"foo", "bar", "12"}, result.Foo) {
		t.Fatalf("bad Foo: %#v", result.Foo)
	}
	if result.Bar != "baz" {
		t.Fatalf("bad Bar: %#v", result.Bar)
	}
	if result.Baz != "" {
		t.Fatalf("bad Baz: %#v", result.Baz)
	}

	keys := append([]string(nil), result.Keys...)
	sort.Strings(keys)

	expectedKeys := []string{"Bar", "Foo"}
	if !reflect.DeepEqual(expectedKeys, keys) {
		t.Fatalf("bad Keys: %#v", result.Keys)
	}
}
