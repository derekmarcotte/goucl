# How this API compares to other Go config libraries

Checked, not recalled. Every claim below was verified against the library's
own source or by running it, in September 2026, at these versions:

| | |
|---|---|
| `encoding/json` | Go 1.23 standard library |
| `gopkg.in/yaml.v3` | v3.0.1 |
| `github.com/BurntSushi/toml` | v1.6.0 |
| `github.com/pelletier/go-toml/v2` | v2.4.3 |
| `github.com/hashicorp/hcl/v2` | v2.24.0 (with `go-cty` v1.16.3) |

These are other people's libraries and they will move. Anything here is a
snapshot, and unlike the rest of `docs/` there is no oracle to re-run: if a
claim matters, check it again before leaning on it.

HCL is in the set because it is the closest analogue -- a configuration
*language* with a Go library, rather than a serialisation format -- and
because it comes from the author of the binding this port replaces.

## The short version

| | goucl | json | yaml.v3 | BurntSushi | go-toml/v2 | HCL |
|---|---|---|---|---|---|---|
| Unknown keys as a list | yes | no | partly | yes | yes | yes |
| Unknown keys as an error | yes | yes | yes | **no** | yes | yes |
| Parse error with source excerpt | yes | **no** | no | yes | yes | yes |
| Comment-preserving round trip | yes | no | yes | no | no | yes |
| Position on a *value* | **no** | no | yes | no | no | yes |
| Editable tree | **no** | no | yes | no | no | yes |
| Comma-ok typed access | yes | no | no | no | no | no |

The two bolded gaps in the goucl column are the honest weaknesses, and they
are the same decision seen twice. See "What it cannot do".

## Where it is deliberately unremarkable

`Marshal`/`Unmarshal`, `Encoder`/`Decoder`, struct tags with `omitempty`,
`DisallowUnknownFields`, and `encoding/json`'s embedding rules. A Go
programmer needs no documentation for any of it, which is the whole point:
novelty here would be a cost with no benefit.

The `json` tag fallback is a small thing the others do not offer -- yaml.v3
and go-toml/v2 both make you re-tag a struct -- and it means an existing
JSON-tagged configuration type usually works unchanged.

## Where it comes out ahead

**Unknown keys, both ways.** A typo'd key is the most common thing a user
does wrong, and reporting it is the highest-value feature a *configuration*
library has. The field is split:

* `encoding/json` errors on the **first** unknown field only, with no way to
  get a list. Observed: `json: unknown field "typo"` for a document with
  three.
* BurntSushi offers `MetaData.Undecoded() []Key` and **no error mode at
  all**; its own documentation says strictness is the caller's job.
* yaml.v3 has `KnownFields(bool)` and no list. Its `*yaml.TypeError` does
  carry one formatted message per offending field in `Errors []string`, so
  the names can be recovered by parsing strings, but there is no structured
  list of keys.
* go-toml/v2 has both, and its `DecodeError` exposes `Key()` and
  `Position()`.

goucl has both: `Undecoded()` returns every unknown key, nested ones
included, as dotted paths in the spelling `Node.Get` accepts, and
`DisallowUnknownFields` turns the first into an error. Only go-toml/v2 is
equally well equipped.

**Comma-ok typed access is genuinely unique.** No library in the set exposes
an accessor that separates *absent* from *present but the wrong type* from
*a real zero*:

* yaml.v3's `Node.Decode(v)` returns only `error`; there is no typed getter.
* BurntSushi needs two calls, `IsDefined` then `Type`, and neither is typed.
* go-toml/v2 has no node API at all -- v1's `Tree.Get` was dropped in v2.
* HCL's `cty.Value` accessors (`AsString`, `GetAttr`) **panic** on a
  mismatch.

`n.Int("db.port") (int64, bool)` is the design in `DESIGN.md` §6.3, and the
survey supports the argument that section makes: this shape is unusual
because most of these libraries are serialisation formats first, where a
whole-document unmarshal is the only access anyone wanted.

## Where UCL pushed it somewhere unusual

**Repeated keys have no counterpart** in JSON, YAML or TOML. `x = [1]; x =
[2];` is expressible and none of the three can say it, so `Values()` and
`Elements()` have nothing to imitate. HCL's repeated blocks are the nearest
relative. This is the part of the surface a JSON reader will find
strangest, and `notes/idiomatic.md` explains why collapsing the two
operations loses data.

**Two comment positions, not yaml.v3's three.** yaml.v3 has `HeadComment`,
`LineComment` and `FootComment` on its `Node`. UCL has one chain per node and
one placement bit, so there are exactly two. Forced, not chosen.

## What it cannot do

Three gaps, in the order they are likely to matter.

**No position on a value.** yaml.v3's `Node` carries `Line` and `Column`;
HCL attaches an `hcl.Range` to attributes and blocks themselves, not merely
to diagnostics. goucl reports a position for a *parse* error and nothing
else, so a caller who validates `port = 70000` after a successful parse
cannot say where the offending value came from. For a configuration library
that is close to the top of the list of things people want, and `DESIGN.md`
§6.4 records why the engine cannot supply it without surgery.

**The tree is read-only.** `Node` has no mutators. yaml.v3 can build a node
tree by hand, or decode one, edit it and re-marshal -- verified, the edit
round-trips. That is how comment-preserving rewriters are written. goucl
preserves comments through a *pure* round trip but offers no way to change
one value and write the file back, which is an ordinary configuration task.

These two are the same decision seen from either side. `Node` is a view onto
the engine's tree rather than a copy, which is exactly why a round trip is
byte-exact by construction -- and exactly why nothing can be edited or
annotated with a position. `DESIGN.md` §6.4 took that trade deliberately;
this is what it costs.

**`Decoder` is not a stream.** `json.Decoder` and yaml.v3's both read
successive documents from one reader -- verified for both. goucl's reads
everything on the first call and returns `io.EOF` after. This is defensible,
because UCL has no `---` and nothing frames one document from the next, but
the name leads people to expect otherwise.

## One choice worth second-guessing

`Option` and `EncodeOption` as two types is unusual. Nobody in the set uses
functional options at all:

* yaml.v3: methods -- `(*Decoder).KnownFields`, `(*Encoder).SetIndent`.
* go-toml/v2: chainable methods -- `(*Decoder).DisallowUnknownFields`,
  `(*Encoder).SetIndentTables`.
* BurntSushi: `Decoder` has no configuration at all; `Encoder.Indent` is a
  plain exported field.
* HCL: plain parameters -- `hclsimple.DecodeFile(name, ctx, target)`.

Methods on the decoder would have made the two-type split unnecessary:
`DisallowUnknownFields` cannot be passed to an encoder if it is not a method
on one. The reason it went the other way is that the split follows from
wanting options on the *one-shot* entry points, and `Parse(data,
WithVariable(...))` has no object to hang a method off.

That is a real reason rather than a preference, but it is a trade. It should
not be dressed up as following convention, because it does not: the majority
pattern here is methods on a decoder, and it is only a majority of two.

## The structural difference

Shipping `compat/libucl` **and** an idiomatic package is the thing least like
the rest of the field. Most ports force a migration. Keeping the old API
intact, with its proof -- mitchellh's own suite, unmodified -- is what makes
the new one free to be shaped without arguing about compatibility, and it is
why `DESIGN.md` §9 lists matching the C API as a non-goal for this layer.
