package goucl

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseString checks that no input, however malformed, can make the parser
// panic, and that anything it does accept survives a round trip.
//
// The parser is the component most exposed to untrusted input -- it is the one
// that reads config files -- and it was the only decoder without a fuzz target;
// msgpack and csexp already had one. It is also the area where the C has known
// out-of-bounds reads on malformed input (see NOTES-parser.md), which this port
// bounds rather than reproduces. That divergence is only worth anything if it
// actually holds, which is what this exercises.
//
// Macros are disabled. A fuzzer-generated `.include` would otherwise drive
// filesystem access with attacker-controlled paths, which makes runs slow and
// non-deterministic without testing anything about the state machine. The
// macro paths have their own unit coverage.
//
// The invariant is "never panics", not "always round-trips": a document that
// emits to text the parser then rejects is a real property of libucl, not
// something this port invents (docs/findings.md #3 and #7 are both of that
// shape), so a failed reparse is only a failure if it panics.
func FuzzParseString(f *testing.F) {
	seeds, err := filepath.Glob("testdata/basic/*.in")
	if err != nil {
		f.Fatalf("globbing seeds: %v", err)
	}
	if len(seeds) == 0 {
		f.Fatal("no seed corpus; the fuzz target would start from nothing")
	}
	for _, s := range seeds {
		if b, err := os.ReadFile(s); err == nil {
			f.Add(string(b))
		}
	}
	// A few shapes the fixtures do not cover, aimed at the lexer's edges.
	for _, s := range []string{
		"", "{", "[", "\"", "'", "<<EOD", "a=", "a{b[c", "\x00", "k = \x00v;",
		"/*", "/* /* */", "#", "a [\n # c\n 1\n]", "a = <<EOD\nx\nEOD",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		p := NewParser(ParserDisableMacro)
		if !p.AddString(in) {
			return // a rejected document is a fine outcome
		}
		obj := p.GetObject()
		if obj == nil {
			return
		}

		// Everything reachable on an accepted tree must also be panic-free.
		for _, e := range []Emitter{EmitConfig, EmitJSON, EmitJSONCompact, EmitYAML} {
			var buf bytes.Buffer
			if err := EmitTextFull(&buf, obj, e, nil); err != nil {
				continue
			}
			// Reparsing may legitimately fail; it must not panic.
			p2 := NewParser(ParserDisableMacro)
			p2.AddString(buf.String())
			_ = p2.GetObject()
		}

		var mp bytes.Buffer
		if err := EncodeMsgPackTo(&mp, obj); err == nil {
			_, _ = DecodeMsgPack(mp.Bytes())
		}

		// Conversions and traversal on arbitrary shapes.
		_ = ToStringForced(obj)
		_, _ = ToLString(obj)
		_ = ArraySize(obj)
		var it Iter
		for cur := Iterate(obj, &it, true); cur != nil; cur = Iterate(obj, &it, true) {
			_ = cur.Type()
			_ = ToString(cur)
		}
	})
}
