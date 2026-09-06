package goucl

import (
	"iter"
	"strconv"
	"time"

	"github.com/derekmarcotte/goucl/internal/core"
)

// Kind is the type of a Node.
type Kind uint8

const (
	KindInvalid  Kind = iota // the zero Node, or a nil *Node
	KindObject               // key/value pairs
	KindArray                // array of values
	KindInt                  // integer
	KindFloat                // floating point
	KindString               // string
	KindBool                 // boolean
	KindTime                 // a duration, held as seconds
	KindNull                 // null
	KindUserdata             // opaque payload; never produced by parsing
)

// String names the kind.
//
// The names come from the engine, which is where UCL's type names are defined
// (ucl_object_type_to_string). Keeping a second table here would be two lists
// that have to agree forever.
func (k Kind) String() string {
	t, ok := k.coreType()
	if !ok {
		return "invalid"
	}
	return t.String()
}

// coreType maps back to the engine's type. Every kind but KindInvalid has one.
func (k Kind) coreType() (core.Type, bool) {
	switch k {
	case KindObject:
		return core.TypeObject, true
	case KindArray:
		return core.TypeArray, true
	case KindInt:
		return core.TypeInt, true
	case KindFloat:
		return core.TypeFloat, true
	case KindString:
		return core.TypeString, true
	case KindBool:
		return core.TypeBoolean, true
	case KindTime:
		return core.TypeTime, true
	case KindNull:
		return core.TypeNull, true
	case KindUserdata:
		return core.TypeUserdata, true
	}
	return 0, false
}

func kindOf(t core.Type) Kind {
	switch t {
	case core.TypeObject:
		return KindObject
	case core.TypeArray:
		return KindArray
	case core.TypeInt:
		return KindInt
	case core.TypeFloat:
		return KindFloat
	case core.TypeString:
		return KindString
	case core.TypeBoolean:
		return KindBool
	case core.TypeTime:
		return KindTime
	case core.TypeNull:
		return KindNull
	case core.TypeUserdata:
		return KindUserdata
	}
	return KindInvalid
}

// Node is a value in a parsed UCL document.
//
// A Node is a view onto the engine's tree rather than a copy of it. That is
// what makes Encode exact: the bytes come from the same emitter, over the same
// nodes, that the golden corpus was captured from, so a parse/encode round
// trip cannot drift from the C by construction.
//
// The zero Node and a nil *Node are both KindInvalid, and every method is safe
// to call on either.
type Node struct {
	obj *core.Object

	// comments is the parser's side table, shared by every node from one
	// parse. The engine keys comments by node pointer rather than storing
	// them on the node, so a child must carry the table with it.
	comments core.EmitComments
}

// wrap builds a child node carrying this node's comment table.
func (n *Node) wrap(o *core.Object) *Node {
	if o == nil {
		return nil
	}
	return &Node{obj: o, comments: n.comments}
}

// Kind reports the node's type.
func (n *Node) Kind() Kind {
	if n == nil || n.obj == nil {
		return KindInvalid
	}
	return kindOf(n.obj.Type())
}

// Key returns the node's key, or "" for an array element or the document root.
func (n *Node) Key() string {
	if n == nil || n.obj == nil {
		return ""
	}
	return string(n.obj.Key())
}

// Len reports the number of children: keys for an object, elements for an
// array, and 0 for every scalar.
//
// This deliberately differs from the compatibility layer's Len, which returns
// a string's byte length because that is what the C does. Nothing in a UCL
// document depends on the Go spelling, so this one follows len() on a map or
// slice instead.
func (n *Node) Len() int {
	switch n.Kind() {
	case KindObject:
		count := 0
		var it core.Iter
		for elt := core.Iterate(n.obj, &it, true); elt != nil; elt = core.Iterate(n.obj, &it, true) {
			count++
		}
		return count
	case KindArray:
		return int(core.ArraySize(n.obj))
	}
	return 0
}

// Get resolves a dot-separated path and reports whether it was found.
//
// Path segments index arrays by number, so "servers.0.host" works. An empty
// path returns the node itself, which is what lets the typed accessors below
// read the same whether or not they are given a path.
func (n *Node) Get(path string) (*Node, bool) {
	if n == nil || n.obj == nil {
		return nil, false
	}
	if path == "" {
		return n, true
	}
	found := n.obj.LookupPath(path)
	if found == nil {
		return nil, false
	}
	return n.wrap(found), true
}

// Index returns the i'th element of an array.
func (n *Node) Index(i int) (*Node, bool) {
	if n.Kind() != KindArray || i < 0 {
		return nil, false
	}
	elt := core.ArrayFindIndex(n.obj, uint32(i))
	if elt == nil {
		return nil, false
	}
	return n.wrap(elt), true
}

