package core

// Doubly-linked list helpers for the implicit-array chain, mirroring the
// utlist.h macros libucl uses (DL_APPEND, DL_CONCAT, DL_DELETE,
// DL_REPLACE_ELEM).
//
// The chain is modelled as raw pointers rather than a Go slice because the C
// API exposes it directly -- ucl_elt_append, ucl_object_iterate with
// expandValues false, and the parser's splice all operate at pointer level,
// and ported tests exercise them.
//
// Invariants, matching utlist.h exactly:
//
//	head.Prev == tail   (the head's Prev is the tail, not nil)
//	tail.Next == nil    (the chain is nil-terminated forwards)
//	a lone element points its Prev at itself
//
// Note the asymmetry: this is not a circular list. Prev walks backwards only
// from the tail to the head; the head's Prev jumps to the tail.

// dlAppend appends add to the chain headed by head and returns the new head.
//
// add is expected to be a single element; its own chain is not spliced. Use
// dlConcat for that.
func dlAppend(head, add *Object) *Object {
	if add == nil {
		return head
	}
	if head == nil {
		add.Prev = add
		add.Next = nil
		return add
	}
	add.Prev = head.Prev
	head.Prev.Next = add
	head.Prev = add
	add.Next = nil
	return head
}

// dlConcat splices the chain headed by second onto the end of the chain headed
// by first and returns the combined head.
func dlConcat(first, second *Object) *Object {
	if second == nil {
		return first
	}
	if first == nil {
		return second
	}
	tail := second.Prev
	second.Prev = first.Prev
	first.Prev.Next = second
	first.Prev = tail
	return first
}

// dlDelete removes del from the chain headed by head and returns the new head.
//
// The removed element's own Next and Prev are left as they are, matching
// DL_DELETE: the C never clears them, and callers such as the array pop
// operations observe that a popped chain head still refers to its siblings.
func dlDelete(head, del *Object) *Object {
	if head == nil || del == nil {
		return head
	}
	if del.Prev == del {
		// Sole element of the chain.
		return nil
	}
	if del == head {
		newHead := del.Next
		if newHead != nil {
			newHead.Prev = del.Prev
		}
		return newHead
	}
	del.Prev.Next = del.Next
	if del.Next != nil {
		del.Next.Prev = del.Prev
	} else {
		// Deleting the tail: the head's Prev must follow it back.
		head.Prev = del.Prev
	}
	return head
}

// dlReplaceElem swaps old for add in the chain headed by head, keeping the
// element's position, and returns the new head.
func dlReplaceElem(head, old, add *Object) *Object {
	if head == nil || old == nil || add == nil {
		return head
	}
	add.Next = old.Next
	add.Prev = old.Prev
	if old.Next != nil {
		old.Next.Prev = add
	}
	if old == head {
		if old.Next == nil {
			// Sole element: it is its own Prev.
			add.Prev = add
		}
		return add
	}
	old.Prev.Next = add
	if old.Next == nil {
		// Replacing the tail.
		head.Prev = add
	}
	return head
}

// dlLen counts the elements of a chain by walking it.
func dlLen(head *Object) int {
	n := 0
	for e := head; e != nil; e = e.Next {
		n++
	}
	return n
}
