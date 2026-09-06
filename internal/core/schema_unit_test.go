package core

// Unit tests for the parts of ucl_schema.c the upstream corpus never reaches:
// the UCL-only minValues/maxValues keywords, external and array-indexed $refs,
// error codes and message text, the recursion guard, and each place this port
// deliberately departs from the C. See docs/notes/schema.md.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustParse parses UCL/JSON text into an object, failing the test if it will
// not parse.
func mustParse(t *testing.T, text string) *Object {
	t.Helper()
	p := NewParser(0)
	if !p.AddString(text) {
		t.Fatalf("parsing %q: %v", text, p.GetError())
	}
	o := p.GetObject()
	if o == nil {
		t.Fatalf("parsing %q: no object", text)
	}
	return o
}

// mustValue parses one JSON value. The UCL parser needs a key to hang a
// top-level scalar off, so the value is wrapped and then looked back up.
func mustValue(t *testing.T, text string) *Object {
	t.Helper()
	o := mustParse(t, "v = "+text+"\n").Lookup("v")
	if o == nil {
		t.Fatalf("parsing value %q: no value", text)
	}
	return o
}

// validateText is Validate over textual schema and data.
func validateText(t *testing.T, schema, data string) (bool, *SchemaError) {
	t.Helper()
	return Validate(mustValue(t, schema), mustValue(t, data))
}

func TestSchemaErrorCodesAndMessages(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		data   string
		code   SchemaErrorCode
		msg    string
	}{
		{
			"type mismatch",
			`{"type": "integer"}`, `{"v": "x"}`,
			// The data is the object itself, so the mismatch is object vs int.
			SchemaTypeMismatch, "Invalid type of object, expected integer",
		},
		{
			"bad type name",
			`{"type": "quux"}`, `{}`,
			SchemaInvalidSchema, "Type attribute is invalid in schema",
		},
		{
			"missing property",
			`{"required": ["a"]}`, `{"b": 1}`,
			SchemaMissingProperty, "object has missing property a",
		},
		{
			"required is not an array",
			`{"required": "a"}`, `{"b": 1}`,
			SchemaInvalidSchema, "required attribute is invalid in schema",
		},
		{
			"additionalProperties is not a bool or object",
			`{"additionalProperties": "no"}`, `{"b": 1}`,
			SchemaInvalidSchema, "additionalProperties attribute is invalid in schema",
		},
		{
			"non-allowed property",
			`{"additionalProperties": false}`, `{"b": 1}`,
			SchemaConstraint, "object has non-allowed property b",
		},
		{
			"too few properties",
			`{"minProperties": 3}`, `{"a": 1}`,
			SchemaConstraint, "object has not enough properties: 1, minimum is: 3",
		},
		{
			"too many properties",
			`{"maxProperties": 1}`, `{"a": 1, "b": 2}`,
			SchemaConstraint, "object has too many properties: 2, maximum is: 1",
		},
		{
			"missing dependency",
			`{"dependencies": {"a": ["b"]}}`, `{"a": 1}`,
			SchemaMissingDependency, "dependency b is missing for key a",
		},
		{
			"enum",
			`{"enum": [1, 2]}`, `{"v": 3}`,
			SchemaConstraint, "object is not one of enumerated patterns",
		},
		{
			"items is not an array or object",
			`{"items": 1}`, `[1]`,
			SchemaInvalidSchema, "items attribute is invalid in schema",
		},
		{
			"additionalItems is not a bool or object",
			`{"items": [{}], "additionalItems": "no"}`, `[1, 2]`,
			SchemaInvalidSchema, "additionalItems attribute is invalid in schema",
		},
		{
			"undefined item",
			`{"items": [{}], "additionalItems": false}`, `[1, 2]`,
			SchemaConstraint, "array has undefined item",
		},
		{
			"too few items",
			`{"minItems": 3}`, `[1]`,
			SchemaConstraint, "array has not enough items: 1, minimum is: 3",
		},
		{
			"too many items",
			`{"maxItems": 1}`, `[1, 2]`,
			SchemaConstraint, "array has too many items: 2, maximum is: 1",
		},
		{
			"duplicate items",
			`{"uniqueItems": true}`, `[1, 1]`,
			SchemaConstraint, "duplicate values detected while uniqueItems is true",
		},
		{
			"multipleOf must be positive",
			`{"multipleOf": 0}`, `4`,
			SchemaInvalidSchema, "multipleOf must be greater than zero",
		},
		{
			"not a multiple",
			`{"multipleOf": 2}`, `5`,
			SchemaConstraint, "number 5.0000 is not multiple of 2.0000, remainder is 1.0000000",
		},
		{
			"too big",
			`{"maximum": 3}`, `4`,
			SchemaConstraint, "number is too big: 4.000, maximum is: 3.000",
		},
		{
			"too small",
			`{"minimum": 3}`, `2`,
			SchemaConstraint, "number is too small: 2.000, minimum is: 3.000",
		},
		{
			"string too big",
			`{"maxLength": 1}`, `"ab"`,
			SchemaConstraint, "string is too big: 2, maximum is: 1",
		},
		{
			"string too short",
			`{"minLength": 3}`, `"ab"`,
			SchemaConstraint, "string is too short: 2, minimum is: 3",
		},
		{
			"pattern mismatch",
			`{"pattern": "^a+$"}`, `"b"`,
			SchemaConstraint, "string doesn't match regexp ^a+$",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := validateText(t, tc.schema, tc.data)
			if ok {
				t.Fatalf("expected validation to fail")
			}
			if err.Code != tc.code {
				t.Errorf("code = %v, want %v", err.Code, tc.code)
			}
			if err.Msg != tc.msg {
				t.Errorf("msg  = %q\nwant   %q", err.Msg, tc.msg)
			}
			if err.Obj == nil {
				t.Errorf("Obj is nil; the C always names the offending node")
			}
			if err.Error() != err.Msg {
				t.Errorf("Error() = %q, want %q", err.Error(), err.Msg)
			}
		})
	}
}

