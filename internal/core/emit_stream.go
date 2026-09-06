package core

import (
	"bufio"
	"io"
)

// This file ports src/ucl_emitter_streamline.c: a driver that produces output
// incrementally without ever holding the whole tree, reusing the very same
// rendering code as emit.go rather than being a separate renderer.

// emitStreamFrame is struct ucl_emitter_streamline_stack: one open container.
type emitStreamFrame struct {
	isArray bool
	// empty stays true until the frame's first child is written; it is what
	// gives add value calls their correct per-container "first" flag.
	empty bool
	// obj is the container passed to StartContainer. EndContainer needs it
	// again because the closing logic consults the object's identity and type.
	obj *Object
}

// EmitStream is ucl_emitter_context_streamline: an emitter context plus a
// stack of open containers.
//
// It renders a document incrementally. Containers are opened and closed by the
// caller; complete values, including whole prebuilt subtrees, are handed to
// AddObject and rendered through the ordinary recursive walk.
type EmitStream struct {
	ctx        *emitCtx
	bw         *bufio.Writer
	containers []emitStreamFrame
}

// EmitStreamNew starts a streaming emission to w, mirroring
// ucl_object_emit_streamline_new.
//
// Note what this does eagerly: obj becomes the root container and is opened
// immediately, which means any children it already carries are walked and
// written before this returns. Only what the caller adds afterwards is
// actually streamed. As in the C, an obj that is neither an object nor an
// array (nil included) opens nothing at all, and the returned stream is still
// usable.
func EmitStreamNew(w io.Writer, obj *Object, format Emitter) (*EmitStream, error) {
	if err := emitCheckFormat(format); err != nil {
		return nil, err
	}
	bw := bufio.NewWriter(w)
	s := &EmitStream{ctx: emitNewCtx(bw, obj, format, nil), bw: bw}
	s.StartContainer(obj)
	return s, nil
}

// StartContainer opens obj as the innermost container, mirroring
// ucl_object_emit_streamline_start_container. It reports false, having written
// nothing, if obj is not an object or an array.
func (s *EmitStream) StartContainer(obj *Object) bool {
	if obj == nil || (obj.ObjType != TypeObject && obj.ObjType != TypeArray) {
		return false // API misuse, exactly as the C treats it
	}

	if s.ctx.top == nil {
		s.ctx.top = obj
	}

	// The container prints its own key unless it is a bare array element or
	// the very first container opened.
	var printKey bool
	if top, ok := s.top(); ok {
		printKey = !top.isArray
	}

	// Faithful to the C: "first" here means "the stack was empty", not "this
	// is the first child of its parent". The streaming entry point keeps no
	// per-parent bookkeeping, unlike AddObject below.
	first := len(s.containers) == 0

	frame := emitStreamFrame{empty: true, obj: obj, isArray: obj.ObjType == TypeArray}
	if frame.isArray {
		s.ctx.startArray(obj, first, printKey)
	} else {
		s.ctx.startObject(obj, first, printKey)
	}
	s.containers = append(s.containers, frame)
	return true
}

// AddObject writes obj as the next child of the innermost open container,
// mirroring ucl_object_emit_streamline_add_object.
//
// obj may be any complete value, a whole nested subtree included; it is
// rendered by the ordinary recursive walk, not incrementally. Unlike
// StartContainer, the first-child and key-printing flags here are tracked per
// container and are always correct.
func (s *EmitStream) AddObject(obj *Object) {
	if obj == nil {
		return
	}
	var isArray, isFirst bool
	if top, ok := s.top(); ok {
		isArray = top.isArray
		if top.empty {
			isFirst = true
			top.empty = false
		}
	}
	s.ctx.writeElt(obj, isFirst, !isArray)
}

// EndContainer closes the innermost open container, mirroring
// ucl_object_emit_streamline_end_container. It is a no-op if none is open.
func (s *EmitStream) EndContainer() {
	top, ok := s.top()
	if !ok {
		return
	}
	if top.isArray {
		s.ctx.endArray(top.obj)
	} else {
		s.ctx.endObject(top.obj)
	}
	s.containers = s.containers[:len(s.containers)-1]
}

// Finish closes any containers the caller left open and flushes the output,
// mirroring ucl_object_emit_streamline_finish. The stream must not be used
// afterwards.
func (s *EmitStream) Finish() error {
	for len(s.containers) > 0 {
		s.EndContainer()
	}
	if s.ctx.w.err != nil {
		return s.ctx.w.err
	}
	return s.bw.Flush()
}

// top returns the innermost open container's frame.
func (s *EmitStream) top() (*emitStreamFrame, bool) {
	if len(s.containers) == 0 {
		return nil, false
	}
	return &s.containers[len(s.containers)-1], true
}
