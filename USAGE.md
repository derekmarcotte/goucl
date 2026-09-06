# Using goucl

How to read a UCL document from Go. `README.md` says what the project is,
`DESIGN.md` says why the API has the shape it has, and this file says how to
use it.

## Which package to import

| You are | Import |
|---|---|
| writing new code | `github.com/derekmarcotte/goucl` |
| replacing `github.com/mitchellh/go-libucl` | `github.com/derekmarcotte/goucl/compat/libucl` |

The two are independent and can be used in the same program. The root package
is comma-ok accessors, range-over-func iteration and structured errors; it
makes no attempt to match the cgo binding. `compat/libucl` reproduces that
binding exactly, down to the inert `Close()` calls, so an import-path swap is
the whole migration. See [Migrating from the cgo binding](#migrating-from-the-cgo-binding).

`internal/core` is the engine both are built on. It mirrors the C API
one-to-one and is not importable from outside the module. It is mentioned here
only because error messages and emitted bytes come from it, which is why they
are byte-identical to the C's.

## Quick start

```go
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/derekmarcotte/goucl"
)

func main() {
	doc, err := goucl.ParseFile("app.conf")
	if err != nil {
		var se *goucl.SyntaxError
		if errors.As(err, &se) {
			log.Fatalf("%s\n%s", se, se.Excerpt())
		}
		log.Fatal(err)
	}

	host, _ := doc.String("server.host")
	port, _ := doc.Int("server.port")
	fmt.Printf("%s:%d\n", host, port)
}
```

Three entry points, all returning the document's root `*Node`:

```go
func Parse(data []byte, opts ...Option) (*Node, error)
func ParseString(s string, opts ...Option) (*Node, error)
func ParseFile(name string, opts ...Option) (*Node, error)
```

`opts` registers variables and macros before the document is parsed; see
[Options](#options) below. All three are source-compatible with a call
written before options existed — `Parse(data)` still compiles.

`ParseFile` is not `Parse(os.ReadFile(...))`. The engine opens the file itself,
because opening it is what sets the document's `$FILENAME` and `$CURDIR`
variables and the base path an `.include` is reported against. Reading the
bytes separately would quietly change what the document means.

An empty input is an empty object, not an error and not a nil tree.

### Includes resolve against the process working directory

A relative `.include` is resolved against the working directory of the process,
not against the directory of the file doing the including. That is what the C
does, it is behaviour a document can observe, and it is reproduced deliberately
rather than corrected: a document that works under libucl works identically
here. `docs/findings.md` item 1 has the reasoning, and `DESIGN.md` §6.2 records
the decision, including the earlier proposal to re-root includes and why it was
rejected.

If your program's working directory is not where your config's includes live,
`chdir` before parsing or write absolute paths in the document.

## Reading values

Every typed accessor takes a path and returns comma-ok:

```go
func (n *Node) String(path string) (string, bool)
func (n *Node) Int(path string) (int64, bool)
func (n *Node) Float(path string) (float64, bool)
func (n *Node) Bool(path string) (bool, bool)
func (n *Node) Duration(path string) (time.Duration, bool)
```

An empty path reads the node itself, so there is one spelling rather than two:
`doc.Int("db.port")` and `port.Int("")` are the same call.

### Why comma-ok

A missing key, a key of the wrong kind, and a real zero are three different
answers, and configuration produces all three constantly:

```go
v, ok := doc.Int("retries")   // 0, true   -- the document said 0
v, ok := doc.Int("absent")    // 0, false  -- no such key
v, ok := doc.Int("hostname")  // 0, false  -- present, but a string
```

A bare-value accessor collapses the last two into the first, which is how a
config bug becomes a silent default. Discarding `ok` with `_` is fine when a
zero really is the default you want; the point is that the API makes you say so.

### What converts to what

The accessors do not coerce across families. A present key of the wrong kind is
a miss, exactly like an absent one.

| Node kind | `String` | `Int` | `Float` | `Bool` | `Duration` |
|---|---|---|---|---|---|
| `string` | yes | no | no | no | no |
| `integer` | no | yes | yes | no | no |
| `float` | no | truncates toward zero | yes | no | no |
| `time` (`10s`) | no | truncates toward zero | seconds | no | yes |
| `boolean` | no | no | no | yes | no |
| `null` | no | no | no | no | no |
| `object`, `array` | no | no | no | no | no |

Numeric kinds convert between themselves because the C does. `Int` on `2.9`
yields `2`, and on `-2.9` yields `-2`.

`Duration` is the one that catches people. UCL has a real time type, so `10s`
and `2min` are `KindTime` and read as durations, but a bare `30` is
`KindInt` and `Duration` misses on it. Reading a plain number as seconds would
invent a unit the document did not write.

A string holding raw binary yields `""` and `true`, matching the engine.

### Kinds

`Node.Kind()` reports the type, and its `String` method gives UCL's own name
for it: `object`, `array`, `integer`, `float`, `string`, `boolean`, `time`,
`null`, `userdata`, or `invalid`. The names come from the engine rather than a
second table here, so they cannot drift.

The zero `Node` and a nil `*Node` are both `KindInvalid`, and every method is
safe to call on either. That is deliberate: a chain of lookups that misses
returns a nil node, and calling `Kind()` or an accessor on it must not panic.

`Node.Len()` reports what a container holds — keys for an object, elements for
an array, and 0 for everything else. It is a question about the value, not
about how many values the key holds: a key written twice reports 0, and its
values are reached with `Values`.

Note that `compat/libucl`'s `Len` returns a *string's byte length*, because
that is what the C does. Both are right in their own package; nothing in a UCL
document depends on the Go spelling, so the idiomatic one follows `len()` on a
map or slice.

## Paths

`Get` resolves a dot-separated path and reports whether it found anything:

```go
node, ok := doc.Get("server.tls.cert")
```

Path segments index arrays by number, so one path descends through objects and
arrays alike:

```go
host, ok := doc.String("servers.0.host")
port, ok := doc.Int("servers.1.port")
matrix, ok := doc.Int("m.1.0")          // nested arrays work too
```

`Index(i)` is the direct form for an array element and misses on a negative or
out-of-range index.

Two limits worth knowing:

* **A numeric segment indexes an array, not a repeated key.** A key written
  twice is not addressable by position; use `Values`. This matches
  `ucl_array_find_index`, which reports nothing for one.
* **A key containing a dot is not reachable by path.** `"a.b" = 1` and
  `a { b = 2 }` are different keys, and `Get("a.b")` finds the nested one.
  Use `All()` to reach a dotted key.

## Iteration

`All` ranges over an object's keys in the order the document wrote them:

```go
for key, v := range doc.All() {
	fmt.Printf("%s: %s\n", key, v.Kind())
}
```

It yields nothing for any other kind, rather than panicking, so it is safe to
call on a node you have not checked.

`Values` yields every value a key holds, and `Elements` looks inside an array.
They answer different questions, and neither guesses which you meant:

```go
ports, _ := doc.Get("ports")     // ports = [80, 443]
for p := range ports.Elements() {   // 80, then 443
	n, _ := p.Int("")
}
for v := range ports.Values() {     // once: the array itself
	// v.Kind() == KindArray
}
```

### Repeated keys are lists

A key given more than once is UCL's normal way of writing a list, and these
two documents emit identical JSON:

```
listen = "10.0.0.1:80";      listen = ["10.0.0.1:80", "10.0.0.2:80"]
listen = "10.0.0.2:80";
```

`Values` reads either of them the same way, because a key written once yields
one value and a key written twice yields two:

```go
n, _ := doc.Get("listen")
for v := range n.Values() {
	addr, _ := v.String("")
}
```

**A typed accessor refuses a key that holds more than one value:**

```go
addr, ok := doc.String("listen")   // "", false when listen was written twice
```

Returning the first would be indistinguishable from correctly reading a
single-valued key, and getting it wrong is quiet and expensive — a program
that reads `allowed_ip` with `String` and gets one of three rules has no way
to notice. Use `Values` for a key that may repeat.

A single-valued key is unaffected, so ordinary reads keep working, and the
nested loop below is correct for every key without a type switch:

```go
for key, v := range doc.All() {
	for value := range v.Values() {
		// once for a single value, N times for a repeated key
	}
}
```

`All` yields a key **once**, however many values it holds. It walks the
document's keys, not its values.

### Why the two are separate

Because collapsing them loses data. If `Values` looked inside an array when it
found one, then for a repeated key whose values are arrays:

```
allow = [10];
allow = [20];
```

it would expand the first and the second would be unreachable through the
entire API. Keeping the questions apart makes it ordinary:

```go
for v := range n.Values() {        // [10], then [20]
	for e := range v.Elements() {  // 10, then 20
	}
}
```

This is the engine's own split: its iterator either walks the values of a key
or expands a container, chosen by a flag, and `Values` and `Elements` are those
two operations. For the same reason `Len` and `Index` are container questions —
a repeated key reports `Len() == 0` and is not indexable, matching
`ucl_array_size` and `ucl_array_find_index`, which report nothing for it.

### Kind tells the two forms apart

`Kind()` is where the two spellings differ, deliberately:

```go
// listen = "a"; listen = "b";      ->  KindString
// listen = ["a", "b"]              ->  KindArray
```

A repeated key reports the type of its values, not `KindArray`, because that is
the only thing recording how the document was written. Re-emitting in the
config format preserves each shape, so a document read and written back keeps
the form its author chose. If `Kind` reported `KindArray` for both, that
distinction would be gone and the round trip would rewrite the file.

So: `Kind` says how a value was written, `Values` says what the key holds, and
`Elements`, `Len` and `Index` say what is inside a container.

## Decoding into structs

`Get` and the typed accessors are for reading a handful of values out of a
document you mostly navigate by hand. When the shape of the whole document is
known ahead of time, a struct says that shape once and `Unmarshal` fills it:

```go
type Server struct {
	Host        string        `ucl:"host"`
	Port        int           `ucl:"port"`
	IdleTimeout time.Duration `ucl:"idle_timeout"`
}

type Config struct {
	Server Server   `ucl:"server"`
	Listen []string `ucl:"listen"`
}

var cfg Config
err := goucl.Unmarshal(data, &cfg)
```

```go
func Unmarshal(data []byte, v any, opts ...Option) error
func (n *Node) Decode(v any, opts ...Option) error
```

`Unmarshal` is `Parse` followed by `Decode` on the result. Call `Decode`
directly when you already have a `*Node` — after `Get`ing a subtree, say, or
when the same document is decoded into more than one shape.

### Struct tags

A field's UCL name comes from a `ucl` tag, falling back to a `json` tag when
there is no `ucl` one, and finally to the Go field name itself:

```go
Host string `ucl:"host"`        // reads "host"
Host string `json:"host"`       // reads "host" too, no ucl tag present
Host string                     // reads "Host"
```

`,omitempty` is honoured on `Marshal` the same way `encoding/json` honours
it, `-` skips the field in both directions, and an unexported field is never
looked at. Falling back to `json` rather than inventing a third tag means a
struct already tagged for `encoding/json` decodes UCL with no changes; add a
`ucl` tag only where the two formats should disagree on a name.

### Embedded fields are promoted

An anonymous struct field is flattened into its parent, exactly as
`encoding/json` flattens it — the embedding is invisible in the document:

```go
type Base struct {
	Name string `ucl:"name"`
}
type Derived struct {
	Base
	Extra string `ucl:"extra"`
}
```

```
name = "x";
extra = "y";
```

decodes into a `Derived` with both fields set, not into `Derived.Base.Name`.
Give the embedded field its own tag and it stops flattening, becoming an
ordinary nested field named by the tag instead. Where two embeddings would
promote the same name, the field at the shallower depth wins; at equal depth,
one explicit tag among the candidates wins; otherwise every conflicting field
is dropped rather than guessed at.

One consequence of matching `encoding/json` exactly is also one of its own
limitations: a nil embedded *pointer* to an **unexported** struct type cannot
be allocated during decode, because the field holding the pointer is itself
unexported and `reflect` refuses to set it even though the struct behind it
has exported fields of its own. That decodes as an error, not a panic.

### Repeated keys become slices

`Unmarshal` follows the same rule as `Values` (see [Repeated keys are
lists](#repeated-keys-are-lists) above): a key written more than once fills a
slice, one element per value —

```go
type doc struct {
	Listen []string `ucl:"listen"`
}
```

```
listen = "10.0.0.1:80";
listen = "10.0.0.2:80";
```

decodes to `Listen: []string{"10.0.0.1:80", "10.0.0.2:80"}` — but decoding
the same document into a `string` field is a `*DecodeError`, deliberately.
Returning the first value would be indistinguishable from correctly reading a
key that was only ever written once, which is exactly the wrong answer the
typed accessors were built to refuse.

### Strict mode and undecoded keys

By default an object key with no matching struct field is simply left out —
not an error, because a document is often shared with other consumers or
grows fields over time. It is still tracked, and `(*Decoder).Undecoded`
returns it:

```go
dec := goucl.NewDecoder(r)
if err := dec.Decode(&cfg); err != nil {
	return err
}
for _, path := range dec.Undecoded() {
	log.Printf("unrecognised key: %s", path)
}
```

Paths are dotted, in the same spelling `Node.Get` accepts, and reflect only
the most recent `Decode`. `DisallowUnknownFields` turns the same condition
into an error instead, for a program that would rather fail closed on an
unrecognised key than silently ignore it:

```go
err := goucl.Unmarshal(data, &cfg, goucl.DisallowUnknownFields())
```

### `Decoder`

```go
func NewDecoder(r io.Reader, opts ...Option) *Decoder
func (d *Decoder) Decode(v any) error
func (d *Decoder) Undecoded() []string
```

UCL is a whole-document format, not a framed one — there is nothing marking
where one value ends the way JSON's bracket-matching lets `json.Decoder` read
a stream of values. `Decoder.Decode` reads everything the reader has, parses
it once, and decodes it; a second call finds the reader drained and returns
`io.EOF`, matching `json.Decoder`'s shape without pretending UCL streams the
way JSON does.

### Options

`Parse`, `ParseString`, `ParseFile`, `Unmarshal`, `NewDecoder` and
`Node.Decode` all take the same `Option` values:

```go
func WithVariable(name, value string) Option
func WithMacro(name string, fn MacroFunc) Option
func DisallowUnknownFields() Option
```

`WithVariable` registers a `${name}` a document can reference, and works on
any of the parsing entry points, decode or not. `WithMacro` registers a
user-defined `.macro`:

```go
type MacroFunc func(data []byte, args *Node) error
```

`args` holds a macro invocation's parenthesised `(key=value)` arguments as a
`*Node`, or nil when there were none. Returning an error fails the parse with
a `*SyntaxError` whose `Code` is `ErrMacro` — which is a divergence from the
C worth knowing about: a libucl macro handler signals failure by returning
false, and the engine then aborts the chunk *without recording an error at
all*, leaving a caller with what looks like a successful parse of an empty
document. `WithMacro` closes that gap in the Go layer rather than reproducing
it, because a macro that can only fail silently is not a usable extension
point. `docs/notes/idiomatic.md` has the reference-verified transcript and the
reasoning in full.

`Node.Decode` accepts the same `Option` values for symmetry with `Unmarshal`,
but a `*Node` has already been parsed, so `WithVariable` and `WithMacro` have
nothing left to act on — only `DisallowUnknownFields` does anything there.

## Comments

Comments are the main reason to choose UCL over JSON, so they survive a parse
and a config-format round trip.

```go
func (n *Node) Before() []string   // the block written above this node
func (n *Node) After() []string    // the block written below it
```

Both return the comment lines in source order, including their `#`, or nil.

There are exactly two positions, not yaml.v3's three, because the engine
attaches one comment chain per node with a single placement bit. Inventing a
third would fabricate a distinction the tree cannot hold, and would break the
round trip.

### UCL has no line comments

This is the surprise. The parser accumulates comments until the next node is
built, so a comment trailing a value on the same line binds to the **next**
node, not to the value it sits beside:

```
a = 1; # this comment belongs to b
b = 2;
```

`a.After()` is nil and `b.Before()` is `["# this comment belongs to b"]`. There
is no accessor for a trailing comment because UCL has no concept for one to
hold.

Consecutive comments merge into one block, which has a second consequence: a
comment written after the last value of an object joins the following node's
block and moves when the document is re-emitted.

Only a comment with nothing following it — at the end of a document, or at the
end of a container — lands in `After`. In

```
o {
  a = 1;
  # inside trailing
}
b = 2;
```

the comment is `a.After()`, not `b.Before()`.

All of this is the C's behaviour, reproduced rather than corrected, and pinned
by `comments_test.go` so nobody fixes it by accident.

## Emitting

```go
func (n *Node) Encode(w io.Writer, f Format) error
func (n *Node) Bytes(f Format) ([]byte, error)
```

Four formats:

| Format | Output |
|---|---|
| `FormatConfig` | UCL's own configuration syntax |
| `FormatJSON` | indented JSON |
| `FormatJSONCompact` | JSON with no insignificant whitespace |
| `FormatYAML` | YAML |

Any node encodes, not only the document root, so `Get` then `Bytes` extracts a
subtree. A scalar encodes as a bare JSON value.

**Only `FormatConfig` carries comments.** The other three drop them, exactly as
the C does. A document parsed with comments and written as JSON loses them
silently, because JSON has nowhere to put them.

Converting UCL to JSON is therefore a parse and an encode:

```go
doc, err := goucl.ParseFile("app.conf")
if err != nil {
	return err
}
out, err := doc.Bytes(goucl.FormatJSON)
```

The bytes come from the same emitter, over the same nodes, that the golden
corpus was captured from, so a round trip cannot drift from the C by
construction. A `Format` that is not one of the four constants returns
`ErrFormat`; encoding an invalid node returns an error rather than empty output.

### Encoding Go values

`Marshal` is the reverse of `Unmarshal`: a Go value becomes a UCL document
without a `*Node` in sight.

```go
func Marshal(v any) ([]byte, error)
```

```go
out, err := goucl.Marshal(cfg)
```

`Marshal` always writes `FormatConfig` — there is no options parameter,
because it is the one format that round-trips through `Unmarshal` without
loss; reach for `Node` and `Bytes` above for the other three. Map keys are
sorted before encoding, so two calls on the same map produce the same bytes
even though Go's own map iteration order is randomised per run.

A type the format has no representation for — a channel, a function, a
complex number, an unsafe pointer — is an `*UnsupportedTypeError` naming the
type and the path. A value reachable from itself through a pointer, map or
slice is a `*CycleError` rather than an infinite walk.

Two limits are worth stating plainly, because both come from the C rather
than from this package, and both are covered with the reference-verified
evidence in `docs/notes/idiomatic.md`:

* **A `time.Duration` survives a tree round trip, not a text one.** `Marshal`
  encodes it as a UCL time (`10s`), and `Node.Duration` reads a time back, but
  the config emitter has no textual way to mark a number as meaning seconds.
  A document written out and read back in has already lost the distinction:
  `10s` comes back as `10.0`, and `Kind()` reports `KindFloat`, not
  `KindTime`. This is the language, not a gap in the encoder — the same is
  true of a `10s` a human wrote by hand and passed through the C.
* **A `[]byte` does not survive a text round trip if it holds control
  bytes.** UCL strings hold raw bytes, so `Marshal` writes one untouched
  rather than base64-encoding it, but the config emitter still replaces any
  byte below `0x20` or `0x7F` with the six-character escape `\uFFFD` on the
  way out, which reads back as the Unicode replacement character rather than
  as the byte written. A high byte such as `0xFF` is unaffected and passes
  through.

### `Encoder`

```go
func NewEncoder(w io.Writer, opts ...EncodeOption) *Encoder
func (e *Encoder) Encode(v any) error
func WithFormat(f Format) EncodeOption
```

```go
enc := goucl.NewEncoder(w, goucl.WithFormat(goucl.FormatJSON))
err := enc.Encode(cfg)
```

Without `WithFormat`, an `Encoder` writes `FormatConfig`, same as `Marshal`.
`EncodeOption` is deliberately not the same interface as the `Option` parsing
and decoding take: `WithFormat` means nothing to a parse, and
`DisallowUnknownFields` means nothing to an encode, so a call mixing the two
up is a compile error instead of a silent no-op.

## Errors

Every parse failure returns a `*SyntaxError`, so `errors.As` against it always
succeeds on an error from `Parse`, `ParseString` or `ParseFile`:

```go
doc, err := goucl.ParseFile("app.conf")
if err != nil {
	var se *goucl.SyntaxError
	if errors.As(err, &se) {
		fmt.Printf("%s\n%s", se, se.Excerpt())
	}
	return err
}
```

```go
type SyntaxError struct {
	File    string     // the file the error is in, or "" for non-file input
	Line    int        // 1-based
	Column  int        // 0-based
	Code    ErrorCode
	Message string
}
```

`Column` is **0-based**. That is not Go's usual convention, and it is
deliberate: it is what the engine reports and what `Message` already says, and
the two agreeing matters more than matching `go vet`. `Excerpt` places the
caret correctly either way.

`Error()` returns `Message` unchanged. The engine's text already names the
file, line and column, so prefixing a second, differently formatted position
would say everything twice.

`File` names the file the error is really in, which is not always the file you
passed to `ParseFile`: an `.include` moves the parse into another file, and
`Line`, `Column` and `Excerpt` all refer to whichever file that is. If the
included file cannot be re-read to quote it, `Excerpt` returns `""` rather than
quoting the wrong file at the same line number.

`Excerpt()` renders the offending line with a caret under the column:

```
2 | b = ;
  |     ^
```

It returns `""` when the source is unavailable or the position does not fall
within it, so print it conditionally or accept an empty line. Tabs in the
source are preserved in the caret line, so the caret stays aligned at any tab
width.

### Do not switch on `Code`

`Code` classifies the failure — `ErrSyntax`, `ErrUnpaired`, `ErrNested`,
`ErrMacro` and so on, with the same numeric values as the C enum. But the
engine does not classify every path. A failure raised while processing a macro
— an `.include` naming a file that does not exist, a glob matching nothing, a
document including itself — carries no code, and `Code` is then `ErrOK` on an
error that is perfectly real.

`ErrOK` therefore means "unclassified", not "fine". A top-level file that
cannot be opened is the one such case that can be identified without guessing,
and it reports `ErrIO`; the rest keep whatever the engine set.

So treat a non-nil error as the failure signal, read `Message` for the detail,
and never write a switch that assumes one of the other codes must apply.

### `*DecodeError`

A parse failure is always a `*SyntaxError`. A document that parses fine but
does not fit the shape `Unmarshal` or `Decode` was asked to fill it into —
a type mismatch, an unsupported target kind, a strict-mode unknown key — is a
different failure, and it is a different type:

```go
type DecodeError struct {
	Path    string // dotted, in the spelling Node.Get accepts
	Message string
	Err     error  // the underlying cause, if any; Unwrap reaches it
}
```

```go
var de *goucl.DecodeError
if errors.As(err, &de) {
	fmt.Printf("%s: %s\n", de.Path, de.Message)
}
```

`Path` is relative to whatever `Decode` was called on, empty for a failure at
the root. Keeping the two error types apart matters because they call for
different responses: a `*SyntaxError` means the document itself is broken and
carries a position and an excerpt to show where; a `*DecodeError` means the
document is fine but does not match the Go shape asked of it, and carries a
field path instead.

## What does not exist yet

The library does not build or edit documents. Specifically, none of these
exist:

* generic accessors (`Get[T]`) — deferred rather than rejected, because
  adding them later breaks nobody and removing them would
* per-node position — the engine records no line or column on a node; only
  the parser knows one, and only while parsing. Errors carry a position and
  nodes do not
* tree construction and editing of any kind
* `WithFS`, an opt-in sandbox restricting where an `.include` may read from —
  it needs the engine to let this layer intercept an include before the
  engine opens it, which does not exist yet either

MessagePack, S-expressions and JSON Schema validation are not on the
idiomatic surface. Whether they should be is an open question in `DESIGN.md`
rather than a settled no: the text formats shipped alone because adding a
format later breaks nobody and withdrawing one would. They remain reachable
through the lower-level entry points and `compat/libucl`.

## Migrating from the cgo binding

`compat/libucl` is a drop-in replacement for
`github.com/mitchellh/go-libucl`, package name and all. The migration is an
import-path change:

```diff
-import "github.com/mitchellh/go-libucl"
+import "github.com/derekmarcotte/goucl/compat/libucl"
```

Nothing else changes. The `libucl` struct tag, `Object.Decode`, `Emit`,
`Iterate`, the `ObjectType` and `Emitter` constants and the parser flags all
keep their names, values and signatures.

The proof is that mitchellh's own test suite — `decoder_test.go`,
`object_test.go` and `parser_test.go`, decoding through his unmodified
`decoder.go` — builds and passes against this implementation: **38 of his 39
tests, unmodified**. The 39th is disabled because it is flaky upstream,
asserting a fixed order over a Go map iteration; that was verified by building
his original cgo binding against the C library and running it 40 times, not
inferred. The evidence sits beside the test, a deterministic replacement is in
`decoder_flaky_test.go`, and `tools/check-compat-identity.sh` proves the
carried-over files are otherwise untouched.

Two things to know:

* **The C memory management is inert.** `Object.Ref`, `Object.Close`,
  `ObjectIter.Close` and `Parser.Close` do nothing, because the garbage
  collector owns lifetime now. Their signatures are unchanged so existing call
  sites keep compiling, including the asymmetry where `Object.Close` returns an
  error and `ObjectIter.Close` returns nothing. Double-close and
  use-after-close are safe.
* **`Object.Len` returns a string's byte length** for a string, not 0. That is
  the C's behaviour and a contract here, and it is the one place the two
  packages deliberately disagree.

### One upstream test is disabled

`TestObjectDecode_structKeys` is commented out in `decoder_test.go`, and it is
the only thing in the carried-over files that differs from upstream.

It asserts a fixed order for a list that `decoder.go` builds by ranging a map,
so it fails whenever Go's randomised iteration comes back the other way —
about one run in eight. That is a defect in the upstream test, verified rather
than assumed: the original cgo binding was built against the real C libucl and
its own suite run 40 times, failing twice with the identical message.

Keeping it enabled would fail your test runs one time in eight while proving
nothing about this implementation, so it is disabled in place with the evidence
recorded beside it, and the behaviour it meant to cover is asserted by
`TestObjectDecode_structKeys_Deterministic`.

If you would rather run it, strip the comment markers. Everything else is
byte-identical to upstream, which `tools/check-compat-identity.sh` verifies.

## Where to look next

* `example_test.go` — runnable examples for every entry point above, compiled
  and checked by `go test`
* `DESIGN.md` — why the API has this shape, and what is settled versus intent
* `docs/findings.md` — every place this port knowingly differs from libucl
  0.9.4, with the evidence
* `docs/notes/idiomatic.md` — how the root package is built and what surprised
  the author while building it