// TestSchemaNonObjectSchema pins the check at the very top of
// ucl_schema_validate: draft-4 boolean schemas are not supported.
func TestSchemaNonObjectSchema(t *testing.T) {
	for _, schema := range []string{`true`, `[1]`, `"x"`} {
		ok, err := Validate(mustParse(t, "v = "+schema).Lookup("v"), FromInt(1))
		if ok {
			t.Fatalf("schema %s: expected failure", schema)
		}
		if err.Code != SchemaInvalidSchema || !strings.HasPrefix(err.Msg, "schema is ") {
			t.Errorf("schema %s: got %v %q", schema, err.Code, err.Msg)
		}
	}
}

// TestSchemaSuccessReturnsNoError checks that the "stale err after a
// successful anyOf/not" footgun documented in docs/reference/schema.md 5 cannot be hit
// through this API: success always yields a nil error.
func TestSchemaSuccessReturnsNoError(t *testing.T) {
	// The first anyOf branch fails and writes an error before the second
	// branch succeeds.
	ok, err := validateText(t, `{"anyOf": [{"type": "string"}, {"type": "integer"}]}`, `1`)
	if !ok || err != nil {
		t.Fatalf("got (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = validateText(t, `{"not": {"type": "string"}}`, `1`)
	if !ok || err != nil {
		t.Fatalf("not: got (%v, %v), want (true, nil)", ok, err)
	}
}

// TestSchemaMessageIsTruncated pins the C's fixed char msg[128] budget.
func TestSchemaMessageIsTruncated(t *testing.T) {
	long := strings.Repeat("a", 300)
	ok, err := validateText(t, `{"required": ["`+long+`"]}`, `{}`)
	if ok {
		t.Fatal("expected failure")
	}
	if len(err.Msg) != schemaErrMsgSize-1 {
		t.Errorf("len(msg) = %d, want %d", len(err.Msg), schemaErrMsgSize-1)
	}
}

// --- minValues / maxValues (UCL-only, no corpus coverage) ------------------

// implicitArray parses a UCL document with repeated keys and returns the head
// of the resulting chain.
func implicitArray(t *testing.T, n int) *Object {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "k = %d\n", i)
	}
	head := mustParse(t, b.String()).Lookup("k")
	got := 0
	for cur := head; cur != nil; cur = cur.Next {
		got++
	}
	if got != n {
		t.Fatalf("built a chain of %d, want %d", got, n)
	}
	return head
}

