package goucl

import (
	"fmt"

	"go.marcottesoftware.ca/goucl/internal/core"
)

// Option configures parsing and decoding: Parse, ParseString, ParseFile,
// Unmarshal, NewDecoder and Node.Decode all take a variadic list of them.
//
// EncodeOption configures encoding instead. DESIGN.md §7's sketch shows one
// Option type shared by both directions, but that sketch is explicitly
// "illustrative only". A shared type would let
// NewEncoder(w, DisallowUnknownFields()) compile and silently do nothing --
// DisallowUnknownFields has no meaning without a decode to be strict about.
// Two types make that a compile error instead of a silent no-op, at the cost
// of two interfaces where the sketch had one.
//
// The method is unexported so the set of implementations stays closed to
// this package.
type Option interface {
	applyOption(*parseConfig)
}

// EncodeOption configures encoding. See Option's doc comment for why this is
// a separate type rather than Option itself.
type EncodeOption interface {
	applyEncodeOption(*encodeConfig)
}

// optionFunc adapts a plain function to Option, the same shape
// http.HandlerFunc uses for http.Handler.
type optionFunc func(*parseConfig)

func (f optionFunc) applyOption(c *parseConfig) { f(c) }

// encodeOptionFunc adapts a plain function to EncodeOption.
type encodeOptionFunc func(*encodeConfig)

func (f encodeOptionFunc) applyEncodeOption(c *encodeConfig) { f(c) }

// MacroFunc is a user-defined UCL macro, the Go-shaped counterpart of the
// engine's core.MacroHandler.
//
// data is the macro's raw argument text, with variables already expanded.
// args holds the parenthesised key/value arguments, if the macro invocation
// had any ("(key=value)" before the body); it is nil otherwise.
//
// DESIGN.md §2 requires that a macro be able to fail. The engine's handler
// reports that as a bool; MacroFunc reports it as an error instead, which is
// the shape Go expects and lets a macro say what went wrong.
type MacroFunc func(data []byte, args *Node) error

// nodeFromArgs wraps a macro's argument object as a Node, the same way
// (*Node).wrap does for every other child node produced by this package.
// args is nil when the macro invocation had no parenthesised arguments, and
// that nil is preserved rather than turned into a non-nil *Node wrapping a
// nil object -- every Node method already treats a nil *Node as KindInvalid,
// so a caller doing args.Get("foo") on a no-arguments macro gets ok == false
// rather than a panic.
func nodeFromArgs(args *core.Object) *Node {
	if args == nil {
		return nil
	}
	return &Node{obj: args}
}

// parseConfig accumulates what Parse's family of entry points need from
// Option before and after the engine runs: variables and macros to register
// on the core.Parser before parsing, and the strict flag decodeInto needs
// after it.
//
// Node.Decode also takes ...Option, for symmetry with the parsing entry
// points, but it has no parser to register vars or macros on -- there is no
// document left to parse, only a tree to decode -- so it reads only the
// strict flag out of the built config and silently ignores the rest. See
// (*Node).Decode.
type parseConfig struct {
	vars   []nameValue
	macros []namedMacro
	strict bool
}

type nameValue struct{ name, value string }

type namedMacro struct {
	name string
	fn   MacroFunc
}

// newParseConfig builds a parseConfig from a caller's options.
func newParseConfig(opts []Option) *parseConfig {
	c := &parseConfig{}
	for _, o := range opts {
		if o != nil {
			o.applyOption(c)
		}
	}
	return c
}

// applyToParser registers c's variables and macros on p, and returns a
// function reporting the error, if any, that a registered macro's handler
// returned.
//
// That indirection exists because the engine has no way to carry a macro
// handler's own failure back out through (*core.Parser).GetError: a handler
// that returns false does abort the chunk, but unless it also calls the
// engine's own (unexported) error-recording method, no error is recorded --
// see TestParserMacroFailureStopsChunkWithoutError in internal/core, which
// asserts exactly that as the engine's faithful, intentional behaviour. This
// package cannot reach that unexported method from the outside, so the
// wrapper below records the failure itself, in a closure, and the caller
// checks it after the add call returns.
func (c *parseConfig) applyToParser(p *core.Parser) (macroErr func() error) {
	for _, v := range c.vars {
		p.RegisterVariable(v.name, v.value)
	}

	var firstErr error
	for _, m := range c.macros {
		name, fn := m.name, m.fn
		p.RegisterMacro(name, func(data []byte, args *core.Object) bool {
			if firstErr != nil {
				// A previous macro invocation in this parse already failed.
				// Keep returning false without overwriting the first error,
				// so the reported failure is the one that happened first.
				return false
			}
			if err := fn(data, nodeFromArgs(args)); err != nil {
				firstErr = fmt.Errorf("macro %q: %w", name, err)
				return false
			}
			return true
		})
	}
	return func() error { return firstErr }
}

// WithVariable registers a variable a document can reference as ${name} or
// $name, mirroring (*core.Parser).RegisterVariable.
func WithVariable(name, value string) Option {
	return optionFunc(func(c *parseConfig) {
		c.vars = append(c.vars, nameValue{name, value})
	})
}

// WithMacro registers a user macro under name, mirroring
// (*core.Parser).RegisterMacro. See MacroFunc for the calling convention.
//
// Registering the same name twice adds a second registration; as in the
// engine, the most recently registered one wins, which lets a user macro
// shadow a built-in of the same name (".include", ".load", and so on).
func WithMacro(name string, fn MacroFunc) Option {
	return optionFunc(func(c *parseConfig) {
		c.macros = append(c.macros, namedMacro{name, fn})
	})
}

// DisallowUnknownFields makes an object key with no matching struct field a
// decode error instead of being silently collected for Undecoded.
func DisallowUnknownFields() Option {
	return optionFunc(func(c *parseConfig) {
		c.strict = true
	})
}

// encodeConfig accumulates what NewEncoder needs from EncodeOption.
type encodeConfig struct {
	format Format
}

// newEncodeConfig builds an encodeConfig from a caller's options, defaulting
// to FormatConfig -- UCL's own syntax, and the only format that round-trips
// through Marshal/Unmarshal without loss.
func newEncodeConfig(opts []EncodeOption) *encodeConfig {
	c := &encodeConfig{format: FormatConfig}
	for _, o := range opts {
		if o != nil {
			o.applyEncodeOption(c)
		}
	}
	return c
}

// WithFormat selects the output format for NewEncoder. Without it, an
// Encoder writes FormatConfig.
func WithFormat(f Format) EncodeOption {
	return encodeOptionFunc(func(c *encodeConfig) {
		c.format = f
	})
}
