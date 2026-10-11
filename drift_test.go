package goucl

import (
	"testing"

	"github.com/derekmarcotte/goucl/internal/core"
)

// The engine is the source of truth. This package necessarily restates two of
// its enumerations, because a public API cannot name a type from an internal
// package -- but a restatement can drift, and these tests are what stop it.
//
// They do not hold their own list of what exists. They enumerate the engine's
// values and demand this package account for every one, so adding a type to
// core fails here rather than silently falling out of the Go-shaped API.

// searchLimit is well past the engine's largest enumerator; the loops below
// probe upward and let the engine say which values are real.
const searchLimit = 64

// TestKindCoversEveryCoreType fails if the engine gains a type this package
// does not map, loses one it still maps, or renames one.
func TestKindCoversEveryCoreType(t *testing.T) {
	found := 0
	for i := 0; i < searchLimit; i++ {
		ct := core.Type(i)
		// The engine names its own types; anything it calls "unknown" is not
		// a real type, so this list comes from core rather than from here.
		if ct.String() == "unknown" {
			continue
		}
		found++

		k := kindOf(ct)
		if k == KindInvalid {
			t.Errorf("core.Type(%d) is %q but maps to KindInvalid: the Go-shaped API cannot express it",
				i, ct)
			continue
		}
		back, ok := k.coreType()
		if !ok || back != ct {
			t.Errorf("core.Type(%d) %q round trips to %v, want itself", i, ct, back)
		}
		if k.String() != ct.String() {
			t.Errorf("core.Type(%d) is named %q by the engine and %q here",
				i, ct, k)
		}
	}

	if found == 0 {
		t.Fatal("found no core types; the probe is broken, not the mapping")
	}
	t.Logf("every one of the engine's %d types is reachable as a Kind", found)
}

// TestNoKindInventsAType is the other direction: every Kind this package
// exposes must correspond to something the engine actually has.
func TestNoKindInventsAType(t *testing.T) {
	for k := Kind(0); k < searchLimit; k++ {
		ct, ok := k.coreType()
		if !ok {
			continue // KindInvalid and unused values
		}
		if ct.String() == "unknown" {
			t.Errorf("%v maps to core.Type(%d), which the engine does not recognise",
				k, int(ct))
		}
		if kindOf(ct) != k {
			t.Errorf("%v maps to %q which maps back to %v", k, ct, kindOf(ct))
		}
	}
}

// TestFormatsMapToDistinctEmitters pins the format translation. MessagePack is
// deliberately absent: the engine has an EmitMsgPack emitter, but it is not a
// text format and EmitTextFull rejects it, so it is not one of these.
func TestFormatsMapToDistinctEmitters(t *testing.T) {
	seen := map[core.Emitter]Format{}
	for _, f := range []Format{FormatConfig, FormatJSON, FormatJSONCompact, FormatYAML} {
		em, ok := f.emitter()
		if !ok {
			t.Errorf("%v has no engine emitter", f)
			continue
		}
		if prev, dup := seen[em]; dup {
			t.Errorf("%v and %v both map to engine emitter %d", prev, f, int(em))
		}
		seen[em] = f
	}
	if len(seen) != 4 {
		t.Errorf("mapped %d formats, want 4", len(seen))
	}
	// A Format outside the set must be rejected rather than silently
	// becoming emitter zero.
	if _, ok := Format(99).emitter(); ok {
		t.Error("an unknown Format produced an emitter")
	}
}

// TestErrorCodesAreTheEngines pins the error codes to the engine's values
// rather than to a copy of them.
func TestErrorCodesAreTheEngines(t *testing.T) {
	for _, tc := range []struct {
		mine ErrorCode
		core core.ErrorCode
	}{
		{ErrOK, core.ErrOK},
		{ErrSyntax, core.ErrSyntax},
		{ErrIO, core.ErrIO},
		{ErrState, core.ErrState},
		{ErrNested, core.ErrNested},
		{ErrUnpaired, core.ErrUnpaired},
		{ErrMacro, core.ErrMacro},
		{ErrInternal, core.ErrInternal},
		{ErrSSL, core.ErrSSL},
		{ErrMerge, core.ErrMerge},
	} {
		if int(tc.mine) != int(tc.core) {
			t.Errorf("%v is %d here and %d in the engine", tc.mine, int(tc.mine), int(tc.core))
		}
		if tc.mine.String() == "" {
			t.Errorf("%d has no name", int(tc.mine))
		}
	}
}