func TestSchemaMinMaxValues(t *testing.T) {
	// maxValues is off by one in the C: the count is compared before the
	// current element is counted, so maxValues:1 tolerates two values and
	// only rejects the third. Replicated deliberately.
	cases := []struct {
		schema string
		values int
		want   bool
	}{
		{`{"maxValues": 1}`, 1, true},
		{`{"maxValues": 1}`, 2, true},
		{`{"maxValues": 1}`, 3, false},
		{`{"minValues": 2}`, 1, false},
		{`{"minValues": 2}`, 2, true},
		{`{"minValues": 2}`, 3, true},
		// Non-integer constraints are ignored outright, not reported.
		{`{"maxValues": "1"}`, 5, true},
		{`{"minValues": 2.0}`, 1, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.schema, tc.values), func(t *testing.T) {
			got, err := Validate(mustParse(t, tc.schema), implicitArray(t, tc.values))
			if got != tc.want {
				t.Fatalf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

// TestSchemaImplicitArrayValidatesEveryElement pins the try_array loop: every
// value on the duplicate-key chain has to satisfy the schema.
func TestSchemaImplicitArrayValidatesEveryElement(t *testing.T) {
	head := mustParse(t, "k = 1\nk = \"two\"\n").Lookup("k")
	if ok, _ := Validate(mustParse(t, `{"type": "integer"}`), head); ok {
		t.Error("a chain with a string in it satisfied type:integer")
	}
	head = mustParse(t, "k = 1\nk = 2\n").Lookup("k")
	if ok, err := Validate(mustParse(t, `{"type": "integer"}`), head); !ok {
		t.Errorf("a chain of integers failed type:integer: %v", err)
	}
}

// --- $ref ------------------------------------------------------------------

func TestSchemaRefErrors(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		msg    string
	}{
		{"missing component", `{"$ref": "#/nope/x"}`, "reference nope/x is invalid, missing path component"},
		{"missing final component", `{"$ref": "#/nope"}`, "reference #/nope is invalid, cannot find specified object"},
		{"empty component", `{"$ref": "#//a"}`, "reference #//a is invalid, empty path component"},
		{"through a scalar", `{"a": 1, "$ref": "#/a/b/c"}`, "reference b/c is invalid, contains primitive object in the path"},
		{"not an object", `{"a": 1, "$ref": "#/a"}`, "reference #/a is invalid, cannot find specified object"},
		{"bad item number", `{"a": [{}], "$ref": "#/a/x/y"}`, "reference x/y is invalid, invalid item number"},
		{"item out of range", `{"a": [{}], "$ref": "#/a/4/y"}`, "reference 4/y is invalid, item number 4 does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := validateText(t, tc.schema, `{}`)
			if ok {
				t.Fatal("expected failure")
			}
			if err.Code != SchemaInvalidSchema || err.Msg != tc.msg {
				t.Errorf("got %v %q\nwant %v %q", err.Code, err.Msg, SchemaInvalidSchema, tc.msg)
			}
		})
	}
}

// TestSchemaRefNoPointerUnescaping pins the deliberate absence of RFC 6901
// ~0/~1 handling and of percent-decoding: components are raw bytes, which is
// what ref.json's "escaped pointer ref" suite relies on.
func TestSchemaRefNoPointerUnescaping(t *testing.T) {
	// The literal key "a~0b" resolves; the RFC 6901 spelling of "a~b" does not.
	if ok, err := validateText(t, `{"a~0b": {"type": "integer"}, "$ref": "#/a~0b"}`, `1`); !ok {
		t.Errorf("literal component did not resolve: %v", err)
	}
	if ok, _ := validateText(t, `{"a~b": {"type": "integer"}, "$ref": "#/a~0b"}`, `1`); ok {
		t.Error("~0 was unescaped to ~; it must not be")
	}
	if ok, _ := validateText(t, `{"a/b": {"type": "integer"}, "$ref": "#/a~1b"}`, `1`); ok {
		t.Error("~1 was unescaped to /; it must not be")
	}
}

// TestSchemaRefArrayIndex covers this port's divergence from the missing
// counter increment in ucl_schema_resolve_ref_component: in the C only index 0
// ever resolves, because `i` is never incremented while walking. ref.json uses
// "#/items/0" only, so the corpus cannot tell the two apart.
func TestSchemaRefArrayIndex(t *testing.T) {
	schema := `{
		"items": [{"type": "integer"}, {"type": "string"}],
		"properties": {"second": {"$ref": "#/items/1"}}
	}`
	if ok, err := validateText(t, schema, `{"second": "x"}`); !ok {
		t.Errorf("#/items/1 did not resolve: %v", err)
	}
	if ok, _ := validateText(t, schema, `{"second": 1}`); ok {
		t.Error("#/items/1 resolved to the wrong element")
	}
}

// TestSchemaRefWholeDocument pins that "#" denotes the whole root document.
// It only terminates because the enclosing "properties" step descends one
// level into the data on every hop; a bare top-level {"$ref": "#"} recurses
// forever, which is what TestSchemaRecursionGuard covers.
func TestSchemaRefWholeDocument(t *testing.T) {
	schema := `{"properties": {"foo": {"$ref": "#"}}, "additionalProperties": false}`
	if ok, err := validateText(t, schema, `{"foo": {"foo": false}}`); !ok {
		t.Errorf(`"$ref": "#" failed: %v`, err)
	}
	if ok, _ := validateText(t, schema, `{"foo": {"bar": false}}`); ok {
		t.Error(`"$ref": "#" did not re-apply the root schema`)
	}
}

// TestSchemaRefIsAdditive pins libucl's reading of $ref: sibling keywords are
// evaluated too, unlike strict draft-4 tooling where $ref wins alone.
func TestSchemaRefIsAdditive(t *testing.T) {
	schema := `{
		"definitions": {"i": {"type": "integer"}},
		"properties": {"v": {"$ref": "#/definitions/i", "maximum": 10}}
	}`
	if ok, err := validateText(t, schema, `{"v": 5}`); !ok {
		t.Errorf("expected valid: %v", err)
	}
	if ok, _ := validateText(t, schema, `{"v": 50}`); ok {
		t.Error("the sibling maximum was ignored; $ref must be additive")
	}
	if ok, _ := validateText(t, schema, `{"v": "x"}`); ok {
		t.Error("the $ref itself was ignored")
	}
}

// TestSchemaRefNonString covers the one defensive change to the $ref path:
// the C hands a non-string straight to ucl_object_tostring and dereferences
// the NULL it gets back.
func TestSchemaRefNonString(t *testing.T) {
	ok, err := validateText(t, `{"$ref": 1}`, `{}`)
	if ok {
		t.Fatal("expected failure")
	}
	if err.Code != SchemaInvalidSchema || err.Msg != "$ref is integer instead of string" {
		t.Errorf("got %v %q", err.Code, err.Msg)
	}
}

// TestSchemaRecursionGuard covers the depth limit this port adds. libucl
// recurses forever on this schema and overflows its stack.
func TestSchemaRecursionGuard(t *testing.T) {
	ok, err := validateText(t, `{"$ref": "#"}`, `1`)
	if ok {
		t.Fatal("expected failure")
	}
	if err.Code != SchemaInternalError || !strings.Contains(err.Msg, "too deep") {
		t.Errorf("got %v %q, want the depth guard", err.Code, err.Msg)
	}
}

// countingFetcher records what a validation asked for.
type countingFetcher struct {
	docs  map[string][]byte
	calls []string
}

func (f *countingFetcher) FetchRef(ref string) ([]byte, error) {
	f.calls = append(f.calls, ref)
	doc, ok := f.docs[ref]
	if !ok {
		return nil, fmt.Errorf("no such document")
	}
	return doc, nil
}

// TestSchemaExternalRefCache pins the external-ref path: the cache is keyed by
// the raw pre-'#' substring, a caller-supplied cache survives across calls, and
// refs inside the fetched document resolve against that document.
func TestSchemaExternalRefCache(t *testing.T) {
	fetcher := &countingFetcher{docs: map[string][]byte{
		"other.json": []byte(`{"definitions": {"i": {"type": "integer"}}, "$ref": "#/definitions/i"}`),
	}}
	schema := mustParse(t, `{"properties": {
		"a": {"$ref": "other.json#/definitions/i"},
		"b": {"$ref": "other.json"}
	}}`)

	extRefs := TypedNew(TypeObject)
	for i := 0; i < 2; i++ {
		ok, err := ValidateRootExtFetch(schema, mustParse(t, `{"a": 1, "b": 2}`), schema, extRefs, fetcher)
		if !ok {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "other.json" {
		t.Errorf("fetches = %v, want one fetch of other.json", fetcher.calls)
	}
	if extRefs.Lookup("other.json") == nil {
		t.Error("the fetched document was not cached under its raw url")
	}
	// "b" resolves to the whole document, whose own "#/definitions/i" must
	// resolve inside it rather than against the original root.
	if ok, _ := ValidateRootExtFetch(schema, mustParse(t, `{"b": "x"}`), schema, extRefs, fetcher); ok {
		t.Error("the nested ref in the fetched document was not applied")
	}
}

func TestSchemaExternalRefFetchFailure(t *testing.T) {
	schema := mustParse(t, `{"$ref": "missing.json"}`)
	ok, err := ValidateRootExtFetch(schema, FromInt(1), schema, nil, &countingFetcher{})
	if ok {
		t.Fatal("expected failure")
	}
	if err.Code != SchemaInvalidSchema || !strings.HasPrefix(err.Msg, "cannot fetch reference missing.json: ") {
		t.Errorf("got %v %q", err.Code, err.Msg)
	}
}

// TestSchemaLocalRefFetcher covers the default fetcher: local files are read
// as given, and anything that looks like a URL is refused rather than fetched.
func TestSchemaLocalRefFetcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub.json")
	if err := os.WriteFile(path, []byte(`{"definitions": {"i": {"type": "integer"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schema := mustParse(t, fmt.Sprintf(`{"$ref": %q}`, path+"#/definitions/i"))
	if ok, err := Validate(schema, FromInt(1)); !ok {
		t.Errorf("local file ref failed: %v", err)
	}
	if ok, _ := Validate(schema, FromString("x")); ok {
		t.Error("local file ref resolved to the wrong schema")
	}

	remote := mustParse(t, `{"$ref": "http://example.invalid/s.json"}`)
	ok, err := Validate(remote, FromInt(1))
	if ok {
		t.Fatal("expected a remote ref to be refused")
	}
	if !strings.Contains(err.Msg, "remote references is not supported") {
		t.Errorf("got %q, want the remote-fetch refusal", err.Msg)
	}
}

// TestSchemaValidateRoot pins the second entry point: the sub-schema is what
// validates, but "#" refs resolve against the separate root.
func TestSchemaValidateRoot(t *testing.T) {
	root := mustParse(t, `{"definitions": {"i": {"type": "integer"}}}`)
	sub := mustParse(t, `{"$ref": "#/definitions/i"}`)
	if ok, err := ValidateRoot(sub, FromInt(1), root); !ok {
		t.Errorf("expected valid: %v", err)
	}
	if ok, _ := ValidateRoot(sub, FromString("x"), root); ok {
		t.Error("expected invalid")
	}
	// Without the root, the ref cannot resolve at all.
	if ok, _ := Validate(sub, FromInt(1)); ok {
		t.Error("the ref resolved without its root document")
	}
}

// --- regexp ----------------------------------------------------------------

// TestSchemaPatternCompileFailure covers both halves of this port's regexp
// posture: Perl-only syntax is rejected rather than quietly given RE2's
// meaning, and an uncompilable patternProperties key is reported instead of
// being swallowed the way ucl_schema_test_pattern swallows it.
func TestSchemaPatternCompileFailure(t *testing.T) {
	for _, tc := range []struct{ name, schema, data string }{
		{"pattern", `{"pattern": "(?:a)"}`, `"a"`},
		{"patternProperties", `{"patternProperties": {"(?:a)": {}}}`, `{"a": 1}`},
		{"additionalProperties sweep", `{"patternProperties": {"(?:a)": {}}, "additionalProperties": false}`, `{"b": 1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := validateText(t, tc.schema, tc.data)
			if ok {
				t.Fatal("expected failure")
			}
			if err.Code != SchemaInvalidSchema || !strings.HasPrefix(err.Msg, "cannot compile pattern ") {
				t.Errorf("got %v %q", err.Code, err.Msg)
			}
		})
	}
}

// TestSchemaPatternIsPOSIX pins the flavour: POSIX character classes work,
// matching is an unanchored, case-sensitive search.
func TestSchemaPatternIsPOSIX(t *testing.T) {
	for _, tc := range []struct {
		pattern, data string
		want          bool
	}{
		{"[[:digit:]]+", `"a1b"`, true},
		{"^[[:digit:]]+$", `"a1b"`, false},
		{"X_", `"a_X_3"`, true},
		{"X_", `"a_x_3"`, false},
		{"[0-9]{2,}", `"a31b"`, true},
	} {
		schema := fmt.Sprintf(`{"pattern": %q}`, tc.pattern)
		got, err := validateText(t, schema, tc.data)
		if got != tc.want {
			t.Errorf("pattern %q against %s: got %v (%v), want %v", tc.pattern, tc.data, got, err, tc.want)
		}
	}
}

// --- deliberate departures from the C --------------------------------------

// TestSchemaExclusiveBoundsDoNotLeak covers the divergence in
// ucl_schema_validate_number, where `exclusive` is hoisted out of the keyword
// loop and only ever assigned, so an exclusiveMaximum leaks into a "minimum"
// visited later and silently makes it exclusive too.
func TestSchemaExclusiveBoundsDoNotLeak(t *testing.T) {
	// "maximum" is declared first, so it is visited first and would leave
	// exclusive set when "minimum" is reached.
	schema := `{"maximum": 10, "exclusiveMaximum": true, "minimum": 5}`
	if ok, err := validateText(t, schema, `5`); !ok {
		t.Errorf("the inclusive minimum rejected its own boundary: %v", err)
	}
	if ok, _ := validateText(t, schema, `10`); ok {
		t.Error("exclusiveMaximum was not applied")
	}
	if ok, _ := validateText(t, schema, `4`); ok {
		t.Error("minimum was not applied")
	}
}

// TestSchemaAdditionalPropertiesPatternIsPerKey covers the divergence in the
// additionalProperties sweep: the C asks whether *any* key of the object
// matches a patternProperties pattern, so a single matching key exempts every
// other key from the check.
func TestSchemaAdditionalPropertiesPatternIsPerKey(t *testing.T) {
	schema := `{
		"properties": {"foo": {}},
		"patternProperties": {"^v": {}},
		"additionalProperties": false
	}`
	// "vroom" matches the pattern; "quux" matches nothing and must be denied
	// even though a sibling key does match.
	ok, err := validateText(t, schema, `{"foo": 1, "vroom": 2, "quux": 3}`)
	if ok {
		t.Error("quux was exempted by vroom matching the pattern")
	} else if err.Msg != "object has non-allowed property quux" {
		t.Errorf("got %q, want the quux message", err.Msg)
	}
	if ok, err := validateText(t, schema, `{"foo": 1, "vroom": 2}`); !ok {
		t.Errorf("a pattern-covered property was denied: %v", err)
	}
}

// TestSchemaAdditionalItemsValidatedOnce checks that each additional item is
// validated, which the C's post-increment loop also achieves (it just
// validates the first one twice).
func TestSchemaAdditionalItemsValidatedOnce(t *testing.T) {
	schema := `{"items": [{"type": "integer"}], "additionalItems": {"type": "string"}}`
	if ok, err := validateText(t, schema, `[1, "a", "b"]`); !ok {
		t.Errorf("expected valid: %v", err)
	}
	for _, data := range []string{`[1, 2, "b"]`, `[1, "a", 2]`} {
		if ok, _ := validateText(t, schema, data); ok {
			t.Errorf("%s: expected invalid", data)
		}
	}
}

// --- keywords libucl does not implement ------------------------------------

// TestSchemaUnimplementedKeywordsAreInert pins the draft-4 and draft-6+
// keywords ucl_schema.c never reads. They must not fail validation and must
// not constrain anything.
func TestSchemaUnimplementedKeywordsAreInert(t *testing.T) {
	inert := []string{
		`{"default": 42}`,
		`{"format": "email"}`,
		`{"title": "t", "description": "d"}`,
		`{"id": "http://example.invalid/schema#"}`,
		`{"$schema": "http://json-schema.org/draft-04/schema#"}`,
		// draft-6+: none of these are recognised.
		`{"const": 1}`,
		`{"contains": {"type": "integer"}}`,
		`{"propertyNames": {"maxLength": 1}}`,
		`{"if": {"type": "string"}, "then": {"maxLength": 1}}`,
		// A numeric exclusiveMaximum is draft-6 syntax and is ignored, so the
		// data is unconstrained rather than rejected.
		`{"exclusiveMaximum": 1}`,
	}
	for _, schema := range inert {
		t.Run(schema, func(t *testing.T) {
			if ok, err := validateText(t, schema, `{"anything": "at all"}`); !ok {
				t.Errorf("expected the keyword to be inert: %v", err)
			}
		})
	}
	// "definitions" gets no special handling either: it is an ordinary
	// property that happens to be where $ref pointers land.
	if ok, err := validateText(t, `{"definitions": {"a": "not a schema"}}`, `1`); !ok {
		t.Errorf("definitions was validated as a schema container: %v", err)
	}
}

// TestSchemaMalformedConstraintsAreIgnored pins the soft-fail style: libucl
// checks the type of a constraint value and silently skips the keyword rather
// than reporting a bad schema, which draft-4 would.
func TestSchemaMalformedConstraintsAreIgnored(t *testing.T) {
	cases := []struct{ schema, data string }{
		{`{"minProperties": "3"}`, `{"a": 1}`},
		{`{"maxItems": "1"}`, `[1, 2, 3]`},
		{`{"maximum": "1"}`, `5`},
		{`{"minimum": [1]}`, `0`},
		{`{"multipleOf": "2"}`, `5`},
		// maxLength/minLength take an integer only: no float coercion, unlike
		// the numeric keywords.
		{`{"maxLength": 1.0}`, `"abcdef"`},
		{`{"uniqueItems": "true"}`, `[1, 1]`},
		{`{"not": ["anything"]}`, `1`},
		{`{"dependencies": {"a": "b"}}`, `{"a": 1}`},
		{`{"enum": "abc"}`, `1`},
		{`{"allOf": {"type": "string"}}`, `1`},
	}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			if ok, err := validateText(t, tc.schema, tc.data); !ok {
				t.Errorf("expected the malformed keyword to be ignored: %v", err)
			}
		})
	}
	// minProperties does accept a float or a time, truncating toward zero.
	if ok, _ := validateText(t, `{"minProperties": 3.9}`, `{"a": 1, "b": 2, "c": 3}`); !ok {
		t.Error("a float minProperties was not truncated to 3")
	}
}

