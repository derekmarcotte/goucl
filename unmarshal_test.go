package goucl

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// --- scalars ---------------------------------------------------------------

func TestUnmarshalScalars(t *testing.T) {
	var got struct {
		S   string
		I   int
		I8  int8
		U   uint
		F32 float32
		F64 float64
		B   bool
		D   time.Duration
	}
	src := `
		s = "text";
		i = 42;
		i8 = -5;
		u = 7;
		f32 = 1.5;
		f64 = 2.5;
		b = true;
		d = 10s;
	`
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.S != "text" {
		t.Errorf("S = %q, want text", got.S)
	}
	if got.I != 42 {
		t.Errorf("I = %d, want 42", got.I)
	}
	if got.I8 != -5 {
		t.Errorf("I8 = %d, want -5", got.I8)
	}
	if got.U != 7 {
		t.Errorf("U = %d, want 7", got.U)
	}
	if got.F32 != 1.5 {
		t.Errorf("F32 = %v, want 1.5", got.F32)
	}
	if got.F64 != 2.5 {
		t.Errorf("F64 = %v, want 2.5", got.F64)
	}
	if !got.B {
		t.Errorf("B = false, want true")
	}
	if got.D != 10*time.Second {
		t.Errorf("D = %v, want 10s", got.D)
	}
}

