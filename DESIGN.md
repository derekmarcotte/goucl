# Design: an idiomatic Go UCL library

**Status: being built.** The restructure in §5 is done: the engine moved to
`internal/core` and the drop-in to `compat/libucl`. The idiomatic surface
itself — §7's sketch — is not written yet.

The decisions in §6 have been settled, but **only §10's module path is marked
as such below**; §6.1–§6.4 still read as open questions and are yet to be
amended. Until that pass lands, this document is not a reliable record of what
was decided.

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

* **Includes are a security surface, not a convenience.** `docs/findings.md` #1
  records that a relative `.include` resolves against the *process working
  directory*, not the including file's directory. In a program reading
  semi-trusted configuration that is a footgun. Fixing it by construction is
  the single most valuable decision available (§6.2).
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

## 6. Decisions to make first

Each of these is hard to reverse once there is code.

### 6.1 Does the idiomatic tree preserve comments?

* **Yes** → the tree is a `Node` in the yaml.v3 sense, with comments attached,
  and that shape pervades every API that touches it.
* **No** → a simpler `Value`, and comment round-trip becomes a separate mode.

**Recommendation: yes.** Comments are a main reason to choose UCL over JSON,
and this is close to impossible to retrofit. The cost is a heavier tree type
and a decision about where a comment attaches when a document is edited
programmatically.

### 6.2 Are includes on by default, and where do they resolve?

* **A.** As libucl: on, resolving against the process working directory.
* **B.** On, resolving against the including file's directory.
* **C.** Off unless the caller supplies a root, and then rooted at an `fs.FS`.

**Recommendation: C.** It is explicit, trivially testable, sandboxable, and it
eliminates finding #1 by construction rather than by documentation. It is a
deliberate divergence from libucl and should be called out as one. `os.DirFS`
makes the common case a one-liner, and `fstest.MapFS` makes include behaviour
testable without touching a disk.

### 6.3 Typed access: generics or comma-ok?

* `ucl.Get[int](v, "a.b")` reads well and fails loudly, but sets a floor on the
  Go version and interacts awkwardly with user-defined types.
* `v.Int("a.b") (int, bool)` is duller and works everywhere.

No recommendation yet; wants prototyping against real call sites. They can also
coexist, with generics as sugar over the methods.

### 6.4 Is `Value` an interface or a concrete struct?

An interface lets users supply their own implementations and makes mocking
easy; a concrete struct is faster, simpler, and keeps the tree closed so
invariants hold. `encoding/json` and yaml.v3 both use concrete types.

**Leaning concrete**, but this interacts with 6.1 and should be settled with
it.

### 6.5 What does a decode error look like?

At minimum: file, line, column, the offending token, and the path within the
document. It should satisfy `errors.As` against a single exported type, and
render with a source excerpt. This is cheap to add now and near-impossible to
add later without breaking everyone's error handling.

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
* Minimum Go version, which 6.3 depends on.
