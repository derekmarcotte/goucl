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
func Parse(data []byte) (*Node, error)
func ParseString(s string) (*Node, error)
func ParseFile(name string) (*Node, error)
```

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

`Node.Len()` reports the number of children — values for a repeated key, keys
for an object, elements for an array, and 0 for every other scalar. Note that `compat/libucl`'s `Len` returns a
*string's byte length*, because that is what the C does. Both are right in
their own package; nothing in a UCL document depends on the Go spelling, so the
idiomatic one follows `len()` on a map or slice.

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

* **A numeric segment indexes an array or a repeated key**, since the two are
  the same document — see [Repeated keys are lists](#repeated-keys-are-lists).
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

`Values` ranges over an array's elements:

```go
ports, _ := doc.Get("ports")
for p := range ports.Values() {
	n, _ := p.Int("")
	// ...
}
```

### Repeated keys are lists

A key given more than once forms an implicit array. This is UCL's normal way
of writing a list, and these two documents are the same document:

```
listen = "10.0.0.1:80";      listen = ["10.0.0.1:80", "10.0.0.2:80"]
listen = "10.0.0.2:80";
```

Both emit `{"listen":["10.0.0.1:80","10.0.0.2:80"]}`, and both read the same
way. A key holding more than one value is a sequence:

```go
n, _ := doc.Get("listen")

n.Len()              // 2
n.Index(0)           // "10.0.0.1:80"
doc.Get("listen.1")  // "10.0.0.2:80"

for v := range n.Values() {
	addr, _ := v.String("")
}
```

**A typed accessor refuses a key that holds more than one value:**

```go
addr, ok := doc.String("listen")   // "", false
```

This is the one case where returning a value would be indistinguishable from
a correct read, and getting it wrong is quiet and expensive — a program that
reads `allowed_ip` with `String` and gets only the first of three rules has no
way to notice. Use `Values` or `Index` for a key that may repeat.

A single-valued key is unaffected: its `Len()` is 0, it is not indexable, and
the accessors read it normally. `Values` on it yields it once, because in UCL
a lone value and a one-element implicit array are the same thing — which is
what makes this correct for every key without a type switch:

```go
for key, v := range doc.All() {
	for elt := range v.Values() {
		// runs once for a single value, N times for a repeated key
	}
}
```

`All` yields a repeated key **once**, as the whole sequence, not once per
value.

### Kind tells the two forms apart

`Kind()` is the one place the two spellings differ, deliberately:

```go
// listen = "a"; listen = "b";      ->  KindString, Len 2
// listen = ["a", "b"]              ->  KindArray,  Len 2
```

A repeated key reports the type of its values, not `KindArray`, because that
is the only thing recording how the document was written. Re-emitting in the
config format preserves each shape, so a document read and written back keeps
the form its author chose. If `Kind` reported `KindArray` for both, that
distinction would be gone and the round trip would rewrite the file.

So: use `Kind` to learn how a value was written, and `Len`, `Index`, `Values`
and the accessors to work with what it holds.

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

## What does not exist yet

The library reads documents. It does not build or edit them, and it has no
reflect-based layer. Specifically, none of these exist:

* `Marshal` and `Unmarshal`
* `Encoder` and `Decoder`
* parser options — no `WithFS`, `WithVariable`, `WithMacro`,
  `DisallowUnknownFields`, and no way to set the parser flags the engine
  supports
* generic accessors (`Get[T]`) — deferred rather than rejected, because adding
  them later breaks nobody and removing them would
* per-node position — the engine records no line or column on a node; only the
  parser knows one, and only while parsing. Errors carry a position and nodes
  do not
* tree construction and editing of any kind

`DESIGN.md` §7 sketches the intended shape of the first three. Callers who need
the full engine behaviour today — macros, priorities, parser flags, MessagePack,
schema validation — reach it through `compat/libucl`.

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
`object_test.go` and `parser_test.go`, byte-for-byte unmodified, decoding
through his unmodified `decoder.go` — builds and passes against this
implementation: 39 of 39 tests.

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

### The known flake

`TestObjectDecode_structKeys` fails roughly one run in eight, and is left that
way on purpose. mitchellh's `decodeIntoStruct` collects fields into a
`map[*reflect.StructField]reflect.Value` and decodes in Go map iteration order,
so the `,decodedFields` list it produces is randomly ordered while the test
asserts `[]string{"Foo", "Bar"}`.

Nothing about this implementation is involved: a standalone program that builds
the same map from the same struct and does nothing else reverses the order in
12.5% of runs. The cgo binding had the same flake for the same reason. Both the
decoder and the test are unmodified, which is the entire point of running that
suite, so the flake comes across with them. `docs/notes/compat.md` records the
measurement.

If it fails in your CI, re-run it; do not read it as a port defect.

## Where to look next

* `example_test.go` — runnable examples for every entry point above, compiled
  and checked by `go test`
* `DESIGN.md` — why the API has this shape, and what is settled versus intent
* `docs/findings.md` — every place this port knowingly differs from libucl
  0.9.4, with the evidence
* `docs/notes/idiomatic.md` — how the root package is built and what surprised
  the author while building it
