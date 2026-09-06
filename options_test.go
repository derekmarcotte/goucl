package goucl

import (
	"errors"
	"strings"
	"testing"
)

func TestWithVariable(t *testing.T) {
	n, err := ParseString(`host = "${name}";`, WithVariable("name", "example.com"))
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	got, ok := n.String("host")
	if !ok || got != "example.com" {
		t.Errorf("host = %q, %v, want %q, true", got, ok, "example.com")
	}
}

func TestWithMacroSucceeds(t *testing.T) {
	var gotData string
	var gotArgs *Node

	macro := WithMacro("greet", func(data []byte, args *Node) error {
		gotData = string(data)
		gotArgs = args
		return nil
	})

	n, err := ParseString(".greet(who=world) hello\na = 1;\n", macro)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	if gotData != "hello" {
		t.Errorf("macro data = %q, want %q", gotData, "hello")
	}
	if gotArgs == nil {
		t.Fatal("macro args = nil, want the (who=world) arguments")
	}
	if who, ok := gotArgs.String("who"); !ok || who != "world" {
		t.Errorf("args.String(\"who\") = %q, %v, want %q, true", who, ok, "world")
	}
	if v, ok := n.Int("a"); !ok || v != 1 {
		t.Errorf("a = %v, %v, want 1, true -- parsing should continue past the macro", v, ok)
	}
}

func TestWithMacroNoArgs(t *testing.T) {
	var gotArgs *Node
	sawArgs := false
	macro := WithMacro("noop", func(data []byte, args *Node) error {
		gotArgs = args
		sawArgs = true
		return nil
	})
	if _, err := ParseString(".noop x\n", macro); err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	if !sawArgs {
		t.Fatal("macro was never called")
	}
	if gotArgs != nil {
		t.Errorf("args = %v, want nil for a macro invoked without (...)", gotArgs)
	}
	// A nil *Node behaves like any other invalid node rather than panicking.
	if _, ok := gotArgs.String("anything"); ok {
		t.Error("String on a nil Node reported ok, want false")
	}
}

func TestWithMacroFails(t *testing.T) {
	failure := errors.New("boom")
	macro := WithMacro("fails", func(data []byte, args *Node) error {
		return failure
	})

	_, err := ParseString(".fails x\na = 1;\n", macro)
	if err == nil {
		t.Fatal("expected the parse to fail")
	}

	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T: %v", err, err)
	}
	if se.Code != ErrMacro {
		t.Errorf("Code = %v, want ErrMacro", se.Code)
	}
	// The message should name the macro and carry the original failure text,
	// even though *SyntaxError has no Unwrap to reach failure through
	// errors.Is.
	if msg := se.Error(); !strings.Contains(msg, `macro "fails"`) || !strings.Contains(msg, "boom") {
		t.Errorf("Error() = %q, want it to mention the macro name and %q", msg, "boom")
	}
}

func TestDisallowUnknownFields(t *testing.T) {
	src := `known = 1; extra = 2;`
	var got struct {
		Known int
	}

	if err := Unmarshal([]byte(src), &got, DisallowUnknownFields()); err == nil {
		t.Fatal("expected an error for the unknown field")
	}

	got = struct{ Known int }{}
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal without DisallowUnknownFields: %v", err)
	}
	if got.Known != 1 {
		t.Errorf("Known = %d, want 1", got.Known)
	}
}

func TestUndecodedPaths(t *testing.T) {
	src := `
		known = 1;
		extra = 2;
		nested {
			also_known = 3;
			also_extra = 4;
		}
	`
	var got struct {
		Known  int
		Nested struct {
			AlsoKnown int `ucl:"also_known"`
		}
	}

	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	undecoded, err := decodeInto(n, &got, false)
	if err != nil {
		t.Fatalf("decodeInto: %v", err)
	}

	want := []string{"extra", "nested.also_extra"}
	if len(undecoded) != len(want) {
		t.Fatalf("Undecoded = %v, want %v", undecoded, want)
	}
	for i := range want {
		if undecoded[i] != want[i] {
			t.Errorf("Undecoded[%d] = %q, want %q", i, undecoded[i], want[i])
		}
	}
}
