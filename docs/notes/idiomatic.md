# The idiomatic layer

Notes on the root package, `goucl` — what it is, what surprised us building
it, and what was deliberately left out. `../../DESIGN.md` holds the decisions;
this holds the workings.

## What it is

A Go-shaped surface over `internal/core`. It reimplements nothing: parsing,
lookup, iteration, conversion and emission are all the engine's, called
through its exported API.

`Node` is a **view** onto the engine's tree, not a copy. That is a correctness
decision rather than an optimisation. Emission depends on per-node state — the
multiline and single-quote flags, priorities, implicit-array chains, the cached
string rendering in `valueTrash` — so a parallel tree would have to reproduce
all of it to emit the same bytes. Sharing the engine's nodes and calling
`EmitTextFull` makes a parse/encode round trip exact by construction rather
than by vigilance.

A `Node` therefore carries the parser's comment table along with the object
pointer, because the engine keys comments by node pointer rather than storing
them on the node. Every child must be handed the same table; `wrap` is the only
place a `Node` is built for that reason.

## What surprised us

Each of these was checked against the compiled C reference rather than reasoned
about, because reasoning about them produced the wrong answer twice.

### `ucl_parser_add_file` can return false on a document that parsed

`testdata/basic/9.in` is the case:

```
add_file=false error=(none) obj=yes errcode=0
```

The C returns false, records no error, and produces a complete document. The
engine reproduces this exactly. So success here is judged by the error state
and the presence of a document, never by the boolean — which is what libucl's
own harness does, and why that fixture emits its goldens perfectly well.
Treating the return value as authoritative would invent a failure the C does
not report.

### An empty document is an empty object

```
add_string=true error=(none) obj=yes type=object
```

Not an error, and not a nil tree. This is the same behaviour that makes
`.include()` with empty parens yield an empty arguments object. An early test
asserted the opposite and the oracle corrected it.

### UCL has no line comments

The parser accumulates comments until the next node is built, so a comment
trailing a value on the same line binds to the **next** node:

```
a = 1; # this comment belongs to b
b = 2;
```

Consecutive comments merge into one block, so a comment written after the last
value of an object joins the following node's block and moves when re-emitted.

This is why `Node` has `Before` and `After` and not yaml.v3's head/line/foot:
the engine attaches one comment chain per node with a single placement bit
(`FlagInherited`, set meaning "after"), so there are exactly two positions.
Inventing a third would fabricate a distinction the tree cannot hold and would
break the round trip. Both behaviours are asserted in `comments_test.go` so
that nobody "fixes" them later.

Note that the parser's `attachComment(obj, before)` parameter reads backwards:
it is called with `before=true` from the *after-value* paths. Trust the flag
and the emitter, not the parameter name.

### A time does not survive a text round trip as a time

`Marshal` encodes a `time.Duration` as a UCL time, and `Node.Duration` reads
one back, but a document that goes out as text and comes back in has lost the
distinction: the value returns as `KindFloat`, not `KindTime`.

That is the C's behaviour, not a gap in the encoder. The emitter has no textual
way to mark a number as seconds, so a time emits as a plain double. Verified
against the compiled reference rather than reasoned from the port:

```
t = 10s;        parses to type "number"
u = 2min;

config emit ->  t = 10.0;
                u = 120.0;
json emit   ->  {"t":10.0,"u":120.0}
```

A document author can observe this — `10s` written by hand comes back as
`10.0` from the C too — so it is the language, not a defect, and it is
reproduced rather than corrected. The consequence for the idiomatic layer is
that `Duration` round trips through the *tree* (`toNode` then `Node.Duration`)
and not through *text*. `marshal_test.go` asserts it at the tree level and
says why.

### The config emitter replaces control bytes

A UCL string holds raw bytes, and `Marshal` puts a `[]byte` in one without
base64 encoding it. Emitting is still lossy for part of the byte range: the
config emitter writes `�` for bytes below 0x20 and for 0x7F, while high
bytes pass through untouched. Verified against the reference:

```
insert the bytes  61 01 1f 7f ff 62
config emit ->    k = "a���\xffb";
```

So `0xFF` survives a text round trip and `0x01` does not. This is the C's
escaping, so it stands. `marshal_test.go`'s byte round trip deliberately picks
content outside the lossy range while still covering high bytes and embedded
structure, and a comment there points here.

### A failing macro is an error here and is not one in the C

`WithMacro` takes a `MacroFunc` that returns an error, because DESIGN.md §2
asked for a macro that can fail. Delivering that meant diverging from the
engine, which cannot express it.

In the C, a macro handler returning `false` aborts the chunk, records **no**
error, and leaves a document behind. Verified against the reference, with a
handler that does nothing but return `false`:

```
add_chunk = false
get_error = (none)
object    = yes        (and it emits as empty)
```

The engine reproduces that faithfully, and `internal/core` asserts it on
purpose. Two decisions then collide. `finish` deliberately ignores what the
add call returned, because a document that parses perfectly well can still
return `false` (see the `add_file` note above). So the one signal the C gives
for a failed macro is precisely the signal this package had already decided
not to trust -- and a macro failure would surface as a successful parse of an
empty document.

`WithMacro` closes the gap above the engine rather than inside it: the wrapper
captures the first error its `MacroFunc` returns, and the parse entry points
check it after the add and report a `*SyntaxError` with `ErrMacro` and the
parser's position. Nothing in `internal/core` changed, so the mirror stays
exact and the divergence lives entirely in the idiomatic layer -- which is
what that layer is for. A caller using the compatibility layer still sees the
C's behaviour unchanged.

## Deliberate differences from the compatibility layer

