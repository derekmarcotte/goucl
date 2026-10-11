package goucl

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.marcottesoftware.ca/goucl/internal/core"
)

// The engine is proven against the C by 168 goldens in six emitter modes. Its
// harness is tests/test_basic.c, which parses with KEY_LOWERCASE, registers an
// ABI variable, sets file variables and emits the SECOND round trip -- so a
// golden is not the output of a plain parse-and-emit and cannot be compared
// against this package's Parse directly. Options that would let it are M3.
//
// What matters here is a different property anyway: that this package is
// transparent over the engine. Node is a view rather than a copy and Encode
// calls the same emitter, so the two should agree byte for byte on every
// fixture in the corpus, in every text format, including the fixtures that
// fail to parse. These tests pin that, over the same 168 inputs.

const corpus = "internal/core/testdata/basic"

func fixtures(t *testing.T) []string {
	t.Helper()
	ins, err := filepath.Glob(filepath.Join(corpus, "*.in"))
	if err != nil || len(ins) == 0 {
		t.Fatalf("no fixtures in %s: %v", corpus, err)
	}
	return ins
}

var formats = []struct {
	name string
	f    Format
	em   core.Emitter
}{
	{"config", FormatConfig, core.EmitConfig},
	{"json", FormatJSON, core.EmitJSON},
	{"compact", FormatJSONCompact, core.EmitJSONCompact},
	{"yaml", FormatYAML, core.EmitYAML},
}

// engineParse is what this package does, spelled against the engine directly.
func engineParse(path string) (*core.Object, core.EmitComments, error) {
	p := core.NewParser(parserFlags)
	p.AddFile(path)
	if err := p.GetError(); err != nil {
		return nil, nil, err
	}
	return p.GetObject(), p.GetComments(), nil
}

func TestTransparentOverEngine(t *testing.T) {
	ins := fixtures(t)
	if len(ins) == 0 {
		t.Fatal("empty corpus")
	}

	var parsed, failed int
	for _, in := range ins {
		base := filepath.Base(in)
		t.Run(base, func(t *testing.T) {
			wantObj, wantComments, wantErr := engineParse(in)
			node, err := ParseFile(in)

			if (wantErr != nil) != (err != nil) {
				t.Fatalf("disagree on whether %s parses: engine err=%v, goucl err=%v",
					base, wantErr, err)
			}
			if wantErr != nil {
				failed++
				// The message is the engine's, so it must survive intact.
				if err.Error() != wantErr.Error() {
					t.Errorf("error text differs:\n engine: %s\n goucl:  %s",
						wantErr, err)
				}
				var se *SyntaxError
				if !errors.As(err, &se) {
					t.Fatalf("parse error is not a *SyntaxError: %T", err)
				}
				return
			}
			parsed++

			for _, f := range formats {
				var want bytes.Buffer
				if err := core.EmitTextFull(&want, wantObj, f.em, wantComments); err != nil {
					t.Fatalf("engine emit %s: %v", f.name, err)
				}
				got, err := node.Bytes(f.f)
				if err != nil {
					t.Fatalf("goucl emit %s: %v", f.name, err)
				}
				if !bytes.Equal(got, want.Bytes()) {
					t.Errorf("%s: %d bytes from goucl, %d from the engine\n got: %q\nwant: %q",
						f.name, len(got), want.Len(), truncate(got), truncate(want.Bytes()))
				}
			}
		})
	}

	// Guard against the corpus silently emptying, the way
	// golden_coverage_test.go guards the engine's.
	if parsed+failed != len(ins) {
		t.Fatalf("accounted for %d of %d fixtures", parsed+failed, len(ins))
	}
	t.Logf("%d fixtures: %d parsed, %d failed to parse, 4 formats each", len(ins), parsed, failed)
}

// TestCommentRoundTrip checks the property comments exist for: that a document
// carrying them survives a parse and re-emit in the config format.
func TestCommentRoundTrip(t *testing.T) {
	for _, in := range fixtures(t) {
		base := filepath.Base(in)
		node, err := ParseFile(in)
		if err != nil {
			continue // a failing fixture is covered above
		}
		first, err := node.Bytes(FormatConfig)
		if err != nil {
			t.Fatalf("%s: emit: %v", base, err)
		}
		reparsed, err := Parse(first)
		if err != nil {
			// Re-parsing our own output must work; if it does not, that is a
			// real finding and not something to skip past.
			t.Errorf("%s: re-parsing emitted config failed: %v", base, err)
			continue
		}
		second, err := reparsed.Bytes(FormatConfig)
		if err != nil {
			t.Fatalf("%s: re-emit: %v", base, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("%s: config emission is not idempotent\nfirst:  %q\nsecond: %q",
				base, truncate(first), truncate(second))
		}
	}
}

func truncate(b []byte) []byte {
	const max = 300
	if len(b) <= max {
		return b
	}
	return append(append([]byte{}, b[:max]...), "..."...)
}

func TestCorpusIsPresent(t *testing.T) {
	if _, err := os.Stat(corpus); err != nil {
		t.Fatalf("corpus missing: %v", err)
	}
	if n := len(fixtures(t)); n != 28 {
		t.Fatalf("corpus is %d fixtures, expected the 28 that back the 168 goldens", n)
	}
}
