# Design: an idiomatic Go UCL library

**Status: being built.** §5's restructure is done, and §6's decisions are all
settled and marked below. The tree, the parser entry points, the emitters and
the error type exist; `Marshal`, `Unmarshal`, `Encoder`, `Decoder` and the
options in §7's sketch do not yet.

Where building something corrected this document, the section says so rather
than quietly reading as though it were always right. §6.1 assumed yaml.v3's
three comment positions and UCL has two; §6.2 recommended re-rooting includes
and that was wrong; §6.4's sketch includes a per-node position the engine
cannot supply.

The current library is feature complete and passing; nothing here is required
to use it.

---

## 1. The problem

The port deliberately mirrors the C API one-to-one, because that is what let
libucl's own tests come across mechanically and what makes the goldens
meaningful. That was the right call for proving correctness. It is the wrong
shape for a library people enjoy using.

Two audiences are being served by one surface, badly:

* someone replacing `github.com/mitchellh/go-libucl` wants that exact API;
* someone reaching for UCL in a new program wants what `encoding/json`,
  `go-toml` and `yaml.v3` taught them to expect.

Trying to be both means the second audience meets `Ref() error`, `Close()`,
and a `Get` that returns `nil` on a miss.

## 2. How far the current surface is from idiomatic

The distance is real but almost entirely **mechanical, not architectural**.
The object model — a tree of typed values with a chain for duplicate keys — is
sound and would survive unchanged. What dates it is the surface:

| Current | Why it grates | Convention |
|---|---|---|
| `Close() error`, `Ref() error` | C refcounting leaking into Go | absent; the GC owns lifetime |
| `Emit(Emitter) (string, error)` never returns non-nil error | the signature lies | return it, or do not have it |
| `Get(key) *Object`, `nil` on miss | `.Get("a").Get("b").ToString()` silently yields `""` | comma-ok, or an error naming the path |
| `ToInt()`, `ToString()` zero on mismatch | a real `0` and a failed conversion are indistinguishable | comma-ok or typed error |
| `Len() uint` | `uint` for a count | `int` |
| `Iterate(bool) *ObjectIter` + `Close()` | manual iterator lifecycle | `iter.Seq2`, range-over-func |
| errors are opaque strings | line and column are not reachable programmatically | structured error, `errors.As` |
| `MacroFunc func(string)` | a macro cannot fail | returns `error` |
| no `Marshal`/`Unmarshal`, no `Encoder`/`Decoder` | the shape every Go programmer reaches for first | universal |
| `NewParser` → `AddString` → `Object()` | three calls and a mutable parser for the common case | one-shot `Unmarshal` |

The binding's `decoder.go` is the strongest part: a mapstructure-style reflect
decoder, reasonable for its time. It needs `encoding.TextUnmarshaler` support
and a `json` tag fallback to feel current, not replacing.

## 3. What to borrow

Conventions worth taking wholesale, with the library that does each best:

* **`Marshal`/`Unmarshal` plus `Encoder`/`Decoder`** — `encoding/json`. Not
  negotiable; it is the shape people expect before reading any documentation.
* **Reporting undecoded keys** — BurntSushi/toml's `MetaData.Undecoded()`. For
  a *configuration* language this is the highest-value feature on the list: it
  is how a program tells a user they typo'd a key. Most config libraries
  cannot do it.
* **Strict mode** — `DisallowUnknownFields`, as in `encoding/json` and
  go-toml/v2.
* **Comment-preserving round trip** — yaml.v3's `Node`, with head, line and
  foot comments attached to nodes. The closest existing precedent for what UCL
  needs.
* **Errors that render a source excerpt** with line, column and a caret —
  go-toml/v2 and BurntSushi both do this well.
* **Functional options** instead of flag bitmasks.

Watch `encoding/json/v2` upstream rather than designing against it; it may move
some of these conventions.

## 4. What has no precedent to copy

The interesting work is here, because serializers do not face it:

