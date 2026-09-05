package goucl

// Parser unit tests for behaviour the testdata/basic goldens cannot reach:
// the parser flags test_basic never sets, the duplicate strategies, user
// macros, the variable handler and the file-based entry points.
//
// Every expected value here was captured from libucl 0.9.4 through a small
// C driver, not derived from this implementation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseConfig parses src and renders it with the CONFIG emitter, which is the
// shape the C oracle printed.
func parseConfig(t *testing.T, src string, flags ParserFlag, strat DuplicateStrategy) (string, error) {
	t.Helper()
	p := NewParser(flags)
	p.RegisterVariable("ABI", "unknown")
	p.AddChunkFull([]byte(src), 0, strat, ParseUCL)
	if err := p.GetError(); err != nil {
		return "", err
	}
	out, emitErr := EmitTextString(p.GetObject(), EmitConfig)
	if emitErr != nil {
		t.Fatalf("emit: %v", emitErr)
	}
	return out, nil
}

const dupSource = "a = 1\na = 2\na = 3\nb = [1]\nb = 2\nc = 10s\nd = 10ms\ne = 1kb\n"

func TestParserDuplicateStrategies(t *testing.T) {
	cases := []struct {
		name  string
		strat DuplicateStrategy
		want  string
	}{
		{"append", DuplicateAppend, "a = 1;\na = 2;\na = 3;\nb [\n    1,\n]\nb = 2;\nc = 10.0;\nd = 0.010000;\ne = 1024;\n"},
		// Merge hands the existing container to the value parser, so the
		// second "b" overwrites the array in place.
		{"merge", DuplicateMerge, "a = 1;\na = 2;\na = 3;\nb = 2;\nc = 10.0;\nd = 0.010000;\ne = 1024;\n"},
		{"rewrite", DuplicateRewrite, "a = 3;\nb = 2;\nc = 10.0;\nd = 0.010000;\ne = 1024;\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfig(t, dupSource, 0, tc.strat)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}

	t.Run("error", func(t *testing.T) {
		_, err := parseConfig(t, dupSource, 0, DuplicateError)
		want := "error while parsing <unknown>: line: 2, column: 4 - " +
			"'duplicate element for key 'a' found', character: '2'"
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	})
}

func TestParserNoImplicitArrays(t *testing.T) {
	// The first duplicate converts the entry into a real array; a literal
	// array has no MULTIVALUE flag, so "b" is wrapped in a second array.
	want := "a [\n    1,\n    2,\n    3,\n]\nb [\n    [\n        1,\n    ]\n    2,\n]\n" +
		"c = 10.0;\nd = 0.010000;\ne = 1024;\n"
	got, err := parseConfig(t, dupSource, ParserNoImplicitArrays, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParserNoTime(t *testing.T) {
	// "10s" becomes a string, but "10ms" still yields a time: the ms branch of
	// the number lexer is not guarded by allow_time.
	got, err := parseConfig(t, "c = 10s\nd = 10ms\ne = 1kb\nf = 10min\ng = 3w\n",
		ParserNoTime, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "c = \"10s\";\nd = 0.010000;\ne = 1024;\nf = \"10min\";\ng = \"3w\";\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParserNumbers(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"n = -0xdeadbeef", "n = -3735928559;\n"},
		{"n = 0xdeadbeef.1", "n = \"0xdeadbeef.1\";\n"},
		{"n = 0xreadbeef", "n = \"0xreadbeef\";\n"},
		{"n = 1.5k", "n = 1500.0;\n"},
		{"n = 10min", "n = 600.0;\n"},
		{"n = 1k", "n = 1000;\n"},
		{"n = 12abc", "n = \"12abc\";\n"},
		{"n = 1.2.3", "n = \"1.2.3\";\n"},
		{"n = inf", "n = inf;\n"},
		{"n = -inf", "n = \"-inf\";\n"},
		{"n = null", "n = null;\n"},
		{"n = 00x10", "n = 16;\n"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := parseConfig(t, tc.src+"\n", 0, DuplicateAppend)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("range", func(t *testing.T) {
		// ERANGE is fatal rather than a fallback to a string.
		_, err := parseConfig(t, "a = 99999999999999999999999\n", 0, DuplicateAppend)
		if err == nil || !strings.Contains(err.Error(), "numeric value out of range") {
			t.Errorf("got %v, want a range error", err)
		}
	})
}

func TestParserHeredoc(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"simple", "x = <<EOD\nline\nEOD\n", "x = \"line\";\n"},
		// The terminator must be followed by end of input, newline, ';' or
		// ','; "ABCD" is content, not a terminator.
		{"partial terminator", "x = <<ABC\nline\nABCD\nABC\n", "x = <<EOD\nline\nABCD\nEOD;\n"},
		{"no trailing newline", "x = <<EOD\nvalue\nEOD", "x = \"value\";\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfig(t, tc.src, 0, DuplicateAppend)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("empty is an error", func(t *testing.T) {
		_, err := parseConfig(t, "x = <<E\nE\n", 0, DuplicateAppend)
		if err == nil || !strings.Contains(err.Error(), "unterminated multiline value") {
			t.Errorf("got %v, want an unterminated-multiline error", err)
		}
	})

	t.Run("variables are expanded", func(t *testing.T) {
		got, err := parseConfig(t, "x = <<EOD\nbody $ABI here\nEOD\n", 0, DuplicateAppend)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "body unknown here") {
			t.Errorf("heredoc was not expanded: %q", got)
		}
	})
}

func TestParserErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"bad key start", "= 5\n",
			"error while parsing <unknown>: line: 1, column: 0 - 'key must begin with a letter', character: '='"},
		{"missing delimiter", "a = \"x\" \"y\"\n",
			"error while parsing <unknown>: line: 1, column: 8 - 'delimiter is missing', character: '\"'"},
		// The stack still holds the root frame, so this is the unpaired-brace
		// message rather than "without corresponding start".
		{"unmatched close", "a = b\n}\n",
			"memory:2 object closed with } is not opened with { at line 1"},
		{"unterminated string", "a = \"unterminated\n",
			"error while parsing <unknown>: line: 1, column: 17 - 'unexpected newline', character: '0x0a'"},
		{"unterminated comment", "/* unterminated\n",
			"error while parsing <unknown>: at the end of chunk: unfinished multiline comment"},
		// The cursor has already moved past the separator by the time the key
		// is built, so the reported character is the value's first byte.
		{"empty key", "\"\" = 1\n",
			"error while parsing <unknown>: line: 1, column: 5 - 'empty keys are not allowed', character: '1'"},
		// Produced by ucl_create_err, so it carries no "error while parsing"
		// prefix and names the chunk rather than parser->cur_file.
		{"unknown macro", ".nosuch \"x\"\n",
			"error at memory:1 at column 7: unknown macro: 'nosuch', character: ' '"},
		{"invalid macro name", ". \"x\"\n",
			"error at memory:1 at column 1: invalid macro name"},
		{"unmatched open brace", "a {\nb = 1\n",
			"memory:3 unmatched open brace at 1; "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseConfig(t, tc.src, 0, DuplicateAppend)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if err.Error() != tc.want {
				t.Errorf("got  %q\nwant %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParserDisableMacroRejectsMacroLines(t *testing.T) {
	// ParserDisableMacro does not make macros behave like comments: the
	// leading '.' is then lexed as a key and rejected (findings.md item 2).
	_, err := parseConfig(t, ".include \"x\"\n", ParserDisableMacro, DuplicateAppend)
	want := "error while parsing <unknown>: line: 1, column: 0 - 'key must begin with a letter', character: '.'"
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

func TestParserDisableMacroDisablesVariables(t *testing.T) {
	got, err := parseConfig(t, "a = \"$ABI\"\n", ParserDisableMacro, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a = \"$ABI\";\n" {
		t.Errorf("variables were expanded despite ParserDisableMacro: %q", got)
	}
}

func TestParserUserMacros(t *testing.T) {
	type call struct {
		data string
		args string
	}
	var calls []call

	p := NewParser(0)
	p.RegisterVariable("ABI", "unknown")
	record := func(data []byte, args *Object) bool {
		a := "nil"
		if args != nil {
			e, _ := EmitTextString(args, EmitJSONCompact)
			a = e
		}
		calls = append(calls, call{string(data), a})
		return true
	}
	p.RegisterMacro("mymacro", record)

	var ctxType string
	p.RegisterContextMacro("myctx", func(data []byte, args, ctx *Object) bool {
		ctxType = ctx.TypeString()
		return true
	})

	src := ".mymacro \"hello world\"\n" +
		".mymacro(a=1, b=\"two\", c=[1,2]) { braced body }\n" +
		".mymacro bare_atom;\n" +
		"k = 1\n" +
		".myctx \"ctx\"\n"
	if !p.AddString(src) {
		t.Fatalf("parse failed: %v", p.GetError())
	}

	want := []call{
		{"hello world", "nil"},
		// The braced form keeps trailing spaces up to the closing brace.
		{"braced body ", `{"a":1,"b":"two","c":[1,2]}`},
		{"bare_atom", "nil"},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d macro calls, want %d: %#v", len(calls), len(want), calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d: got %#v, want %#v", i, calls[i], want[i])
		}
	}
	if ctxType != "object" {
		t.Errorf("context macro saw %q, want object", ctxType)
	}
}

func TestParserMacroFailureStopsChunkWithoutError(t *testing.T) {
	// A handler returning false aborts the chunk but records no error, which
	// is how ".try_include" of a missing file silently truncates a file.
	p := NewParser(0)
	p.RegisterMacro("myfail", func(data []byte, args *Object) bool { return false })
	if p.AddString("a = 1\n.myfail \"x\"\nb = 2\n") {
		t.Errorf("AddString should report failure")
	}
	if err := p.GetError(); err != nil {
		t.Errorf("no error should be recorded, got %v", err)
	}
	got, _ := EmitTextString(p.GetObject(), EmitConfig)
	if got != "a = 1;\n" {
		t.Errorf("got %q, want the content before the macro only", got)
	}
}

func TestParserUserMacroShadowsBuiltin(t *testing.T) {
	// uthash returns the most recently registered entry, so a user handler
	// registered under a built-in name wins.
	called := false
	p := NewParser(0)
	p.RegisterMacro("include", func(data []byte, args *Object) bool {
		called = true
		return true
	})
	if !p.AddString(".include \"whatever\"\n") {
		t.Fatalf("parse failed: %v", p.GetError())
	}
	if !called {
		t.Errorf("the user's include handler was not called")
	}
}

func TestParserVariables(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`v = "$ABItest"`, "v = \"unknowntest\";\n"},
		{`v = "${ABI}$ABI${ABI}${$ABI}"`, "v = \"unknownunknownunknown${unknown}\";\n"},
		// Nothing resolves, so the whole string is left alone -- "$$" is not
		// even collapsed.
		{`v = "${some}$no${}$$test$$$$$$$"`, "v = \"${some}$no${}$$test$$$$$$$\";\n"},
		{`v = "$ABI$$ABI$$$ABI$$$$"`, "v = \"unknown$ABI$unknown$$\";\n"},
		// Single quotes are never expanded.
		{`v = '$ABI'`, "v = '$ABI';\n"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := parseConfig(t, tc.src+"\n", 0, DuplicateAppend)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParserVariableHandler(t *testing.T) {
	var seen []string
	p := NewParser(0)
	p.SetVariablesHandler(func(d []byte) ([]byte, bool) {
		seen = append(seen, string(d))
		return []byte("HANDLED"), true
	})
	if !p.AddString("v = \"${SOMETHING}\"\nw = \"$SOMETHING\"\n") {
		t.Fatalf("parse failed: %v", p.GetError())
	}
	got, _ := EmitTextString(p.GetObject(), EmitConfig)
	// Only the braced form reaches the handler; the loose form is left alone.
	want := "v = \"HANDLED\";\nw = \"$SOMETHING\";\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The measuring pass sees the bare name, the expansion pass the rest of
	// the string: the asymmetry is real and documented.
	wantSeen := []string{"SOMETHING", "SOMETHING}"}
	if len(seen) != 2 || seen[0] != wantSeen[0] || seen[1] != wantSeen[1] {
		t.Errorf("handler saw %#v, want %#v", seen, wantSeen)
	}
}

func TestParserRegisterAndUnregisterVariable(t *testing.T) {
	p := NewParser(ParserNoFilevars)
	p.RegisterVariable("V", "one")
	p.RegisterVariable("V", "two")
	if len(p.variables) != 1 {
		t.Fatalf("re-registering should replace in place, got %d variables", len(p.variables))
	}
	if !p.AddString("a = \"${V}\"\n") {
		t.Fatalf("parse failed: %v", p.GetError())
	}
	if got, _ := EmitTextString(p.GetObject(), EmitConfig); got != "a = \"two\";\n" {
		t.Errorf("got %q", got)
	}

	p2 := NewParser(ParserNoFilevars)
	p2.RegisterVariable("V", "one")
	p2.UnregisterVariable("V")
	p2.AddString("a = \"${V}\"\n")
	if got, _ := EmitTextString(p2.GetObject(), EmitConfig); got != "a = \"${V}\";\n" {
		t.Errorf("got %q, want the variable left verbatim", got)
	}
}

func TestParserNoFilevars(t *testing.T) {
	p := NewParser(ParserNoFilevars)
	if len(p.variables) != 0 {
		t.Errorf("ParserNoFilevars should suppress FILENAME and CURDIR, got %#v", p.variables)
	}
	q := NewParser(0)
	if len(q.variables) != 2 || q.variables[0].name != "FILENAME" ||
		q.variables[0].value != "undef" || q.variables[1].name != "CURDIR" {
		t.Errorf("FILENAME/CURDIR should be pre-registered in that order, got %#v", q.variables)
	}
}

func TestParserIncludes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("sub.conf", "inc = 1\n")
	write("braced.conf", "{\n bracedkey = 2\n}\n")
	main := write("main.conf", ""+
		".include \"${CURDIR}/sub.conf\"\n"+
		".include \"${CURDIR}/braced.conf\"\n"+
		"top = 3\n")

	p := NewParser(0)
	if !p.AddFile(main) {
		t.Fatalf("AddFile failed: %v", p.GetError())
	}
	got, _ := EmitTextString(p.GetObject(), EmitConfig)
	// A braced include merges flat: its '{' marks the caller's frame
	// automatic, so the matching '}' clears the flags instead of popping.
	want := "inc = 1;\nbracedkey = 2;\ntop = 3;\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	t.Run("try_include truncates the rest of the file", func(t *testing.T) {
		// .try_include sets soft_fail, and a soft failure returns false from
		// the macro handler, which aborts the chunk without recording an
		// error. Everything after the failed include is silently dropped.
		trunc := write("trunc.conf", ""+
			"before = 1\n"+
			".try_include \"${CURDIR}/absent.conf\"\n"+
			"after = 2\n")
		q := NewParser(0)
		if q.AddFile(trunc) {
			t.Errorf("AddFile should report failure")
		}
		if err := q.GetError(); err != nil {
			t.Errorf("no error should be recorded, got %v", err)
		}
		got, _ := EmitTextString(q.GetObject(), EmitConfig)
		if got != "before = 1;\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("include with try=true is a silent no-op", func(t *testing.T) {
		// ".include(try=true)" clears must_exist but leaves soft_fail unset,
		// so a missing file is skipped and parsing continues.
		opt := write("opt.conf", ""+
			"before = 1\n"+
			".include(try=true) \"${CURDIR}/absent.conf\"\n"+
			"after = 2\n")
		q := NewParser(0)
		if !q.AddFile(opt) {
			t.Fatalf("AddFile failed: %v", q.GetError())
		}
		got, _ := EmitTextString(q.GetObject(), EmitConfig)
		if got != "before = 1;\nafter = 2;\n" {
			t.Errorf("got %q", got)
		}
	})
}

func TestParserIncludePrefix(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub.conf")
	if err := os.WriteFile(sub, []byte("inc = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.conf")
	body := "" +
		".include(prefix=true) \"${CURDIR}/sub.conf\"\n" +
		".include(key=\"arr\"; target=\"array\") \"${CURDIR}/sub.conf\"\n" +
		".include(key=\"arr\"; target=\"array\") \"${CURDIR}/sub.conf\"\n"
	if err := os.WriteFile(main, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewParser(0)
	if !p.AddFile(main) {
		t.Fatalf("AddFile failed: %v", p.GetError())
	}
	got, _ := EmitTextString(p.GetObject(), EmitConfig)
	// prefix=true derives the key from the basename with .conf stripped.
	want := "sub {\n    inc = 1;\n}\narr [\n    {\n        inc = 1;\n    }\n    {\n        inc = 1;\n    }\n]\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParserIncludeSelfAndNesting(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self.conf")
	if err := os.WriteFile(self, []byte(".include \"${CURDIR}/self.conf\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewParser(0)
	p.AddFile(self)
	if err := p.GetError(); err == nil || !strings.Contains(err.Error(), "from itself") {
		t.Errorf("got %v, want a self-include error", err)
	}

	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	os.WriteFile(a, []byte(".include \"${CURDIR}/b.conf\"\n"), 0o644)
	os.WriteFile(b, []byte(".include \"${CURDIR}/a.conf\"\n"), 0o644)
	q := NewParser(0)
	q.AddFile(a)
	if err := q.GetError(); err == nil ||
		err.Error() != "maximum include nesting limit is reached: 17" {
		t.Errorf("got %v, want the nesting limit message", err)
	}
}

func TestParserLoadMacro(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(payload, []byte("  42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.conf")
	os.WriteFile(main, []byte(""+
		".load(key=\"raw\") \"${CURDIR}/payload.txt\"\n"+
		".load(key=\"trimmed\", trim=true) \"${CURDIR}/payload.txt\"\n"+
		".load(key=\"number\", target=\"int\") \"${CURDIR}/payload.txt\"\n"), 0o644)

	p := NewParser(0)
	if !p.AddFile(main) {
		t.Fatalf("AddFile failed: %v", p.GetError())
	}
	obj := p.GetObject()
	if got := ToString(obj.Lookup("raw")); got != "  42\n" {
		t.Errorf("raw = %q", got)
	}
	if got := ToString(obj.Lookup("trimmed")); got != "42" {
		t.Errorf("trimmed = %q", got)
	}
	if got := ToInt(obj.Lookup("number")); got != 42 {
		t.Errorf("number = %d", got)
	}

	t.Run("duplicate key", func(t *testing.T) {
		dup := filepath.Join(dir, "dup.conf")
		os.WriteFile(dup, []byte(""+
			".load(key=\"k\") \"${CURDIR}/payload.txt\"\n"+
			".load(key=\"k\") \"${CURDIR}/payload.txt\"\n"), 0o644)
		q := NewParser(0)
		q.AddFile(dup)
		if err := q.GetError(); err == nil || err.Error() != "Key k already exists" {
			t.Errorf("got %v", err)
		}
	})

	t.Run("no key", func(t *testing.T) {
		bad := filepath.Join(dir, "nokey.conf")
		os.WriteFile(bad, []byte(".load \"${CURDIR}/payload.txt\"\n"), 0o644)
		q := NewParser(0)
		q.AddFile(bad)
		if err := q.GetError(); err == nil || err.Error() != "No Key specified in load macro" {
			t.Errorf("got %v", err)
		}
	})
}

func TestParserInherit(t *testing.T) {
	src := "" +
		"base { x = 1; y = 2 }\n" +
		"d1 { .inherit \"base\"; x = 9 }\n" +
		"d2 { .inherit(replace=true) \"base\"; x = 9 }\n"
	got, err := parseConfig(t, src, 0, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without replace the copies are flagged inherited, so the later real
	// assignment wins outright; with replace the copy lands after it and
	// forms an implicit array.
	want := "base {\n    x = 1;\n    y = 2;\n}\nd1 {\n    x = 9;\n    y = 2;\n}\n" +
		"d2 {\n    x = 1;\n    x = 9;\n    y = 2;\n}\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	t.Run("missing parent", func(t *testing.T) {
		_, err := parseConfig(t, "a { .inherit \"nothing\" }\n", 0, DuplicateAppend)
		if err == nil || err.Error() != "Unable to find inherited object nothing" {
			t.Errorf("got %v", err)
		}
	})
}

func TestParserPriorityMacro(t *testing.T) {
	src := "a = 1;\n.priority 5\na = 2;\nb = 3;\n"
	got, err := parseConfig(t, src, 0, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The higher-priority value replaces the old one in place, keeping its
	// position in the emission order.
	want := "a = 2;\nb = 3;\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	t.Run("lower priority is discarded", func(t *testing.T) {
		got, err := parseConfig(t, ".priority 5\na = 1;\n.priority 1\na = 2;\n", 0, DuplicateAppend)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "a = 1;\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("argument form swallows the next line as its body", func(t *testing.T) {
		// ".priority(priority=2)" with nothing after it on the line is a trap:
		// the macro value parser skips the newline and reads the next line.
		_, err := parseConfig(t, ".priority(priority=2)\nb = 4;\n", 0, DuplicateAppend)
		if err == nil || err.Error() != "Invalid priority value in macro: b = 4" {
			t.Errorf("got %v", err)
		}
	})

	t.Run("invalid value", func(t *testing.T) {
		_, err := parseConfig(t, ".priority notanumber\n", 0, DuplicateAppend)
		if err == nil || err.Error() != "Invalid priority value in macro: notanumber" {
			t.Errorf("got %v", err)
		}
	})
	t.Run("no value", func(t *testing.T) {
		_, err := parseConfig(t, "a = 1\n.priority ;\n", 0, DuplicateAppend)
		if err == nil || err.Error() != "Unable to parse priority macro" {
			t.Errorf("got %v", err)
		}
	})
}

func TestParserComments(t *testing.T) {
	p := NewParser(ParserSaveComments)
	src := "# leading\nkey = 1 # trailing\n"
	if !p.AddString(src) {
		t.Fatalf("parse failed: %v", p.GetError())
	}
	comments := p.GetComments()
	if len(comments) == 0 {
		t.Fatalf("no comments were saved")
	}
	var texts []string
	for _, chain := range comments {
		for cur := chain; cur != nil; cur = cur.Next {
			texts = append(texts, string(cur.sv))
		}
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "# leading") || !strings.Contains(joined, "# trailing") {
		t.Errorf("saved comments were %q", joined)
	}

	q := NewParser(0)
	q.AddString(src)
	if q.GetComments() != nil {
		t.Errorf("comments should be nil without ParserSaveComments")
	}
}

func TestParserNestedComments(t *testing.T) {
	// Block comments nest, and a double quote inside one suppresses "*/".
	got, err := parseConfig(t, "/* a /* b */ c */ key = 1\n", 0, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "key = 1;\n" {
		t.Errorf("got %q", got)
	}
	got, err = parseConfig(t, "/* key = \"*/value\" */ other = 2\n", 0, DuplicateAppend)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "other = 2;\n" {
		t.Errorf("got %q", got)
	}
}

func TestParserUnescapers(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		cases := []struct{ in, want string }{
			{`a\nb`, "a\nb"},
			{`a\qb`, "aqb"},
			{`a\/b`, "a/b"},
			{`A`, "A"},
			{`é`, "é"},
			// Only four digits are consumed, whatever follows.
			{`ὤ0`, "ὤ0"[:0] + "ὤ" + "0"},
			{`trailing\`, `trailing\`},
		}
		for _, tc := range cases {
			if got := string(unescapeJSONString([]byte(tc.in))); got != tc.want {
				t.Errorf("unescapeJSONString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("squoted", func(t *testing.T) {
		cases := []struct{ in, want string }{
			{`b\n\'a`, `b\n'a`},
			{"\\\naaa", "aaa"},
			{"\\\r\naaa", "aaa"},
			{`x\`, `x\`},
		}
		for _, tc := range cases {
			if got := string(unescapeSquotedString([]byte(tc.in))); got != tc.want {
				t.Errorf("unescapeSquotedString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})
}

func TestParserCharTable(t *testing.T) {
	// Spot-checks of the classes the lexers actually branch on.
	if !testChar('\t', chWhitespace) || !testChar(' ', chWhitespace) {
		t.Errorf("WHITESPACE must contain tab and space")
	}
	if testChar('\n', chWhitespace) {
		t.Errorf("WHITESPACE must not contain newline")
	}
	for _, c := range []byte{0x00, '\n', '\r', '#', ',', ';', ']', '}'} {
		if !testChar(c, chValueEnd) {
			t.Errorf("VALUE_END should contain %q", c)
		}
	}
	for _, c := range []byte{'{', '['} {
		if testChar(c, chValueEnd) {
			t.Errorf("VALUE_END should not contain %q", c)
		}
	}
	if testChar('.', chKeyStart) || !testChar('.', chKey) {
		t.Errorf("'.' is a key character but not a key start")
	}
	if !testChar(0x80, chKeyStart) || !testChar(0xff, chKeyStart) {
		t.Errorf("high bytes should be key starts")
	}
	for _, c := range []byte{'"', '/', '\\', 'b', 'f', 'n', 'r', 't', 'u'} {
		if !testChar(c, chEscape) {
			t.Errorf("ESCAPE should contain %q", c)
		}
	}
	if testChar('B', chEscape) {
		t.Errorf("ESCAPE is lowercase only")
	}
}

func TestParserAPI(t *testing.T) {
	t.Run("default priority", func(t *testing.T) {
		var nilParser *Parser
		if nilParser.DefaultPriority() != -1 {
			t.Errorf("a nil parser reports -1")
		}
		p := NewParser(0)
		p.SetDefaultPriority(7)
		if p.DefaultPriority() != 7 {
			t.Errorf("got %d", p.DefaultPriority())
		}
	})

	t.Run("chunk cursor", func(t *testing.T) {
		p := NewParser(0)
		p.AddString("a = 1\n")
		// The whole chunk is consumed, so there is nothing left to peek at.
		if p.ChunkPeek() != 0 {
			t.Errorf("peek should be 0 at the end of the chunk")
		}
		if p.LineNum() < 1 {
			t.Errorf("line number should be at least 1")
		}
	})

	t.Run("error state is sticky", func(t *testing.T) {
		p := NewParser(0)
		p.AddString("= bad\n")
		if p.GetError() == nil {
			t.Fatalf("expected an error")
		}
		if p.GetObject() != nil {
			t.Errorf("GetObject must return nil in the error state")
		}
		if p.AddString("a = 1\n") {
			t.Errorf("a parser in the error state rejects further chunks")
		}
		p.ClearError()
		if p.GetError() != nil {
			t.Errorf("ClearError should discard the message")
		}
	})

	t.Run("add fd", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "x.conf")
		os.WriteFile(path, []byte("fd = 1\n"), 0o644)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		p := NewParser(0)
		if !p.AddFd(f.Fd()) {
			t.Fatalf("AddFd failed: %v", p.GetError())
		}
		if got, _ := EmitTextString(p.GetObject(), EmitConfig); got != "fd = 1;\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		p := NewParser(0)
		if p.AddFile(filepath.Join(t.TempDir(), "absent.conf")) {
			t.Errorf("AddFile should fail")
		}
		if err := p.GetError(); err == nil ||
			!strings.HasPrefix(err.Error(), "cannot open file ") ||
			!strings.HasSuffix(err.Error(), ": No such file or directory") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("insert chunk", func(t *testing.T) {
		p := NewParser(0)
		p.RegisterMacro("gen", func(data []byte, args *Object) bool {
			return p.InsertChunk([]byte("generated = " + string(data) + ";\n"))
		})
		if !p.AddString("a = 1;\n.gen 42\nb = 2;\n") {
			t.Fatalf("parse failed: %v", p.GetError())
		}
		got, _ := EmitTextString(p.GetObject(), EmitConfig)
		want := "a = 1;\ngenerated = 42;\nb = 2;\n"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("multiple chunks", func(t *testing.T) {
		p := NewParser(0)
		p.AddString("a = 1;\n")
		p.AddString("b = 2;\n")
		got, _ := EmitTextString(p.GetObject(), EmitConfig)
		if got != "a = 1;\nb = 2;\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("empty chunk still yields a document", func(t *testing.T) {
		p := NewParser(0)
		if !p.AddString("") {
			t.Fatalf("failed: %v", p.GetError())
		}
		obj := p.GetObject()
		if obj == nil || obj.ObjType != TypeObject {
			t.Errorf("want an empty object, got %v", obj)
		}
	})
}

func TestParserZeroCopy(t *testing.T) {
	// ZEROCOPY is an allocation strategy, not a semantic change: the values
	// must be identical either way.
	src := "a = plain\nb = \"quoted\"\nc = 1\n"
	with, err1 := parseConfig(t, src, ParserZeroCopy, DuplicateAppend)
	without, err2 := parseConfig(t, src, 0, DuplicateAppend)
	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v %v", err1, err2)
	}
	if with != without {
		t.Errorf("ZEROCOPY changed the result:\n%s\nvs\n%s", with, without)
	}
}

// TestParserArrayLeadingComment pins the one deliberate divergence from
// libucl, documented in docs/findings.md item 3.
//
// The C skips whitespace and then comments exactly once when it starts a
// value, so a comment on the first line inside an array leaves the cursor on
// the next line's indentation and the element is lexed as a string -- or, for
// a block comment, fails outright. The wantC column records what libucl 0.9.4
// (adef4bb) actually produces; wantGo is what this port produces instead.
//
// Everything the C gets right is here too, so a future change that "fixes"
// more than intended shows up as a diff.
func TestParserArrayLeadingComment(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		wantC  string // libucl 0.9.4, captured from the C library
		wantGo string // this port; "" means "same as wantC"
	}{
		{
			name:   "indented int after line comment",
			src:    "arr [\n  # c\n  1,\n  2\n]\n",
			wantC:  "arr [\n    \"  1\",\n    2,\n]\n",
			wantGo: "arr [\n    1,\n    2,\n]\n",
		},
		{
			name:   "indented object after line comment",
			src:    "arr [\n  # c\n  {a=1},\n  2\n]\n",
			wantC:  "arr [\n    \"  {a=1}\",\n    2,\n]\n",
			wantGo: "arr [\n    {\n        a = 1;\n    }\n    2,\n]\n",
		},
		{
			name: "indented int after block comment",
			src:  "arr [\n  /* c */\n  1,\n  2\n]\n",
			// The C fails here: "string value must not be empty".
			wantC:  "",
			wantGo: "arr [\n    1,\n    2,\n]\n",
		},
		{
			name:  "content at column zero is unaffected",
			src:   "arr [\n# c\n1,\n2\n]\n",
			wantC: "arr [\n    1,\n    2,\n]\n",
		},
		{
			name:  "comment after the first element is unaffected",
			src:   "arr [\n  1,\n  # c\n  2\n]\n",
			wantC: "arr [\n    1,\n    2,\n]\n",
		},
		{
			name:  "single line array is unaffected",
			src:   "arr [ 1, 2 ]\n",
			wantC: "arr [\n    1,\n    2,\n]\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.wantGo
			if want == "" {
				want = tc.wantC
			}
			got, err := parseConfig(t, tc.src, 0, DuplicateAppend)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
			if tc.wantGo != "" && tc.wantGo == tc.wantC {
				t.Errorf("wantGo should be left empty when it equals wantC")
			}
		})
	}

	t.Run("also fixed with save-comments", func(t *testing.T) {
		// This is the shape that makes basic/9.in's save-comments round trip
		// fail in the C: emitted comments land inside an implicit array.
		got, err := parseConfig(t, "arr [\n  # c\n  1,\n  2\n]\n",
			ParserSaveComments, DuplicateAppend)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "arr [\n    1,\n    2,\n]\n" {
			t.Errorf("got:\n%s", got)
		}
	})
}