`Node.Len` returns the number of children — keys for an object, elements for an
array, 0 for a scalar. The compatibility layer's `Len` returns a string's byte
length, because that is what the C does. Both are right: the C's spelling is a
contract in `compat/libucl`, and nothing in a UCL *document* depends on what a
Go method is called, so this one follows `len()` on a map or slice.

`Column` in `SyntaxError` is 0-based, which is not the convention in Go's own
tools. It is what the engine reports and what the message text already says,
and the two agreeing matters more than the convention. `Excerpt` places the
caret correctly either way.

`Error()` returns the engine's message unchanged. It already names the file,
line and column, so prefixing a second, differently formatted position would
say everything twice.

## Guarding the restatements

A public API cannot name a type from an `internal/` package, so `Kind` and
`Format` have to be declared here even though `core.Type` and `core.Emitter`
already exist. That is the one unavoidable duplication, and it is the kind that
rots: a type added to the engine would fall out of the idiomatic API silently.

`drift_test.go` closes it. It holds no list of its own — it enumerates values
upward and asks the *engine* which are real, by asking whether it names them,
then requires this package to account for every one in both directions. Names
come from `core.Type.String` rather than a second table.

The guard was verified by deleting a case from the mapping and confirming it
fails. A guard that cannot fail is not a guard.

## What is left out

* **Per-node position.** The engine records no line or column on an object;
  only the parser knows a position, and only while parsing. Errors carry one
  and nodes do not. Supplying it means adding fields to the mirrored
  `ucl_object_t` and setting them everywhere the parser builds a node — engine
  surgery for a feature nothing has asked for.
* **Generic accessors.** `Get[T]` can be added later without breaking anyone;
  removing it could not. Deferred on that asymmetry alone.
* **`Marshal`, `Unmarshal`, `Encoder`, `Decoder` and the options.** Next.
* **Tree construction and editing.** Everything here reads a parsed document.
  The question `DESIGN.md` §6.1 deferred — where a comment attaches when a
  document is built or edited programmatically — comes due with the encoder,
  because that is the first thing to build a tree rather than read one.

## Testing

The 168 goldens prove the engine, but they cannot be compared against `Parse`
directly: the C harness parses with `KEY_LOWERCASE`, registers an ABI variable,
sets file variables, and emits the **second** round trip. Reproducing that
needs parser options, which do not exist yet.

So the property tested is transparency. Every one of the 28 fixtures, in all
four text formats, must emit identical bytes through this package and through
the engine, and the two must agree on which inputs fail. That reuses the corpus
as an oracle without pretending the harnesses match.

## Multi-valued keys

A key written more than once is UCL's ordinary way of spelling a list — the
engine's own README calls it "a multi-value key (implicit array)" — and it
emits identically to an array literal:

```
listen = "a";                 listen = ["a", "b"]
listen = "b";
```

The engine does not gather those into an array node. It threads them through
the same `Next` chain it uses everywhere, so the node at `listen` *is* a string
that happens to have siblings. Verified against the reference:
`ucl_object_lookup` reports `type = string`, `ucl_array_size` reports 0, and
`ucl_array_find_index` misses.

### The hazard

`ucl_object_tostring` returns a bare `const char *`. It has no channel to say
"there are two values here", so it returns the first, and a C caller cannot
detect the truncation from the return value. Our accessors inherited that
exactly: `String("listen")` returned `"a"` with `ok == true`.

That is the one wrong answer comma-ok cannot signal, because it is
indistinguishable from correctly reading a single-valued key. A program reading
`allowed_ip` that way enforces the first rule and drops the rest, with no error
and no wrong-type signal. So the accessors refuse a key holding more than one
value. This is a deliberate divergence from `ucl_object_tostring`, and an
allowed one: what a Go method named `String` does is not something a UCL
document can observe. `compat/libucl` still returns the first, as the C does.

### Two questions, not one

How many values a key holds, and what is inside a container, are separate
questions. The engine keeps them separate — its iterator either walks the chain
or expands the container, chosen by a flag:

```
ucl_object_iterate(n, &it, false)  ->  walks the values of the key
ucl_object_iterate(n, &it, true)   ->  expands the container
```

`Values` and `Elements` are those two operations, and neither guesses which the
caller meant.

**An earlier version did guess**, picking the flag from the node's kind:
expand if it is an array, else walk the chain. That reads well until a repeated
key holds arrays:

```
x = [1];
x = [2];
```

The node is an array, so it expanded the first one, and `[2]` became
unreachable through the entire API — not by `Values`, `Index`, `Len` or any
path. The C has both operations and loses nothing; we had one and lost a value.
That is what decided the split.

For the same reason `Len` and `Index` are container questions only, matching
`ucl_array_size` and `ucl_array_find_index`. Making them count the chain
conflated the two axes again, and hid the array case rather than fixing it.

### Kind is deliberately not array

`Kind` reports the node's own type — `string` for a repeated key, `array` for a
literal. That is the only thing recording which form the author wrote, and the
config emitter round-trips each faithfully. Reporting `array` for both would
read better at the call site and would mean that reading a document and writing
it back could rewrite the file into a shape its author did not choose.

### Implementation note

Values reached through `Values` are marked as elements (`Node.elem`). They are
still chain members, so each still points at its siblings, and without the mark
each would look like a multi-valued key and refuse to be read — making `Values`
yield values that could not be used. The test suite caught that, not
inspection.

### Left alone

A repeated key whose values are arrays emits as `{"x":[1]}` in JSON: the C
drops all but the first. Recorded as finding #11, reproduced rather than
fixed. The values are reachable through the API; it is only that emitter which
loses them.