// All iterates an object's keys in insertion order. It yields nothing for any
// other kind.
//
// A key given more than once in a document forms an implicit array, and is
// yielded once, as the head of that chain. Range over the value's Values to
// reach the rest:
//
//	for key, v := range doc.All() {
//		for elt := range v.Values() {
//			...
//		}
//	}
func (n *Node) All() iter.Seq2[string, *Node] {
	return func(yield func(string, *Node) bool) {
		if n.Kind() != KindObject {
			return
		}
		var it core.Iter
		for elt := core.Iterate(n.obj, &it, true); elt != nil; elt = core.Iterate(n.obj, &it, true) {
			if !yield(string(elt.Key()), n.wrap(elt)) {
				return
			}
		}
	}
}

// Values iterates an array's elements, or the implicit array a repeated key
// forms. Anything else yields itself once, because in UCL a lone value and a
// one-element implicit array are the same thing.
//
// Both walks are the engine's. Its iterator expands a container when asked and
// otherwise walks the implicit chain, which is exactly the distinction here,
// so neither traversal is reimplemented.
func (n *Node) Values() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if n == nil || n.obj == nil {
			return
		}
		expand := n.Kind() == KindArray
		var it core.Iter
		for elt := core.Iterate(n.obj, &it, expand); elt != nil; elt = core.Iterate(n.obj, &it, expand) {
			if !yield(n.wrap(elt)) {
				return
			}
		}
	}
}

// --- Typed access ---------------------------------------------------------
//
// Each accessor takes a path and reports whether it found a node of a usable
// kind, so a missing key and a present-but-wrong-kinded one are both false and
// neither is mistaken for a real zero. Pass "" to read the node itself.

// String returns a string value. Only a string node yields one; no other kind
// is rendered. A string holding raw binary yields "" and true, matching the
// engine.
func (n *Node) String(path string) (string, bool) {
	v, ok := n.Get(path)
	if !ok {
		return "", false
	}
	return core.ToStringSafe(v.obj)
}

// Int returns an integer value. A float or a time truncates toward zero, as
// the C does.
func (n *Node) Int(path string) (int64, bool) {
	v, ok := n.Get(path)
	if !ok {
		return 0, false
	}
	return core.ToIntSafe(v.obj)
}

// Float returns a floating point value. An integer or a time converts.
func (n *Node) Float(path string) (float64, bool) {
	v, ok := n.Get(path)
	if !ok {
		return 0, false
	}
	return core.ToDoubleSafe(v.obj)
}

// Bool returns a boolean value. Only a boolean node yields one.
func (n *Node) Bool(path string) (bool, bool) {
	v, ok := n.Get(path)
	if !ok {
		return false, false
	}
	return core.ToBooleanSafe(v.obj)
}

// Duration returns a time value, such as 10s or 2min.
//
// UCL holds a time as a floating point number of seconds, so this scales by
// time.Second. Only a time node yields one: a bare number parses as KindInt or
// KindFloat and is not silently read as seconds.
func (n *Node) Duration(path string) (time.Duration, bool) {
	v, ok := n.Get(path)
	if !ok || v.Kind() != KindTime {
		return 0, false
	}
	secs, ok := core.ToDoubleSafe(v.obj)
	if !ok {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// --- Comments -------------------------------------------------------------

// Before returns the comment lines the engine emits immediately above this
// node, in source order, or nil if there are none.
//
// Two things about UCL comments are worth knowing before relying on this.
//
// A comment trailing a value on the same line is not attached to that value:
// it belongs to whatever node comes next, because the parser accumulates
// comments until the next node is built. UCL has no notion of a line comment,
// so there is no accessor here to hold one.
//
// Consecutive comments accumulate into a single block. A comment written after
// the last value of an object therefore joins the next node's block rather
// than staying where it was written, and re-emitting moves it. That is the
// engine's behaviour and the C's, so it is reproduced rather than corrected.
func (n *Node) Before() []string {
	return n.commentsAt(false)
}

// After returns the comment lines the engine emits immediately below this
// node. Only a comment with nothing following it -- at the end of a document
// or a container -- lands here.
func (n *Node) After() []string {
	return n.commentsAt(true)
}

func (n *Node) commentsAt(after bool) []string {
	if n == nil || n.obj == nil || n.comments == nil {
		return nil
	}
	head := n.comments[n.obj]
	if head == nil {
		return nil
	}
	// FlagInherited on the chain head is the engine's placement bit: set
	// means the block is emitted after the node, clear means before.
	isAfter := head.Flags()&core.FlagInherited != 0
	if isAfter != after {
		return nil
	}
	var out []string
	for cur := head; cur != nil; cur = cur.Next {
		text, _ := core.ToStringSafe(cur)
		out = append(out, text)
	}
	return out
}

// GoString renders the node for %#v, naming the kind and the key.
func (n *Node) GoString() string {
	if n.Kind() == KindInvalid {
		return "goucl.Node(invalid)"
	}
	if k := n.Key(); k != "" {
		return "goucl.Node(" + n.Kind().String() + " " + strconv.Quote(k) + ")"
	}
	return "goucl.Node(" + n.Kind().String() + ")"
}
