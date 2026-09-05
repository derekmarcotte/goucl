package goucl

// Port of libucl's tests/test_schema.c and its tests/schema.test wrapper.
//
// testdata/schema holds the upstream draft-4 suites verbatim. The C harness
// pipes each file into a binary that fails the whole process on the first
// mismatch; this runs every case as its own subtest instead, which reports
// more but means exactly the same thing per case (see docs/reference/schema.md 6).
//
// Like the C, the shell wrapper globs *.json only, so the two *.json.disabled
// files never run. TestSchemaDisabledSuitesAreSkipped pins that.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// schemaTestCase is one entry of a suite's "tests" array.
type schemaTestCase struct {
	description string
	data        *Object
	valid       bool
}

// schemaSuite is one entry of a suite file's top-level array.
type schemaSuite struct {
	description string
	schema      *Object
	tests       []schemaTestCase
}

// parseSuiteFile loads one suite file with this package's own parser rather
// than encoding/json. JSON is a UCL subset, so this exercises the parser on
// the same bytes the C harness fed to ucl_parser_add_string.
func parseSuiteFile(t *testing.T, path string) []schemaSuite {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	p := NewParser(0)
	if !p.AddChunk(data) {
		t.Fatalf("parsing %s: %v", path, p.GetError())
	}
	top := p.GetObject()
	if top == nil {
		t.Fatalf("parsing %s: no object", path)
	}

	var suites []schemaSuite
	var it Iter
	for elt := Iterate(top, &it, true); elt != nil; elt = Iterate(top, &it, true) {
		// perform_tests: a suite must be an object carrying all three keys.
		if elt.Type() != TypeObject {
			t.Fatalf("%s: bad test case: suite is %s, not an object", path, elt.TypeString())
		}
		schema := elt.Lookup("schema")
		tests := elt.Lookup("tests")
		desc := elt.Lookup("description")
		if schema == nil || tests == nil || desc == nil {
			t.Fatalf("%s: bad test case: suite is missing schema, tests or description", path)
		}

		s := schemaSuite{description: ToString(desc), schema: schema}
		var tit Iter
		for tc := Iterate(tests, &tit, true); tc != nil; tc = Iterate(tests, &tit, true) {
			// perform_test: a case must carry data, description and valid.
			cd := tc.Lookup("description")
			cv := tc.Lookup("valid")
			data := tc.Lookup("data")
			if cd == nil || cv == nil || data == nil {
				t.Fatalf("%s: bad test case in suite %q", path, s.description)
			}
			s.tests = append(s.tests, schemaTestCase{
				description: ToString(cd),
				data:        data,
				valid:       ToBoolean(cv),
			})
		}
		suites = append(suites, s)
	}
	return suites
}

// schemaSuiteFiles lists testdata/schema/*.json, matching the shell glob in
// tests/schema.test. The *.json.disabled files are excluded by the glob, not
// by an explicit skip -- upstream disables them the same way.
func schemaSuiteFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "schema", "*.json"))
	if err != nil {
		t.Fatalf("globbing suites: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no schema suites found in testdata/schema")
	}
	sort.Strings(files)
	return files
}

// TestSchemaSuites is the ported harness: every case in every enabled suite.
func TestSchemaSuites(t *testing.T) {
	for _, path := range schemaSuiteFiles(t) {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			for _, suite := range parseSuiteFile(t, path) {
				t.Run(subtestName(suite.description), func(t *testing.T) {
					for _, tc := range suite.tests {
						t.Run(subtestName(tc.description), func(t *testing.T) {
							got, serr := Validate(suite.schema, tc.data)
							if got == tc.valid {
								return
							}
							// The C prints the message plus both objects
							// re-emitted as config; do the same.
							msg := "<none>"
							if serr != nil {
								msg = serr.Code.String() + ": " + serr.Msg
							}
							t.Errorf("expected %s, got %s: %s\ndata:   %s\nschema: %s",
								validity(tc.valid), validity(got), msg,
								emitForTest(tc.data), emitForTest(suite.schema))
						})
					}
				})
			}
		})
	}
}

func validity(v bool) string {
	if v {
		return "valid"
	}
	return "invalid"
}

// emitForTest renders an object the way the C harness's
// ucl_object_emit(..., UCL_EMIT_CONFIG) call does, flattened onto one line so
// a failure stays readable.
func emitForTest(o *Object) string {
	b, err := EmitText(o, EmitJSONCompact)
	if err != nil {
		return "<unemittable: " + err.Error() + ">"
	}
	return string(b)
}

// subtestName keeps Go's subtest names free of the separators it would
// otherwise have to escape.
func subtestName(s string) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(s)
}

// TestSchemaDisabledSuitesAreSkipped documents why two upstream files never
// run: the shell wrapper globs *.json, so the .disabled suffix removes them.
// definitions.json covers a "definitions" keyword libucl has no special
// handling for, and refRemote.json needs live network fetches and the "id"
// keyword, neither of which libucl implements. Both are kept in testdata as
// documentation.
func TestSchemaDisabledSuitesAreSkipped(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "schema", "*.json.disabled"))
	if err != nil {
		t.Fatalf("globbing disabled suites: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 disabled suites, found %d: %v", len(files), files)
	}
	for _, f := range files {
		t.Logf("not run, as upstream does not run it: %s", filepath.Base(f))
	}
}

// TestSchemaCorpusPatternsCompile checks that every regexp the corpus uses is
// accepted by regexp.CompilePOSIX, so no suite result depends on the POSIX
// ERE constructs RE2 cannot express. If this ever fails, the failing pattern
// is the one to write up.
func TestSchemaCorpusPatternsCompile(t *testing.T) {
	var patterns []string
	collect := func(schema *Object) {
		var walk func(o *Object)
		walk = func(o *Object) {
			if o == nil {
				return
			}
			switch o.Type() {
			case TypeObject:
				var it Iter
				for elt := Iterate(o, &it, true); elt != nil; elt = Iterate(o, &it, true) {
					switch schemaKey(elt) {
					case "pattern":
						if elt.Type() == TypeString {
							patterns = append(patterns, ToString(elt))
						}
					case "patternProperties":
						var pit Iter
						for p := Iterate(elt, &pit, true); p != nil; p = Iterate(elt, &pit, true) {
							patterns = append(patterns, schemaKey(p))
						}
					}
					walk(elt)
				}
			case TypeArray:
				var it Iter
				for elt := Iterate(o, &it, true); elt != nil; elt = Iterate(o, &it, true) {
					walk(elt)
				}
			}
		}
		walk(schema)
	}

	for _, path := range schemaSuiteFiles(t) {
		for _, suite := range parseSuiteFile(t, path) {
			collect(suite.schema)
		}
	}
	if len(patterns) == 0 {
		t.Fatal("found no patterns in the corpus")
	}
	for _, p := range patterns {
		if _, err := regexp.CompilePOSIX(p); err != nil {
			t.Errorf("corpus pattern %q does not compile as POSIX ERE: %v", p, err)
		}
	}
	t.Logf("%d corpus patterns, all POSIX ERE compatible", len(patterns))
}
