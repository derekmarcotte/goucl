package goucl

import (
	"slices"
	"strings"
	"testing"
)

// Comments are the main reason to choose UCL over JSON, so the shape the
// engine actually gives us is pinned here rather than described. Every
// expectation below was taken from the engine's behaviour, which is the C's.

func TestCommentBefore(t *testing.T) {
	doc := mustParse(t, "# leading\nkey = 1;\n")
	v, ok := doc.Get("key")
	if !ok {
		t.Fatal("key missing")
	}
	if got := v.Before(); !slices.Equal(got, []string{"# leading"}) {
		t.Errorf("Before = %q, want [# leading]", got)
	}
	if got := v.After(); got != nil {
		t.Errorf("After = %q, want nil", got)
	}
}

func TestCommentBlockAccumulates(t *testing.T) {
	doc := mustParse(t, "# one\n# two\n# three\nkey = 1;\n")
	v, _ := doc.Get("key")
	want := []string{"# one", "# two", "# three"}
	if got := v.Before(); !slices.Equal(got, want) {
		t.Errorf("Before = %q, want %q", got, want)
	}
}

// A comment with nothing after it attaches to the preceding node instead, and
// is the only way an After comment arises.
func TestCommentAfterAtEOF(t *testing.T) {
	doc := mustParse(t, "key = 1;\n# trailing\n")
	v, _ := doc.Get("key")
	if got := v.After(); !slices.Equal(got, []string{"# trailing"}) {
		t.Errorf("After = %q, want [# trailing]", got)
	}
	if got := v.Before(); got != nil {
		t.Errorf("Before = %q, want nil", got)
	}
}

// UCL has no line comments. A comment trailing a value on the same line binds
// to the NEXT node, not the one it sits beside. This is surprising, it is the
// C's behaviour, and it is reproduced rather than corrected -- so it is
// asserted here to stop anyone "fixing" it by accident.
func TestTrailingCommentBindsToNextNode(t *testing.T) {
	doc := mustParse(t, "a = 1; # about a?\nb = 2;\n")

	av, _ := doc.Get("a")
	if got := av.Before(); got != nil {
		t.Errorf("a.Before = %q, want nil", got)
	}
	if got := av.After(); got != nil {
		t.Errorf("a.After = %q, want nil", got)
	}

	bv, _ := doc.Get("b")
	if got := bv.Before(); !slices.Equal(got, []string{"# about a?"}) {
		t.Errorf("b.Before = %q, want the comment written beside a", got)
	}
}

func TestCommentsSurviveConfigRoundTrip(t *testing.T) {
	src := "# a leading comment\nkey = 1;\n# another\nother = 2;\n"
	doc := mustParse(t, src)

	out, err := doc.Bytes(FormatConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# a leading comment", "# another"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("emitted config lost %q:\n%s", want, out)
		}
	}

	// And the comments are still reachable after re-parsing our own output.
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	v, _ := again.Get("key")
	if got := v.Before(); !slices.Equal(got, []string{"# a leading comment"}) {
		t.Errorf("after round trip Before = %q", got)
	}
}

// Only the config format emits comments, matching the C.
func TestCommentsOnlyInConfigFormat(t *testing.T) {
	doc := mustParse(t, "# secret\nkey = 1;\n")
	for _, f := range []Format{FormatJSON, FormatJSONCompact, FormatYAML} {
		out, err := doc.Bytes(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "# secret") {
			t.Errorf("%v emitted a comment:\n%s", f, out)
		}
	}
}