// A negative int cannot decode into an unsigned field.
func TestUnmarshalNegativeIntoUnsignedErrors(t *testing.T) {
	var got struct{ U uint }
	err := Unmarshal([]byte(`u = -1;`), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
}

// --- nested structs, maps, slices, arrays, pointers, any -------------------

func TestUnmarshalNestedStruct(t *testing.T) {
	type Server struct {
		Host string
		Port int
	}
	var got struct {
		Server Server
	}
	src := `server { host = "example.org"; port = 8080; }`
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Server.Host != "example.org" || got.Server.Port != 8080 {
		t.Errorf("Server = %+v", got.Server)
	}
}

func TestUnmarshalMap(t *testing.T) {
	var got map[string]int
	src := `a = 1; b = 2; c = 3;`
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]int{"a": 1, "b": 2, "c": 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestUnmarshalSliceFromArray(t *testing.T) {
	var got struct{ Ports []int }
	if err := Unmarshal([]byte(`ports = [80, 443, 8080];`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !slices.Equal(got.Ports, []int{80, 443, 8080}) {
		t.Errorf("Ports = %v", got.Ports)
	}
}

func TestUnmarshalArrayFromArray(t *testing.T) {
	var got struct{ Ports [2]int }
	if err := Unmarshal([]byte(`ports = [80, 443, 8080];`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// A fixed-size array takes only as many elements as it has room for,
	// matching encoding/json.
	if got.Ports != [2]int{80, 443} {
		t.Errorf("Ports = %v, want [80 443]", got.Ports)
	}
}

func TestUnmarshalPointer(t *testing.T) {
	var got struct{ Host *string }
	if err := Unmarshal([]byte(`host = "example.org";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host == nil || *got.Host != "example.org" {
		t.Errorf("Host = %v", got.Host)
	}
}

func TestUnmarshalAny(t *testing.T) {
	var got any
	src := `
		s = "text";
		i = 42;
		f = 1.5;
		b = true;
		n = null;
		arr = [1, "two", 3.0];
		obj { k = "v"; }
	`
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map[string]any", got)
	}
	if m["s"] != "text" {
		t.Errorf("s = %v", m["s"])
	}
	if m["i"] != int64(42) {
		t.Errorf("i = %v (%T), want int64(42)", m["i"], m["i"])
	}
	if m["f"] != 1.5 {
		t.Errorf("f = %v", m["f"])
	}
	if m["b"] != true {
		t.Errorf("b = %v", m["b"])
	}
	if m["n"] != nil {
		t.Errorf("n = %v, want nil", m["n"])
	}
	arr, ok := m["arr"].([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("arr = %v", m["arr"])
	}
	if arr[0] != int64(1) || arr[1] != "two" || arr[2] != 3.0 {
		t.Errorf("arr = %v", arr)
	}
	obj, ok := m["obj"].(map[string]any)
	if !ok || obj["k"] != "v" {
		t.Errorf("obj = %v", m["obj"])
	}
}

// Decoding a time into any produces a time.Duration, matching Node.Duration.
func TestUnmarshalAnyDuration(t *testing.T) {
	var got any
	if err := Unmarshal([]byte(`d = 10s;`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	m := got.(map[string]any)
	d, ok := m["d"].(time.Duration)
	if !ok || d != 10*time.Second {
		t.Errorf("d = %v (%T), want 10s", m["d"], m["d"])
	}
}

// --- struct tags -------------------------------------------------------

func TestUnmarshalUCLTag(t *testing.T) {
	var got struct {
		Host string `ucl:"hostname"`
	}
	if err := Unmarshal([]byte(`hostname = "example.org";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "example.org" {
		t.Errorf("Host = %q", got.Host)
	}
}

// When a field has no ucl tag, the json tag is used instead.
func TestUnmarshalJSONTagFallback(t *testing.T) {
	var got struct {
		Host string `json:"hostname"`
	}
	if err := Unmarshal([]byte(`hostname = "example.org";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "example.org" {
		t.Errorf("Host = %q", got.Host)
	}
}

// A ucl tag wins over a json tag when both are present.
func TestUnmarshalUCLTagWinsOverJSON(t *testing.T) {
	var got struct {
		Host string `ucl:"hostname" json:"other"`
	}
	if err := Unmarshal([]byte(`hostname = "example.org"; other = "wrong";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "example.org" {
		t.Errorf("Host = %q, want example.org", got.Host)
	}
}

// ",omitempty" and similar suffixes only affect encoding, so they are
// ignored on the way in.
func TestUnmarshalOmitemptyIgnored(t *testing.T) {
	var got struct {
		Host string `ucl:"hostname,omitempty"`
	}
	if err := Unmarshal([]byte(`hostname = "example.org";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "example.org" {
		t.Errorf("Host = %q", got.Host)
	}
}

// A "-" tag skips the field entirely: it is left zero and does not count as
// undecoded, since the caller said explicitly it is not wanted.
func TestUnmarshalDashSkipsField(t *testing.T) {
	var got struct {
		Host string `ucl:"-"`
	}
	undecoded, err := decodeInto(mustParse(t, `host = "example.org";`), &got, false)
	if err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if got.Host != "" {
		t.Errorf("Host = %q, want zero value", got.Host)
	}
	if !slices.Contains(undecoded, "host") {
		t.Errorf("undecoded = %v, want it to include host", undecoded)
	}
}

// Exact key matches win over a case-insensitive one.
func TestUnmarshalExactMatchWinsOverCaseInsensitive(t *testing.T) {
	var got struct {
		Host string `ucl:"Host"`
	}
	if err := Unmarshal([]byte(`Host = "exact"; host = "loose";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "exact" {
		t.Errorf("Host = %q, want exact (the exact-case match)", got.Host)
	}
}

// Absent an exact match, a case-insensitive one is used.
func TestUnmarshalCaseInsensitiveMatch(t *testing.T) {
	var got struct {
		HostName string
	}
	if err := Unmarshal([]byte(`hostname = "example.org";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.HostName != "example.org" {
		t.Errorf("HostName = %q", got.HostName)
	}
}

// Unexported fields are skipped, never panicked on.
func TestUnmarshalUnexportedFieldSkipped(t *testing.T) {
	var got struct {
		Host   string
		hidden string
	}
	if err := Unmarshal([]byte(`host = "example.org"; hidden = "nope";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Host != "example.org" {
		t.Errorf("Host = %q", got.Host)
	}
	if got.hidden != "" {
		t.Errorf("hidden = %q, want zero value", got.hidden)
	}
}

// --- TextUnmarshaler ---------------------------------------------------

type upperString string

func (u *upperString) UnmarshalText(text []byte) error {
	s := string(text)
	for i := range s {
		if s[i] >= 'a' && s[i] <= 'z' {
			s = s[:i] + string(s[i]-32) + s[i+1:]
		}
	}
	*u = upperString(s)
	return nil
}

func TestUnmarshalTextUnmarshaler(t *testing.T) {
	var got struct{ Name upperString }
	if err := Unmarshal([]byte(`name = "example";`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Name != "EXAMPLE" {
		t.Errorf("Name = %q, want EXAMPLE", got.Name)
	}
}

type failingUnmarshaler struct{}

func (*failingUnmarshaler) UnmarshalText([]byte) error {
	return errors.New("boom")
}

func TestUnmarshalTextUnmarshalerErrorWraps(t *testing.T) {
	var got struct{ V failingUnmarshaler }
	err := Unmarshal([]byte(`v = "x";`), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
	if de.Err == nil || de.Err.Error() != "boom" {
		t.Errorf("DecodeError.Err = %v, want the wrapped \"boom\"", de.Err)
	}
}

// --- repeated keys -------------------------------------------------------

// A repeated key decoding into a slice collects every value, via
// Node.Values -- not the first, and not by expanding a container.
func TestUnmarshalRepeatedKeyIntoSlice(t *testing.T) {
	var got struct{ Listen []string }
	src := "listen = \"10.0.0.1:80\";\nlisten = \"10.0.0.2:80\";\n"
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !slices.Equal(got.Listen, []string{"10.0.0.1:80", "10.0.0.2:80"}) {
		t.Errorf("Listen = %v", got.Listen)
	}
}

// A repeated key decoding into a scalar field is an error: taking the first
// value would be indistinguishable from a correct single-valued read.
func TestUnmarshalRepeatedKeyIntoScalarErrors(t *testing.T) {
	var got struct{ Listen string }
	src := "listen = \"10.0.0.1:80\";\nlisten = \"10.0.0.2:80\";\n"
	err := Unmarshal([]byte(src), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
}

// The case node.go's "Multi-valued keys" comment calls out by name: a
// repeated key whose values are themselves arrays. Collapsing Values and
// Elements would make the second array unreachable; decodeSequence must not
// make that mistake either.
func TestUnmarshalRepeatedKeyOfArrays(t *testing.T) {
	var got struct{ X [][]int }
	if err := Unmarshal([]byte(`x = [1]; x = [2];`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := [][]int{{1}, {2}}
	if len(got.X) != 2 || !slices.Equal(got.X[0], want[0]) || !slices.Equal(got.X[1], want[1]) {
		t.Errorf("X = %v, want %v", got.X, want)
	}
}

// A single key whose value is an array decodes via Elements, not Values: it
// is one value, not several.
func TestUnmarshalSingleArrayValueIntoSlice(t *testing.T) {
	var got struct{ Listen []string }
	if err := Unmarshal([]byte(`listen = ["10.0.0.1:80", "10.0.0.2:80"];`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !slices.Equal(got.Listen, []string{"10.0.0.1:80", "10.0.0.2:80"}) {
		t.Errorf("Listen = %v", got.Listen)
	}
}

// Repeated objects (the nginx pattern) decode into a slice of structs.
func TestUnmarshalRepeatedKeyOfObjectsIntoSlice(t *testing.T) {
	type Server struct{ Port int }
	var got struct{ Server []Server }
	src := "server { port = 80; }\nserver { port = 443; }\n"
	if err := Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Server) != 2 || got.Server[0].Port != 80 || got.Server[1].Port != 443 {
		t.Errorf("Server = %+v", got.Server)
	}
}

// --- unknown keys ----------------------------------------------------------

func TestUnmarshalUnknownKeyNonStrict(t *testing.T) {
	var got struct{ Host string }
	n := mustParse(t, `host = "example.org"; extra = 1;`)
	undecoded, err := decodeInto(n, &got, false)
	if err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if !slices.Equal(undecoded, []string{"extra"}) {
		t.Errorf("undecoded = %v, want [extra]", undecoded)
	}
}

func TestUnmarshalUnknownKeyStrict(t *testing.T) {
	var got struct{ Host string }
	n := mustParse(t, `host = "example.org"; extra = 1;`)
	_, err := decodeInto(n, &got, true)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
	if de.Path != "extra" {
		t.Errorf("Path = %q, want extra", de.Path)
	}
}

// Undecoded paths use the same dotted spelling Node.Get accepts, so a caller
// can feed one straight back to Get.
func TestUnmarshalUnknownKeyPathIsDotted(t *testing.T) {
	type Server struct{ Host string }
	var got struct{ Servers []Server }
	n := mustParse(t, `servers = [ { host = "a"; port = 1; } ];`)
	undecoded, err := decodeInto(n, &got, false)
	if err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if !slices.Equal(undecoded, []string{"servers.0.port"}) {
		t.Errorf("undecoded = %v, want [servers.0.port]", undecoded)
	}
	// And it really does feed back into Get.
	if v, ok := n.Get(undecoded[0]); !ok || v.Kind() != KindInt {
		t.Errorf("Get(%q) = %v, %v", undecoded[0], v, ok)
	}
}

// A field skipped with "-" is reported undecoded: the tag says the field
// itself is not wanted, not that the key is expected to be absent.
func TestUnmarshalUnknownKeyNested(t *testing.T) {
	type Server struct{ Host string }
	var got struct{ Server Server }
	n := mustParse(t, `server { host = "a"; nope = 1; }`)
	undecoded, err := decodeInto(n, &got, false)
	if err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if !slices.Equal(undecoded, []string{"server.nope"}) {
		t.Errorf("undecoded = %v, want [server.nope]", undecoded)
	}
}

// --- type mismatches -------------------------------------------------------

func TestUnmarshalTypeMismatchIsDecodeError(t *testing.T) {
	var got struct{ Port int }
	err := Unmarshal([]byte(`port = "not a number";`), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
}

// A non-object value cannot decode into a struct.
func TestUnmarshalNonObjectIntoStructIsDecodeError(t *testing.T) {
	var got struct{ Host string }
	err := Unmarshal([]byte(`[1, 2, 3]`), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecodeError", err)
	}
}

// Decoding does not panic on a hostile target: a non-pointer, or a nil
// pointer.
func TestUnmarshalHostileTargetsDoNotPanic(t *testing.T) {
	n := mustParse(t, `a = 1;`)

	var notAPointer struct{ A int }
	if err := n.Decode(notAPointer); err == nil {
		t.Error("Decode(non-pointer) should error")
	}

	var nilPointer *struct{ A int }
	if err := n.Decode(nilPointer); err == nil {
		t.Error("Decode(nil pointer) should error")
	}
}

// --- Unmarshal syntax failure ----------------------------------------------

func TestUnmarshalSyntaxErrorIsSyntaxError(t *testing.T) {
	var got any
	err := Unmarshal([]byte(`a = ;`), &got)
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SyntaxError", err)
	}
}
