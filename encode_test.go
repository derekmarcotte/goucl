package goucl

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormats(t *testing.T) {
	doc := mustParse(t, `key = "value"; n = 1;`)

	for _, tc := range []struct {
		f     Format
		name  string
		check func(string) bool
		want  string
	}{
		{FormatJSON, "json", func(s string) bool {
			return strings.Contains(s, `"key"`) && strings.Contains(s, "\n")
		}, "indented JSON"},
		{FormatJSONCompact, "json-compact", func(s string) bool {
			return strings.HasPrefix(s, "{") && !strings.Contains(s, "\n")
		}, "single-line JSON"},
		{FormatConfig, "config", func(s string) bool {
			return strings.Contains(s, "key = ")
		}, "config syntax"},
		{FormatYAML, "yaml", func(s string) bool {
			return strings.Contains(s, "key")
		}, "YAML"},
	} {
		out, err := doc.Bytes(tc.f)
		if err != nil {
			t.Fatalf("%v: %v", tc.f, err)
		}
		if tc.f.String() != tc.name {
			t.Errorf("Format.String = %q, want %q", tc.f.String(), tc.name)
		}
		if !tc.check(string(out)) {
			t.Errorf("%v did not look like %s:\n%s", tc.f, tc.want, out)
		}
	}
}

func TestEncodeWritesToWriter(t *testing.T) {
	doc := mustParse(t, `key = 1;`)
	var buf bytes.Buffer
	if err := doc.Encode(&buf, FormatJSONCompact); err != nil {
		t.Fatal(err)
	}
	direct, err := doc.Bytes(FormatJSONCompact)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), direct) {
		t.Errorf("Encode and Bytes disagree:\n%q\n%q", buf.Bytes(), direct)
	}
}

func TestUnknownFormat(t *testing.T) {
	doc := mustParse(t, `key = 1;`)
	if _, err := doc.Bytes(Format(99)); err == nil {
		t.Error("expected an error for an unknown format")
	}
	if got := Format(99).String(); got != "unknown" {
		t.Errorf("String = %q, want unknown", got)
	}
}

// A subtree encodes on its own, not just the document root.
func TestEncodeSubtree(t *testing.T) {
	doc := mustParse(t, `outer { inner = "x"; }`)
	sub, ok := doc.Get("outer")
	if !ok {
		t.Fatal("outer missing")
	}
	out, err := sub.Bytes(FormatJSONCompact)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"inner":"x"}` {
		t.Errorf("subtree = %s", got)
	}
}
