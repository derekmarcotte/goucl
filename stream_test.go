package goucl

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// errReader returns err from every Read.
type errReader struct{ err error }

func (r errReader) Read(p []byte) (int, error) { return 0, r.err }

func TestDecoderSurfacesReadError(t *testing.T) {
	wantErr := errors.New("disk on fire")
	d := NewDecoder(errReader{wantErr})

	var v map[string]any
	err := d.Decode(&v)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Decode error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestDecoderSecondCallReturnsEOF(t *testing.T) {
	d := NewDecoder(strings.NewReader(`a = 1;`))

	var v map[string]any
	if err := d.Decode(&v); err != nil {
		t.Fatalf("first Decode: %v", err)
	}
	if err := d.Decode(&v); err != io.EOF {
		t.Errorf("second Decode = %v, want io.EOF", err)
	}
}

func TestDecoderUndecoded(t *testing.T) {
	d := NewDecoder(strings.NewReader(`known = 1; extra = 2;`))

	var got struct {
		Known int
	}
	if err := d.Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []string{"extra"}
	if len(d.Undecoded()) != 1 || d.Undecoded()[0] != want[0] {
		t.Errorf("Undecoded = %v, want %v", d.Undecoded(), want)
	}
}

func TestDecoderOptions(t *testing.T) {
	d := NewDecoder(strings.NewReader(`host = "${name}";`), WithVariable("name", "example.com"))

	var got struct {
		Host string
	}
	if err := d.Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Host != "example.com" {
		t.Errorf("Host = %q, want %q", got.Host, "example.com")
	}
}

func TestDecoderDisallowUnknownFields(t *testing.T) {
	d := NewDecoder(strings.NewReader(`known = 1; extra = 2;`), DisallowUnknownFields())

	var got struct {
		Known int
	}
	if err := d.Decode(&got); err == nil {
		t.Fatal("expected an error for the unknown field")
	}
}

func TestEncoderFormats(t *testing.T) {
	v := struct {
		Key string
	}{Key: "value"}

	for _, tc := range []struct {
		opts []EncodeOption
		name string
		want string // a substring the output must contain
	}{
		{nil, "default", "Key = "},
		{[]EncodeOption{WithFormat(FormatConfig)}, "config", "Key = "},
		{[]EncodeOption{WithFormat(FormatJSON)}, "json", "\"Key\""},
		{[]EncodeOption{WithFormat(FormatJSONCompact)}, "json-compact", "\"Key\""},
		{[]EncodeOption{WithFormat(FormatYAML)}, "yaml", "Key"},
	} {
		var buf bytes.Buffer
		e := NewEncoder(&buf, tc.opts...)
		if err := e.Encode(v); err != nil {
			t.Fatalf("%s: Encode: %v", tc.name, err)
		}
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("%s: output = %q, want it to contain %q", tc.name, buf.String(), tc.want)
		}
	}
}

func TestEncoderDefaultsToFormatConfig(t *testing.T) {
	var withOption, withoutOption bytes.Buffer

	v := struct{ A int }{A: 1}

	if err := NewEncoder(&withOption, WithFormat(FormatConfig)).Encode(v); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := NewEncoder(&withoutOption).Encode(v); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if withOption.String() != withoutOption.String() {
		t.Errorf("default output = %q, want it to match explicit WithFormat(FormatConfig) = %q",
			withoutOption.String(), withOption.String())
	}
}

func TestRoundTripEncoderDecoder(t *testing.T) {
	type doc struct {
		Name  string
		Count int
		Tags  []string
	}
	want := doc{Name: "svc", Count: 3, Tags: []string{"a", "b"}}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(want); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var got doc
	if err := NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
