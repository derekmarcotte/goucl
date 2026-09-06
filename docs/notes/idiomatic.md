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
