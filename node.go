package goucl

import (
	"iter"
	"strconv"
	"time"

	"go.marcottesoftware.ca/goucl/internal/core"
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

	// elem marks a node reached as one value of a repeated key. The engine
	// threads those values through the same Next chain, so such a node still
	// points at its siblings; without this it would look like the head of a
	// chain and be refused as multi-valued, which would make Values yield
	// values that cannot then be read.
	elem bool
}

// wrap builds a child node carrying this node's comment table.
func (n *Node) wrap(o *core.Object) *Node {
	if o == nil {
		return nil
	}
	return &Node{obj: o, comments: n.comments}
}

// wrapElem builds a node for one value of a repeated key.
func (n *Node) wrapElem(o *core.Object) *Node {
	if o == nil {
		return nil
	}
	return &Node{obj: o, comments: n.comments, elem: true}
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

// --- Multi-valued keys ---------------------------------------------------
//
// A key written more than once forms an implicit array, threaded through the
// engine's node chain rather than gathered into an array node:
//
//	listen = "10.0.0.1:80";
//	listen = "10.0.0.2:80";
//
// This is the ordinary way to write a list in UCL, and it emits as an array.
//
// How many values a key holds and what is inside a container are two separate
// questions, and the engine keeps them separate: its iterator either walks the
// chain or expands the container, chosen by a flag. Values and Elements are
// those two operations, and neither guesses which the caller meant.
//
// Collapsing them is a mistake worth not repeating. If Values expands an array
// when it finds one, then for a repeated key whose values are arrays --
// x = [1]; x = [2]; -- it expands the first and the second becomes
// unreachable through the whole API.
//
// The typed accessors refuse a key holding more than one value, because
// returning the first is indistinguishable from correctly reading a
// single-valued key. Len and Index stay container operations, matching
// ucl_array_size and ucl_array_find_index, which report nothing for a chain.
//
// Kind reports the node's own type -- string above, not array -- because that
// is the only thing distinguishing a repeated key from an array literal, and
// losing it would mean re-emitting a document in a shape its author did not
// write.

// chainLen counts the values a key holds: 1 for an ordinary key, more for a
// repeated one.
func (n *Node) chainLen() int {
	if n == nil || n.obj == nil {
		return 0
	}
	count := 0
	for cur := n.obj; cur != nil; cur = cur.Next {
		count++
	}
	return count
}

// multi reports whether this key holds more than one value. Unlike the
// container questions below, this one is about the key, not the value, so it
// applies whatever kind the values are.
func (n *Node) multi() bool {
	if n == nil || n.elem {
		return false
	}
	return n.chainLen() > 1
}

// Len reports the number of children a container holds: keys for an object,
// elements for an array, and 0 for everything else.
//
// It is a question about this value, not about how many values the key holds.
// A key written twice reports 0 here, as ucl_array_size does for a chain; use
// Values to reach them.
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
//
// A numeric segment addresses an array, not a repeated key, matching
// ucl_array_find_index. Use Values for a key written more than once.
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
// A key is yielded once however many values it holds, so this is a walk of
// the document's keys rather than of its values. Range the value's Values to
// reach them, which reads the same whether the key was written once or
// several times:
//
//	for key, v := range doc.All() {
//		for value := range v.Values() {
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

// Values iterates every value the key holds.
//
// A key written once yields one value, whatever kind it is; a key written
// several times yields each of them, in document order. This is the engine's
// chain walk, so it never looks inside a value: a key whose value is an array
// yields that array, not its elements. Use Elements for those.
//
//	listen = "10.0.0.1:80";     ->  two values
//	listen = "10.0.0.2:80";
//
//	ports = [80, 443]           ->  one value, an array
func (n *Node) Values() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if n == nil || n.obj == nil {
			return
		}
		// The values are marked as elements: each is still a member of the
		// chain, so its own successor pointer is set, and without the mark
		// each would look like a multi-valued key and refuse to be read.
		var it core.Iter
		for elt := core.Iterate(n.obj, &it, false); elt != nil; elt = core.Iterate(n.obj, &it, false) {
			if !yield(n.wrapElem(elt)) {
				return
			}
		}
	}
}

// Elements iterates an array's elements. It yields nothing for any other kind,
// including a repeated key, whose values are reached with Values.
//
//	ports = [80, 443]           ->  80, then 443
func (n *Node) Elements() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if n.Kind() != KindArray {
			return
		}
		var it core.Iter
		for elt := core.Iterate(n.obj, &it, true); elt != nil; elt = core.Iterate(n.obj, &it, true) {
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
//
// A key holding more than one value is also false, rather than the first of
// them. Reading one value out of several is the one wrong answer these
// accessors cannot signal, since it is indistinguishable from a correct read
// of a single-valued key. Use Values or Index for a key that may repeat.

// scalar resolves a path to a single value, refusing a repeated key.
func (n *Node) scalar(path string) (*Node, bool) {
	v, ok := n.Get(path)
	if !ok || v.multi() {
		return nil, false
	}
	return v, true
}

// String returns a string value. Only a string node yields one; no other kind
// is rendered. A string holding raw binary yields "" and true, matching the
// engine.
func (n *Node) String(path string) (string, bool) {
	v, ok := n.scalar(path)
	if !ok {
		return "", false
	}
	return core.ToStringSafe(v.obj)
}

// Int returns an integer value. A float or a time truncates toward zero, as
// the C does.
func (n *Node) Int(path string) (int64, bool) {
	v, ok := n.scalar(path)
	if !ok {
		return 0, false
	}
	return core.ToIntSafe(v.obj)
}

// Float returns a floating point value. An integer or a time converts.
func (n *Node) Float(path string) (float64, bool) {
	v, ok := n.scalar(path)
	if !ok {
		return 0, false
	}
	return core.ToDoubleSafe(v.obj)
}

// Bool returns a boolean value. Only a boolean node yields one.
func (n *Node) Bool(path string) (bool, bool) {
	v, ok := n.scalar(path)
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
	v, ok := n.scalar(path)
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