* **Includes reach the filesystem, and resolve surprisingly.**
  `docs/findings.md` #1 records that a relative `.include` resolves against the
  *process working directory*, not the including file's directory.

  This section originally called that "a security surface, not a convenience"
  and proposed fixing it by construction. That was the wrong reading. It is
  behaviour a UCL document can observe, which makes it the language rather than
  a defect, and it is replicated (§6.2). What is left for this layer is not a
  fix but an option: a caller who wants a sandbox can be given one, because an
  option changes no default.
* **Priorities and duplicate-merge strategies** are layered-configuration
  semantics — closer to koanf or viper than to a serializer.
* **Macros and variable expansion** need user-extensible hooks that can fail.
* **One tree, several output formats** (config, JSON, YAML, MessagePack).
* **Bundled schema validation**, which almost no config library ships.

An idiomatic UCL library is therefore *a serializer plus a small config
framework*, and the config half has no obvious model to copy.

## 5. Proposed structure

One module, three packages:

```
<host>/ucl                  idiomatic API — what people import
<host>/ucl/compat/libucl    drop-in for mitchellh/go-libucl, package libucl
<host>/ucl/internal/core    the C-mirror engine (today's goucl)
```

### Alternatives considered

**Three separate modules**, as first suggested. Rejected: three versions to
keep in lockstep for one cohesive thing, three sets of CI, and a release dance
every time the engine changes. Go charges nothing for packages you do not
import, so the split buys nothing.

