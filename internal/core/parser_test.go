package core

// Golden-driven parser tests.
//
// testdata/basic holds libucl's own fixtures plus output captured from the
// compiled C library by tools/gen-goldens.sh. Reproducing those bytes requires
// mirroring tests/test_basic.c exactly, not just parsing and emitting once:
// see runBasicHarness below and docs/findings.md item 6.
//
// One golden is ours rather than the C's: 9.comments.golden replaced
// 9.comments.err, which recorded a reparse failure caused only by the array
// comment bug this port deliberately does not reproduce. See docs/notes/parser.md
// and TestParserArrayLeadingComment.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// basicMode is one of the six command lines tools/gen-goldens.sh captures.
type basicMode struct {
	name   string
	flags  ParserFlag
	format Emitter
}

var basicModes = []basicMode{
	{"config", 0, EmitConfig},
	{"json", 0, EmitJSON},
	{"compact", 0, EmitJSONCompact},
	{"yaml", 0, EmitYAML},
	{"comments", ParserSaveComments, EmitConfig},
	{"nomacro", ParserDisableMacro, EmitConfig},
}

// basicDir is testdata/basic resolved the way the C harness saw it, so error
// messages (which embed parser->cur_file) can be normalised the same way
// tools/gen-goldens.sh normalises them with sed.
func basicDir(t *testing.T) string {
	t.Helper()
	dir, err := realpath("testdata/basic")
	if err != nil {
		t.Fatalf("resolving testdata/basic: %v", err)
	}
	return dir
}

// runBasicHarness is tests/test_basic.c.
//
// The golden is emit(parse(emit(parse(input)))) followed by one unconditional
// newline. Both parsers get UCL_PARSER_KEY_LOWERCASE and the mode's extra
// flag; only the first gets the ABI variable and the input's file variables.
// A parse failure in either phase is itself a legitimate expectation and is
// reported the way the C harness writes it to the output file.
func runBasicHarness(inPath string, mode basicMode) []byte {
	var out bytes.Buffer

	flags := ParserKeyLowercase | mode.flags

	data, err := os.ReadFile(inPath)
	if err != nil {
		out.WriteString("cannot read input: " + err.Error() + "\n")
		return out.Bytes()
	}

	parser := NewParser(flags)
	parser.RegisterVariable("ABI", "unknown")
	parser.SetFilevars(inPath, true)
	parser.AddChunkFull(data, 0, DuplicateAppend, ParseUCL)

	if e := parser.GetError(); e != nil {
		out.WriteString("Error occurred (phase 1): " + e.Error() + "\n")
		return out.Bytes()
	}

	obj := parser.GetObject()
	var comments EmitComments
	if flags&ParserSaveComments != 0 {
		comments = parser.GetComments()
	}

	var emitted []byte
	switch mode.format {
	case EmitJSON, EmitJSONCompact, EmitYAML:
		// The C uses ucl_object_emit here, which takes no comments.
		emitted, _ = EmitText(obj, mode.format)
	default:
		var buf bytes.Buffer
		_ = EmitTextFull(&buf, obj, EmitConfig, comments)
		emitted = buf.Bytes()
	}

	parser2 := NewParser(flags)
	// ucl_parser_add_string with len 0 means strlen, so the C stops at the
	// first NUL byte of the emitted buffer.
	parser2.AddString(cString(emitted))

	if e := parser2.GetError(); e != nil {
		out.WriteString("Error occurred (phase 2): " + e.Error() + "\n")
		// The C prints this with fprintf("%s\n"), which stops at the first NUL
		// -- and a saved block comment can legitimately contain one, see the
		// comment-length note in skipComments.
		out.WriteString(cString(emitted))
		out.WriteByte('\n')
		return out.Bytes()
	}

	if flags&ParserSaveComments != 0 {
		comments = parser2.GetComments()
	} else {
		comments = nil
	}
	obj2 := parser2.GetObject()
	_ = EmitTextFull(&out, obj2, mode.format, comments)
	out.WriteByte('\n')
	return out.Bytes()
}

// cString truncates at the first NUL, the way strlen does.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// TestBasicGoldens runs every fixture in testdata/basic in all six modes.
//
// The working directory matters and is deliberately left alone: `go test` runs
// in the package directory, where "./1.in" does not exist. basic/14.in does
// `.include(try=true) "./1.in"`, and libucl resolves that against the process
// working directory rather than the including file's directory (findings.md
// item 1), so its golden records the not-found path. Do not os.Chdir into
// testdata/basic -- that would make the include succeed and the golden fail.
func TestBasicGoldens(t *testing.T) {
	dir := basicDir(t)
	ins, err := filepath.Glob("testdata/basic/*.in")
	if err != nil || len(ins) == 0 {
		t.Fatalf("no fixtures found in testdata/basic: %v", err)
	}

	for _, in := range ins {
		base := strings.TrimSuffix(filepath.Base(in), ".in")
		abs := filepath.Join(dir, filepath.Base(in))

		for _, mode := range basicModes {
			t.Run(base+"."+mode.name, func(t *testing.T) {
				goldPath := filepath.Join("testdata/basic", base+"."+mode.name+".golden")
				errPath := filepath.Join("testdata/basic", base+"."+mode.name+".err")

				gold, gerr := os.ReadFile(goldPath)
				experr, eerr := os.ReadFile(errPath)
				if gerr != nil && eerr != nil {
					t.Skipf("no golden for %s.%s", base, mode.name)
				}

				got := runBasicHarness(abs, mode)
				// The generator rewrote the fixture directory out of error
				// text; do the same so paths compare.
				norm := strings.ReplaceAll(string(got), dir+"/", "testdata/basic/")

				if eerr == nil {
					if !strings.HasPrefix(norm, "Error occurred") {
						t.Errorf("expected a parse failure, got output:\n%s", indent(norm))
						return
					}
					if norm != string(experr) {
						t.Errorf("error text mismatch\n%s", diffText(string(experr), norm))
					}
					return
				}

				if strings.HasPrefix(norm, "Error occurred") {
					t.Errorf("unexpected parse failure:\n%s", indent(norm))
					return
				}
				if norm != string(gold) {
					t.Errorf("output mismatch\n%s", diffText(string(gold), norm))
				}
			})
		}
	}
}

// diffText renders the first differing line plus a little context, which is
// far more readable than dumping two multi-kilobyte documents.
func diffText(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			var b strings.Builder
			lo := i - 3
			if lo < 0 {
				lo = 0
			}
			b.WriteString("first difference at line ")
			b.WriteString(itoa(i + 1))
			b.WriteString("\n")
			for j := lo; j < i; j++ {
				b.WriteString("   " + wl[j] + "\n")
			}
			b.WriteString("  -" + wl[i] + "\n")
			b.WriteString("  +" + gl[i] + "\n")
			return b.String()
		}
	}
	if len(wl) != len(gl) {
		return "line counts differ: want " + itoa(len(wl)) + ", got " + itoa(len(gl)) +
			"\nwant tail: " + strings.Join(tail(wl, 3), "\\n") +
			"\ngot tail:  " + strings.Join(tail(gl, 3), "\\n")
	}
	return "documents differ but no line does (trailing bytes?)"
}

func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