// TestSchemaScalarsHaveNoBodyValidator pins the switch at the bottom of
// ucl_schema_validate: booleans and nulls reach no per-type validator, so
// string and number keywords simply do not apply to them.
func TestSchemaScalarsHaveNoBodyValidator(t *testing.T) {
	for _, tc := range []struct{ schema, data string }{
		{`{"minLength": 100}`, `true`},
		{`{"minimum": 100}`, `null`},
		{`{"pattern": "^a$"}`, `false`},
	} {
		if ok, err := validateText(t, tc.schema, tc.data); !ok {
			t.Errorf("%s against %s: expected valid: %v", tc.schema, tc.data, err)
		}
	}
}

// TestSchemaErrorCodeNames keeps the enum spelling aligned with the C, since
// the numeric values are the compatibility contract.
func TestSchemaErrorCodeNames(t *testing.T) {
	want := []string{
		"UCL_SCHEMA_OK",
		"UCL_SCHEMA_TYPE_MISMATCH",
		"UCL_SCHEMA_INVALID_SCHEMA",
		"UCL_SCHEMA_MISSING_PROPERTY",
		"UCL_SCHEMA_CONSTRAINT",
		"UCL_SCHEMA_MISSING_DEPENDENCY",
		"UCL_SCHEMA_EXTERNAL_REF_MISSING",
		"UCL_SCHEMA_EXTERNAL_REF_INVALID",
		"UCL_SCHEMA_INTERNAL_ERROR",
		"UCL_SCHEMA_UNKNOWN",
	}
	for i, name := range want {
		if got := SchemaErrorCode(i).String(); got != name {
			t.Errorf("code %d = %q, want %q", i, got, name)
		}
	}
}