**Keep the C-mirror public** (today's arrangement). Rejected as the default:
two public ways to do everything doubles the compatibility surface forever and
invites newcomers onto the wrong one. The mirror's value is as a *tested
engine* — the 168 goldens exercise it — not as an API.

The thing that currently forces it public is that the compatibility layer needs
it. Same-module `internal/` solves exactly that, which is the strongest single
argument for one module.

**Risk of `internal/`:** someone wanting exact C semantics loses the escape
hatch. Mitigated if the idiomatic layer exposes priorities, duplicate
strategies and macros as options; `compat/libucl` covers the rest. If a real
need appears, promoting a package out of `internal/` is easy — the reverse is
not.

## 6. Decisions

All settled. Each records what was decided and, where building it taught us
something the proposal had wrong, what that was.

### 6.1 Does the idiomatic tree preserve comments? SETTLED: yes

`Node` carries them, and a config-format round trip preserves them.

**But not in the yaml.v3 shape this section assumed.** Head, line and foot
comments have no counterpart in UCL. The engine attaches one comment chain per
node with a single placement bit, so there are exactly two positions, and
`Node` exposes exactly those: `Before` and `After`.

There is no line comment because UCL has no such concept. A comment trailing a
value on the same line binds to the *next* node, not the one it sits beside,
because the parser accumulates comments until the next node is built:

```
a = 1; # this comment belongs to b
b = 2;
```

Consecutive comments merge into one block, so a comment written after the last
value of an object joins the following node's block and moves when re-emitted.
Both behaviours are the C's, so both are reproduced and asserted in tests
rather than corrected.

The cost this section predicted -- "a decision about where a comment attaches
when a document is edited programmatically" -- is still unpaid, because nothing
here edits a tree yet. It comes due with the encoder.

### 6.2 Are includes on by default, and where do they resolve? SETTLED: A

**As libucl: on, resolving against the process working directory.**

This section recommended C, rooting includes at an `fs.FS`. That was wrong,
and §4's framing of includes as "a security surface, not a convenience" is
wrong with it. Behaviour a UCL document can observe is the language, not a
defect to design around: a document that works under the C must work
identically here. `findings.md` #1 is therefore replicated and documented.

An opt-in sandbox may still be added, because an option changes no default.

### 6.3 Typed access: generics or comma-ok? SETTLED: comma-ok

`n.Int("db.port") (int64, bool)`, and the same shape for `String`, `Float`,
`Bool` and `Duration`. Each takes a path, so `""` reads the node itself and
there is one spelling rather than two.

Generics were not ruled out, only deferred: adding `Get[T]` later breaks
nobody, removing it would. This is the reversible order, which is the whole
argument, since the section wanted prototyping before committing.

Comma-ok earns its keep on the case that motivated it. A missing key, a key
of the wrong kind, and a real zero are three different answers, and all three
occur constantly in configuration:

```go
v, ok := doc.Int("retries")   // 0, true   -- the document said 0
v, ok := doc.Int("absent")    // 0, false  -- no such key
v, ok := doc.Int("hostname")  // 0, false  -- present, but a string
```

### 6.4 Is `Value` an interface or a concrete struct? SETTLED: concrete

`Node` is a concrete struct, as in `encoding/json` and `yaml.v3`.

It holds the engine's node rather than copying it, which turned out to matter
more than the interface question. Emission depends on per-node state -- the
multiline and single-quote flags, priorities, implicit-array chains, the cached
string rendering -- so a parallel tree would have to reproduce all of it to
emit the same bytes. Sharing the engine's nodes makes a round trip exact by
construction rather than by vigilance.

**One field in §7's sketch cannot be delivered: `position`.** The engine
records no per-node line or column; only the parser knows a position, and only
while it is parsing. Errors therefore carry a position and nodes do not.
Adding one means adding fields to the mirrored `ucl_object_t` and setting them
everywhere the parser builds a node, which is engine surgery for a feature
nothing has asked for yet. Left undone deliberately.

### 6.5 What does a decode error look like? SETTLED

`*SyntaxError`, with `File`, `Line`, `Column` and `Code`, reachable through
`errors.As`, plus `Excerpt` for the caret rendering:

```
2 | b = ;
  |     ^
```

`Column` is 0-based, which is not Go's usual convention. It is what the engine
reports and what the message text already says, and the two agreeing matters
more than matching the 1-based convention of Go's own tools. `Message` is the engine's text, byte-identical to
the C's; it already names file, line and column, so `Error` returns it
unchanged rather than prefixing a second, differently formatted position.

## 7. Sketch

Illustrative only — argument, not specification.

```go
// The 90% case.
func Unmarshal(data []byte, v any) error
func Marshal(v any) ([]byte, error)

// Streaming and options.
func NewDecoder(r io.Reader, opts ...Option) *Decoder
func (d *Decoder) Decode(v any) error
func (d *Decoder) Undecoded() []Path      // the BurntSushi idea

func NewEncoder(w io.Writer, opts ...Option) *Encoder
func (e *Encoder) Encode(v any) error     // format chosen by option

// Options, replacing the flag bitmask.
func WithFS(fsys fs.FS) Option            // includes, rooted and sandboxed
func WithVariable(name, value string) Option
func WithMacro(name string, fn MacroFunc) Option
func DisallowUnknownFields() Option

// The tree, for programs that want it.
type Node struct{ /* kind, value, children, comments, position */ }
func (n *Node) Get(path string) (*Node, bool)
func (n *Node) All() iter.Seq2[string, *Node]
```

## 8. Migration

The existing application changes nothing: `compat/libucl` keeps the current
behaviour, and its proof stays what it is today — mitchellh's own test suite,
unmodified, passing byte-identically.

Moving to the idiomatic package would be a deliberate, file-by-file choice, not
a forced upgrade. Both can be used in one program.

## 9. Non-goals

* Rewriting the engine. It is tested against 168 goldens and a C oracle; that
  investment is why this phase is affordable at all.
* Matching libucl's C API in the idiomatic layer. That is `compat/libucl`'s
  job, and the reason it exists is so this layer is free.
* Bug-for-bug fidelity in the idiomatic layer. The four deliberate divergences
  in `docs/findings.md` already establish the principle: reproduce upstream
  bugs where callers depend on them, fix them where they produce wrong answers
  or crashes.

## 10. Open questions

* **Settled: the module path is `github.com/derekmarcotte/goucl`.** The import
  paths in §5 follow from it.
* Whether the schema validator belongs in the root package, its own package, or
  stays where it is.
* Whether MessagePack and S-expressions are part of the idiomatic surface or
  remain lower-level entry points.
* **Settled: the minimum Go version is 1.23**, for `iter.Seq2`.
