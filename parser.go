package goucl

// The UCL parser: chunk management, the lexical layer, the state machine and
// duplicate-key resolution. This file ports src/ucl_parser.c; the macro
// handlers and variable expansion live in parser_macro.go, which ports the
// corresponding parts of src/ucl_util.c.
//
// The port is deliberately literal: every function carries the name of the C
// function it mirrors, and the upstream quirks documented in docs/parser.md
// and docs/findings.md are reproduced rather than fixed. Where the C reads out
// of bounds (backwards scans without a lower bound, p[-1] at the start of a
// buffer) the Go bounds the read; on every input the C does not already
// corrupt, the two agree.

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// --- Character classes (ucl_chartable.h) ----------------------------------

// Character class bits, mirroring the UCL_CHARACTER_* enum. The values are
// the bit positions of the compiled ucl_chartable, so charTable entries can be
// compared against the header if the C is ever consulted again.
const (
	chDenied uint16 = 1 << iota
	chKey
	chKeyStart
	chWhitespace
	chWhitespaceUnsafe
	chValueEnd
	chValueStr
	chValueDigit
	chValueDigitStart
	chEscape
	chKeySep
	chJSONUnsafe
	chUCLUnsafe
)

// charTable is ucl_chartable, built from the class listing in
// docs/parser.md section 3.1 rather than spelled out as 256 literals.
var charTable = func() (t [256]uint16) {
	set := func(s string, mask uint16) {
		for i := 0; i < len(s); i++ {
			t[s[i]] |= mask
		}
	}
	setRange := func(lo, hi int, mask uint16) {
		for c := lo; c <= hi; c++ {
			t[c] |= mask
		}
	}

	t[0x00] = chValueEnd | chJSONUnsafe | chUCLUnsafe
	setRange(0x01, 0x07, chDenied)
	t[0x08] = chJSONUnsafe | chUCLUnsafe
	t['\t'] = chWhitespace | chWhitespaceUnsafe | chKeySep | chJSONUnsafe | chUCLUnsafe
	t['\n'] = chWhitespaceUnsafe | chValueEnd | chJSONUnsafe | chUCLUnsafe
	t['\v'] = chWhitespaceUnsafe
	t['\f'] = chWhitespaceUnsafe | chJSONUnsafe | chUCLUnsafe
	t['\r'] = chWhitespaceUnsafe | chValueEnd | chJSONUnsafe | chUCLUnsafe
	setRange(0x0e, 0x1f, chDenied)
	t[' '] = chWhitespace | chWhitespaceUnsafe | chValueStr | chKeySep | chUCLUnsafe
	set("!", chValueStr)
	t['"'] = chValueStr | chEscape | chJSONUnsafe | chUCLUnsafe
	t['#'] = chValueEnd
	set("$%&'()*", chValueStr)
	t['+'] = chValueStr | chValueDigit | chUCLUnsafe
	t[','] = chValueEnd
	t['-'] = chKey | chValueStr | chValueDigit | chValueDigitStart
	t['.'] = chKey | chValueStr | chValueDigit
	t['/'] = chKey | chKeyStart | chValueStr | chEscape
	setRange('0', '9', chKey|chKeyStart|chValueStr|chValueDigit|chValueDigitStart)
	t[':'] = chValueStr | chKeySep | chUCLUnsafe
	t[';'] = chValueEnd
	t['<'] = chValueStr
	t['='] = chValueStr | chKeySep | chUCLUnsafe
	set(">?@", chValueStr)
	setRange('A', 'Z', chKey|chKeyStart|chValueStr|chValueDigit)
	setRange('a', 'z', chKey|chKeyStart|chValueStr|chValueDigit)
	set("bfnrtu", chEscape)
	t['['] = chValueStr | chUCLUnsafe
	t['\\'] = chValueStr | chEscape | chJSONUnsafe | chUCLUnsafe
	t[']'] = chValueEnd
	set("^", chValueStr)
	t['_'] = chKey | chKeyStart | chValueStr
	set("`", chValueStr)
	t['{'] = chValueStr | chUCLUnsafe
	set("|", chValueStr)
	t['}'] = chValueEnd
	set("~", chValueStr)
	t[0x7f] = chDenied
	setRange(0x80, 0xff, chKey|chKeyStart|chValueStr)

	return t
}()

// testChar is ucl_test_character.
func testChar(c byte, mask uint16) bool { return charTable[c]&mask != 0 }

// isCommentStart is ucl_lex_is_comment. Note that "//" is not a comment and
// that the second byte is ignored for '#'; every call site guards it with
// remain >= 2, so a '#' as the last byte of a chunk is not a comment start.
func isCommentStart(c1, c2 byte) bool { return (c1 == '/' && c2 == '*') || c1 == '#' }

// --- Parser states (enum ucl_parser_state) --------------------------------

type parserState uint8

const (
	stateInit parserState = iota
	stateObjectUnused
	stateArrayUnused
	stateKey
	stateKeyOBrace
	stateValue
	stateAfterValue
	stateArrayValueUnused
	stateSCommentUnused
	stateMCommentUnused
	stateMacroName
	stateMacro
	stateError
)

// Stack frame flags, mirroring enum ucl_stack_flags.
const (
	stackHasOBrace uint16 = 1 << 0 // opened by an explicit '{' or '['
	stackAutomatic uint16 = 1 << 1 // frame belongs to an inserted or included chunk
)

// maxRecursion is UCL_MAX_RECURSION: the nested-chunk (include) limit.
const maxRecursion = 16

// --- Error codes (enum ucl_error) -----------------------------------------

// ErrorCode mirrors C ucl_error_t. The numeric values are part of the
// compatibility contract.
type ErrorCode int

const (
	ErrOK       ErrorCode = iota // no error
	ErrSyntax                    // syntax error
	ErrIO                        // I/O error
	ErrState                     // invalid parser state
	ErrNested                    // nesting limit exceeded
	ErrUnpaired                  // unpaired brace
	ErrMacro                     // macro error
	ErrInternal                  // internal error
	ErrSSL                       // signature/SSL error
	ErrMerge                     // merge conflict
)

// --- Chunks (struct ucl_chunk) --------------------------------------------

// chunk is one buffer being parsed. Chunks form a stack: an include pushes a
// new one and pops it when the included content is exhausted.
//
// The C carries three redundant cursors (a local pointer, chunk->pos and
// chunk->remain); they collapse here into the single index pos.
type chunk struct {
	data      []byte
	pos       int
	fname     string
	line      int // 1-based
	column    int // 0-based, counted in bytes
	priority  uint
	strategy  DuplicateStrategy
	parseType ParseType
}

func (c *chunk) atEnd() bool   { return c.pos >= len(c.data) }
func (c *chunk) remain() int   { return len(c.data) - c.pos }
func (c *chunk) cur() byte     { return c.data[c.pos] }
func (c *chunk) end() int      { return len(c.data) }
func (c *chunk) at(i int) byte { return c.data[i] }

// skip is ucl_chunk_skipc: advance one byte, maintaining line and column.
// A newline bumps the line and zeroes the column; every other byte, '\r' and
// tabs included, bumps the column by one.
func (c *chunk) skip() {
	if c.pos >= len(c.data) {
		return
	}
	if c.data[c.pos] == '\n' {
		c.line++
		c.column = 0
	} else {
		c.column++
	}
	c.pos++
}

// commentStarts reports whether a comment begins at the cursor, applying the
// remain >= 2 guard every C call site uses.
func (c *chunk) commentStarts() bool {
	return c.remain() >= 2 && isCommentStart(c.data[c.pos], c.data[c.pos+1])
}

// fnameOrMemory is the `chunk->fname ? chunk->fname : "memory"` idiom used by
// the messages that bypass ucl_set_err.
func (c *chunk) fnameOrMemory() string {
	if c == nil || c.fname == "" {
		return "memory"
	}
	return c.fname
}

// --- Stack frames (struct ucl_stack) --------------------------------------

type stackFrame struct {
	obj   *Object
	level int
	flags uint16
	line  int
	chunk *chunk // identity comparison only
}

// --- Parser ---------------------------------------------------------------

// MacroHandler is a user macro implementation, mirroring ucl_macro_handler.
// args is nil when the macro was written without parentheses. Returning false
// aborts the current chunk.
type MacroHandler func(data []byte, args *Object) bool

// ContextMacroHandler is a context macro, mirroring
// ucl_context_macro_handler. ctx is the parser's top object.
type ContextMacroHandler func(data []byte, args, ctx *Object) bool

// VariableHandler resolves a ${...} variable the parser does not know,
// mirroring ucl_variable_handler. It is only consulted for the braced form.
//
// Note the asymmetry documented in docs/parser.md section 8.2: the measuring
// pass passes exactly the variable name, while the expansion pass passes the
// rest of the string starting at the name, so a handler must locate the
// closing brace itself if it cares.
type VariableHandler func(data []byte) (value []byte, ok bool)

type macroDef struct {
	name           string
	handler        MacroHandler
	contextHandler ContextMacroHandler
	isContext      bool
}

type variable struct {
	name  string
	value string
}

// Parser is a UCL parser, mirroring struct ucl_parser.
type Parser struct {
	state, prevState parserState
	recursion        int
	flags            ParserFlag
	defaultPriority  uint

	errCode ErrorCode
	errStr  string
	hasErr  bool

	topObj, curObj *Object
	trashObjs      []*Object
	includePaths   []string
	curFile        string

	// macros is kept in registration order: HASH_FIND returns the first
	// entry inserted under a name, so re-registering a built-in does not
	// override it.
	macros []*macroDef

	stack  []*stackFrame // top is the last element
	chunks []*chunk      // top is the last element

	variables  []*variable // registration order; the lookup order for $VAR
	varHandler VariableHandler

	comments    EmitComments
	lastComment *Object

	includeTrace IncludeTraceFunc
}

// NewParser returns a parser, mirroring ucl_parser_new.
//
// It registers the six built-in macros (include, try_include, includes,
// priority, load, inherit), allocates the comment map when ParserSaveComments
// is set, and pre-defines FILENAME and CURDIR unless ParserNoFilevars is set.
func NewParser(flags ParserFlag) *Parser {
	p := &Parser{flags: flags}

	p.registerMacro("include", func(data []byte, args *Object) bool {
		return p.includeCommon(data, args, false, false)
	})
	p.registerMacro("try_include", func(data []byte, args *Object) bool {
		return p.includeCommon(data, args, true, false)
	})
	p.registerMacro("includes", func(data []byte, args *Object) bool {
		return p.includeCommon(data, args, false, true)
	})
	p.registerMacro("priority", p.priorityHandler)
	p.registerMacro("load", p.loadHandler)
	p.registerContextMacro("inherit", p.inheritHandler)

	if flags&ParserSaveComments != 0 {
		p.comments = make(EmitComments)
	}
	if flags&ParserNoFilevars == 0 {
		p.SetFilevars("", false)
	}
	return p
}

// Free releases the parser, mirroring ucl_parser_free. The GC does the work
// here; the method exists so ported C code reads the same.
func (p *Parser) Free() {}

func (p *Parser) curChunk() *chunk {
	if len(p.chunks) == 0 {
		return nil
	}
	return p.chunks[len(p.chunks)-1]
}

func (p *Parser) stackTop() *stackFrame {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

func (p *Parser) stackNext() *stackFrame {
	if len(p.stack) < 2 {
		return nil
	}
	return p.stack[len(p.stack)-2]
}

func (p *Parser) popStack() {
	if len(p.stack) > 0 {
		p.stack = p.stack[:len(p.stack)-1]
	}
}

func (p *Parser) popChunk() {
	if len(p.chunks) > 0 {
		p.chunks = p.chunks[:len(p.chunks)-1]
	}
}

// --- Errors ---------------------------------------------------------------

// fileName is the `parser->cur_file ? parser->cur_file : "<unknown>"` idiom
// used by ucl_set_err.
func (p *Parser) fileName() string {
	if p.curFile == "" {
		return "<unknown>"
	}
	return p.curFile
}

// createErr is ucl_create_err: the first error wins and later ones are
// dropped. It does not touch the parser state.
func (p *Parser) createErr(format string, args ...any) {
	if p.hasErr {
		return
	}
	p.errStr = fmt.Sprintf(format, args...)
	p.hasErr = true
}

// appendErr is the utstring_printf used for the unmatched-brace report, which
// concatenates one message per unbalanced frame.
func (p *Parser) appendErr(format string, args ...any) {
	p.errStr += fmt.Sprintf(format, args...)
	p.hasErr = true
}

// setErr is ucl_set_err: it wraps msg in the "error while parsing" template,
// records the code and forces the error state.
func (p *Parser) setErr(code ErrorCode, msg string) bool {
	ch := p.curChunk()
	switch {
	case ch != nil && ch.pos < len(ch.data):
		c := ch.data[ch.pos]
		// isgraph() in the C locale: printable and not a space.
		if c >= 0x21 && c <= 0x7e {
			p.createErr("error while parsing %s: line: %d, column: %d - '%s', character: '%c'",
				p.fileName(), ch.line, ch.column, msg, c)
		} else {
			p.createErr("error while parsing %s: line: %d, column: %d - '%s', character: '0x%02x'",
				p.fileName(), ch.line, ch.column, msg, c)
		}
	default:
		p.createErr("error while parsing %s: at the end of chunk: %s", p.fileName(), msg)
	}
	p.errCode = code
	p.state = stateError
	return false
}

// GetError returns the parser's error, or nil, mirroring
// ucl_parser_get_error. The message text matches the C byte for byte.
func (p *Parser) GetError() error {
	if p == nil || !p.hasErr {
		return nil
	}
	return errors.New(p.errStr)
}

// ErrorString returns the raw error text, or "" if there is none.
func (p *Parser) ErrorString() string {
	if p == nil {
		return ""
	}
	return p.errStr
}

// ErrorCode returns the code of the last error, mirroring
// ucl_parser_get_error_code.
func (p *Parser) ErrorCode() ErrorCode {
	if p == nil {
		return ErrOK
	}
	return p.errCode
}

// ClearError discards the recorded error, mirroring ucl_parser_clear_error.
// It does not reset the parser state.
func (p *Parser) ClearError() {
	if p == nil {
		return
	}
	p.errStr = ""
	p.hasErr = false
	p.errCode = ErrOK
}

// Column returns the current column, mirroring ucl_parser_get_column.
func (p *Parser) Column() int {
	if ch := p.curChunk(); ch != nil {
		return ch.column
	}
	return 0
}

// LineNum returns the current line, mirroring ucl_parser_get_linenum.
func (p *Parser) LineNum() int {
	if ch := p.curChunk(); ch != nil {
		return ch.line
	}
	return 0
}

// ChunkPeek returns the byte under the cursor, or 0, mirroring
// ucl_parser_chunk_peek.
func (p *Parser) ChunkPeek() byte {
	ch := p.curChunk()
	if ch == nil || ch.atEnd() {
		return 0
	}
	return ch.cur()
}

// ChunkSkip advances the cursor one byte, mirroring ucl_parser_chunk_skip.
func (p *Parser) ChunkSkip() bool {
	ch := p.curChunk()
	if ch == nil || ch.atEnd() {
		return false
	}
	ch.skip()
	return !ch.atEnd()
}

// --- Configuration --------------------------------------------------------

// SetDefaultPriority sets the priority used by the AddX calls that do not take
// one, mirroring ucl_parser_set_default_priority.
func (p *Parser) SetDefaultPriority(prio uint) bool {
	if p == nil {
		return false
	}
	p.defaultPriority = prio
	return true
}

// DefaultPriority returns the default priority, or -1 for a nil parser,
// mirroring ucl_parser_get_default_priority.
func (p *Parser) DefaultPriority() int {
	if p == nil {
		return -1
	}
	return int(p.defaultPriority)
}

// SetVariablesHandler installs the fallback used for unresolved ${VAR},
// mirroring ucl_parser_set_variables_handler.
func (p *Parser) SetVariablesHandler(h VariableHandler) {
	if p != nil {
		p.varHandler = h
	}
}

// SetIncludePath replaces the search path used by relative includes,
// mirroring ucl_set_include_path. paths must be a TypeArray of strings.
func (p *Parser) SetIncludePath(paths *Object) bool {
	if p == nil || paths == nil || paths.ObjType != TypeArray {
		return false
	}
	var list []string
	var it Iter
	for cur := Iterate(paths, &it, true); cur != nil; cur = Iterate(paths, &it, true) {
		if cur.ObjType == TypeString {
			list = append(list, string(cur.sv))
		}
	}
	p.includePaths = list
	return true
}

// CurFile returns the file currently being parsed, mirroring
// ucl_parser_get_cur_file.
func (p *Parser) CurFile() string { return p.curFile }

// GetObject returns the parsed document, or nil if the parse failed,
// mirroring ucl_parser_get_object.
func (p *Parser) GetObject() *Object {
	if p == nil {
		return nil
	}
	if p.state != stateError && p.topObj != nil {
		return Ref(p.topObj)
	}
	return nil
}

// GetComments returns the comment map for the emitters, mirroring
// ucl_parser_get_comments. It is nil unless ParserSaveComments was set.
func (p *Parser) GetComments() EmitComments {
	if p == nil {
		return nil
	}
	return p.comments
}

// GetCurrentStackObject returns the object depth frames below the top of the
// container stack, mirroring ucl_parser_get_current_stack_object. Every frame
// on the way must hold a TypeObject.
func (p *Parser) GetCurrentStackObject(depth uint) *Object {
	if p == nil || len(p.stack) == 0 {
		return nil
	}
	idx := len(p.stack) - 1
	for i := uint(0); ; i++ {
		if idx < 0 {
			return nil
		}
		fr := p.stack[idx]
		if fr.obj == nil || fr.obj.ObjType != TypeObject {
			return nil
		}
		if i == depth {
			return Ref(fr.obj)
		}
		idx--
	}
}

// PubkeyAdd mirrors ucl_parser_pubkey_add. libucl built without OpenSSL
// rejects every key with this message, and so does this port.
func (p *Parser) PubkeyAdd(key []byte) bool {
	p.createErr("cannot check signatures without openssl")
	return false
}

// --- Chunk entry points ---------------------------------------------------

// AddChunkFull adds data to the parser, mirroring ucl_parser_add_chunk_full.
//
// The chunk is left on the chunk stack, as in the C: only include handlers and
// InsertChunk pop what they pushed. data must therefore stay alive, which in
// Go it does by virtue of the slice header.
func (p *Parser) AddChunkFull(data []byte, priority uint, strat DuplicateStrategy, pt ParseType) bool {
	if p == nil {
		return false
	}
	// The C's "data == NULL && len != 0" guard ("invalid chunk added") has no
	// Go analogue: a nil slice always has length zero.

	if p.state != stateError {
		if pt == ParseAuto && len(data) > 0 {
			switch {
			case data[0]&0x80 == 0x80:
				pt = ParseMsgPack
			case data[0] == '(':
				pt = ParseCSexp
			default:
				pt = ParseUCL
			}
		}

		ch := &chunk{
			data:      data,
			line:      1,
			column:    0,
			priority:  priority,
			strategy:  strat,
			parseType: pt,
		}
		if p.curFile != "" {
			ch.fname = p.curFile
		}
		p.chunks = append(p.chunks, ch)
		p.recursion++

		if p.recursion > maxRecursion {
			// The chunk deliberately stays pushed, as in the C.
			p.createErr("maximum include nesting limit is reached: %d", p.recursion)
			return false
		}

		if len(data) > 0 {
			switch pt {
			case ParseMsgPack:
				return p.addMsgPackChunk(data, priority)
			case ParseCSexp:
				return p.addCSexpChunk(data, priority)
			default:
				return p.stateMachine()
			}
		}

		// An empty chunk still produces a document, which is what makes
		// ".include()" with empty parens yield an empty arguments object.
		if p.topObj == nil {
			p.topObj = NewFull(TypeObject, priority)
		}
		return true
	}

	p.createErr("a parser is in an invalid state")
	return false
}

// addMsgPackChunk stands in for ucl_parse_msgpack.
//
// The C's msgpack reader drives the same container stack as the UCL state
// machine; the Go decoder in msgpack_decode.go is standalone and returns a
// finished tree instead. The two agree for the only shape that matters -- a
// msgpack chunk is always a complete document -- so the result becomes the top
// object, or is merged into it when an earlier chunk already produced one.
func (p *Parser) addMsgPackChunk(data []byte, priority uint) bool {
	obj, err := DecodeMsgPackFull(data, priority)
	if err != nil {
		// Strip the Go error wrapper so the recorded text is the message
		// ucl_create_err would have stored.
		msg := err.Error()
		if _, rest, found := strings.Cut(msg, ErrMsgPack.Error()+": "); found {
			msg = rest
		}
		p.createErr("%s", msg)
		p.errCode = ErrSyntax
		p.state = stateError
		return false
	}
	if p.topObj == nil {
		p.topObj = obj
		p.curObj = obj
		return true
	}
	return p.topObj.Merge(obj, false)
}

// AddChunk adds data at the default priority, mirroring ucl_parser_add_chunk.
func (p *Parser) AddChunk(data []byte) bool {
	if p == nil {
		return false
	}
	return p.AddChunkFull(data, p.defaultPriority, DuplicateAppend, ParseUCL)
}

// AddChunkPriority mirrors ucl_parser_add_chunk_priority.
func (p *Parser) AddChunkPriority(data []byte, priority uint) bool {
	if p == nil {
		return false
	}
	return p.AddChunkFull(data, priority, DuplicateAppend, ParseUCL)
}

// AddString parses s at the default priority, mirroring
// ucl_parser_add_string.
func (p *Parser) AddString(s string) bool {
	if p == nil {
		return false
	}
	return p.AddStringPriority(s, p.defaultPriority)
}

// AddStringPriority mirrors ucl_parser_add_string_priority.
func (p *Parser) AddStringPriority(s string, priority uint) bool {
	if p == nil {
		return false
	}
	return p.AddChunkPriority([]byte(s), priority)
}

// InsertChunk splices data into an in-progress parse, mirroring
// ucl_parser_insert_chunk. It is what a user macro uses to feed generated
// configuration back to the parser.
func (p *Parser) InsertChunk(data []byte) bool {
	if p == nil || p.topObj == nil {
		return false
	}
	saved := p.state
	p.state = stateInit

	// Stop the inserted chunk from closing the container we are inside.
	if top, next := p.stackTop(), p.stackNext(); top != nil && next != nil {
		top.level = next.level
	}

	cur := p.curChunk()
	if cur == nil {
		// The C dereferences parser->chunks here; a top-level chunk is never
		// popped, so this is unreachable through the normal flow.
		p.state = saved
		return false
	}
	res := p.AddChunkFull(data, cur.priority, cur.strategy, cur.parseType)

	if len(p.chunks) > 0 {
		p.popChunk()
		p.recursion--
	}
	p.state = saved
	return res
}

// AddFile parses the named file at the default priority, mirroring
// ucl_parser_add_file.
func (p *Parser) AddFile(filename string) bool {
	if p == nil {
		return false
	}
	return p.AddFileFull(filename, p.defaultPriority, DuplicateAppend, ParseUCL)
}

// AddFilePriority mirrors ucl_parser_add_file_priority.
func (p *Parser) AddFilePriority(filename string, priority uint) bool {
	if p == nil {
		return false
	}
	return p.AddFileFull(filename, priority, DuplicateAppend, ParseUCL)
}

// AddFileFull mirrors ucl_parser_add_file_full. It resolves the name, sets
// FILENAME and CURDIR from it, and parses the contents.
func (p *Parser) AddFileFull(filename string, priority uint, strat DuplicateStrategy, pt ParseType) bool {
	if p == nil {
		return false
	}
	real, err := realpath(filename)
	if err != nil {
		p.createErr("cannot open file %s: %s", filename, cErrno(err))
		return false
	}
	buf, ok := p.fetchFile(real, true)
	if !ok {
		return false
	}
	p.SetFilevars(real, false)
	return p.AddChunkFull(buf, priority, strat, pt)
}

// AddFd parses everything readable from fd at the default priority,
// mirroring ucl_parser_add_fd.
func (p *Parser) AddFd(fd uintptr) bool {
	if p == nil {
		return false
	}
	return p.AddFdFull(fd, p.defaultPriority, DuplicateAppend, ParseUCL)
}

// AddFdPriority mirrors ucl_parser_add_fd_priority.
func (p *Parser) AddFdPriority(fd uintptr, priority uint) bool {
	if p == nil {
		return false
	}
	return p.AddFdFull(fd, priority, DuplicateAppend, ParseUCL)
}

// AddFdFull mirrors ucl_parser_add_fd_full. Unlike AddFileFull it clears
// cur_file, so errors report "<unknown>" and relative includes resolve against
// the process working directory.
func (p *Parser) AddFdFull(fd uintptr, priority uint, strat DuplicateStrategy, pt ParseType) bool {
	if p == nil {
		return false
	}
	f := os.NewFile(fd, "<fd>")
	if f == nil {
		p.createErr("cannot stat fd %d: %s", fd, "Bad file descriptor")
		return false
	}
	st, err := f.Stat()
	if err != nil {
		p.createErr("cannot stat fd %d: %s", fd, cErrno(err))
		return false
	}
	if st.Size() == 0 {
		return true
	}
	buf := make([]byte, st.Size())
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		p.createErr("cannot mmap fd %d: %s", fd, cErrno(err))
		return false
	}
	p.curFile = ""
	return p.AddChunkFull(buf[:n], priority, strat, pt)
}

// --- The state machine ----------------------------------------------------

// stateMachine is ucl_state_machine: the driver loop over the current chunk.
func (p *Parser) stateMachine() bool {
	ch := p.curChunk()
	// macroNameStart is the C's loop-local `c`; it survives across iterations
	// because UCL_STATE_MACRO_NAME consumes the name one byte per dispatch.
	macroNameStart := -1
	var macro *macroDef

	// nextKey and endOfObject are declared once in the C, and ucl_parse_key
	// leaves them alone on several paths -- notably nextKey, which is only
	// assigned when the key had no '='/':' separator. They therefore carry
	// over between keys, and "a b { }" followed by "c = 1" nests the latter
	// rather than treating it as a value. Only gotContent is reset per key.
	nextKey, endOfObject := false, false

	if p.topObj == nil {
		p.state = stateInit
	}

	for !ch.atEnd() {
		switch p.state {
		case stateInit:
			if !p.doInit(ch) {
				return false
			}

		case stateKeyOBrace, stateKey:
			if p.state == stateKeyOBrace {
				// The container on top of the stack was opened by a '{' that
				// belongs to an inserted chunk: its matching '}' clears these
				// flags instead of popping the frame.
				if top := p.stackTop(); top != nil {
					top.flags |= stackHasOBrace | stackAutomatic
				}
			}

			for !ch.atEnd() && testChar(ch.cur(), chWhitespaceUnsafe) {
				ch.skip()
			}
			if ch.atEnd() || ch.cur() == '}' {
				p.state = stateAfterValue
				continue
			}
			if len(p.stack) == 0 {
				p.setErr(ErrSyntax, "top object is finished but the parser expects a key")
				p.prevState = p.state
				p.state = stateError
				return false
			}

			gotContent := false
			if !p.parseKey(ch, &nextKey, &endOfObject, &gotContent) {
				p.prevState = p.state
				p.state = stateError
				return false
			}
			if endOfObject {
				p.state = stateAfterValue
				continue
			}
			if p.state != stateMacroName {
				top := p.stackTop()
				switch {
				case nextKey && top.obj.ObjType == TypeObject:
					// "key1 key2 { ... }" nests an extra container.
					if p.addContainer(p.curObj, false, top.level+1, p.state == stateKeyOBrace) == nil {
						return false
					}
				case gotContent:
					p.state = stateValue
				}
			} else {
				macroNameStart = ch.pos
			}

		case stateValue:
			if p.curObj == nil || !p.parseValue(ch) {
				p.prevState = p.state
				p.state = stateError
				return false
			}

		case stateAfterValue:
			if !p.parseAfterValue(ch) {
				p.prevState = p.state
				p.state = stateError
				return false
			}
			if len(p.stack) != 0 {
				if p.stackTop().obj.ObjType == TypeObject {
					p.state = stateKey
				} else {
					p.state = stateValue
				}
			} else {
				// Everything after the top container is silently ignored.
				return true
			}

		case stateMacroName:
			if p.flags&ParserDisableMacro != 0 {
				if !p.skipMacroAsComment(ch) {
					p.createErr("error at %s:%d at column %d: invalid macro",
						ch.fnameOrMemory(), ch.line, ch.column)
					p.state = stateError
					return false
				}
				p.state = p.prevState
				break
			}
			if !testChar(ch.cur(), chWhitespaceUnsafe) && ch.cur() != '(' {
				ch.skip()
				break
			}
			if macroNameStart < 0 || ch.pos-macroNameStart <= 0 {
				p.createErr("error at %s:%d at column %d: invalid macro name",
					ch.fnameOrMemory(), ch.line, ch.column)
				p.state = stateError
				return false
			}
			name := ch.data[macroNameStart:ch.pos]
			macro = p.findMacro(name)
			if macro == nil {
				p.createErr("error at %s:%d at column %d: unknown macro: '%s', character: '%c'",
					ch.fnameOrMemory(), ch.line, ch.column, name, ch.cur())
				p.state = stateError
				return false
			}
			if !p.skipSpacesComments(ch) {
				return false
			}
			p.state = stateMacro

		case stateMacro:
			var macroArgs *Object
			if !ch.atEnd() && ch.cur() == '(' {
				macroArgs = p.parseMacroArguments(ch)
				if macroArgs != nil {
					if !p.skipSpacesComments(ch) {
						return false
					}
				}
			}
			body, ok := p.parseMacroValue(ch)
			if !ok {
				p.prevState = p.state
				p.state = stateError
				return false
			}
			if expanded, changed := p.expandVariable(body); changed {
				body = expanded
			}
			// Restored before the handler runs, so a handler that pushes a
			// nested chunk resumes in the right state.
			p.state = p.prevState

			var ret bool
			switch {
			case macro == nil:
				ret = false
				p.setErr(ErrInternal, "internal error: parser has macro undefined")
			case macro.isContext:
				ret = macro.contextHandler(body, macroArgs, p.topObj)
			default:
				ret = macro.handler(body, macroArgs)
			}

			// The handler may have pushed or popped chunks.
			ch = p.curChunk()
			if !ret {
				return false
			}

		case stateError:
			return false

		default:
			p.setErr(ErrInternal, "internal error: parser is in an unknown state")
			p.state = stateError
			return false
		}
	}

	return p.epilogue(ch)
}

// doInit is UCL_STATE_INIT: skip comments and whitespace, then decide whether
// the chunk opens with an array, an object or a bare key.
func (p *Parser) doInit(ch *chunk) bool {
	if !p.skipComments(ch) {
		p.prevState = p.state
		p.state = stateError
		return false
	}

	seenOBrace := false
	for !ch.atEnd() && testChar(ch.cur(), chWhitespaceUnsafe) {
		ch.skip()
	}

	if !ch.atEnd() {
		switch ch.cur() {
		case '[':
			p.state = stateValue
			ch.skip()
			seenOBrace = true
		case '{':
			ch.skip()
			p.state = stateKeyOBrace
			seenOBrace = true
		default:
			p.state = stateKey
		}
	}

	if p.topObj == nil {
		// addContainer overwrites the state, so a bare '{' that creates the
		// root goes straight to KEY; an included braced file keeps KEY_OBRACE.
		obj := p.addContainer(nil, p.state == stateValue, 0, seenOBrace)
		if obj == nil {
			return false
		}
		p.topObj = obj
		p.curObj = obj
	}
	return true
}

// epilogue runs when the chunk is exhausted: it flushes a pending comment and
// reports any brace this chunk left open.
func (p *Parser) epilogue(ch *chunk) bool {
	if p.lastComment != nil {
		switch {
		case p.curObj != nil:
			p.attachComment(p.curObj, true)
		case p.stackTop() != nil && p.stackTop().obj != nil:
			p.attachComment(p.stackTop().obj, true)
		case p.topObj != nil:
			p.attachComment(p.topObj, true)
		default:
			p.lastComment = nil
		}
	}

	if len(p.stack) != 0 && p.state != stateError {
		hasError := false
		for i := len(p.stack) - 1; i >= 0; i-- {
			st := p.stack[i]
			if st.chunk != ch {
				break // frames from an outer chunk are not our problem
			}
			if st.flags&stackHasOBrace != 0 {
				p.appendErr("%s:%d unmatched open brace at %d; ",
					ch.fnameOrMemory(), ch.line, st.line)
				hasError = true
			}
		}
		if hasError {
			p.errCode = ErrUnpaired
			return false
		}
	}
	return true
}

// skipSpacesComments is the SKIP_SPACES_COMMENTS macro.
func (p *Parser) skipSpacesComments(ch *chunk) bool {
	for !ch.atEnd() {
		if !testChar(ch.cur(), chWhitespaceUnsafe) {
			if ch.commentStarts() {
				if !p.skipComments(ch) {
					return false
				}
			}
			break
		}
		ch.skip()
	}
	return true
}

// --- Comments -------------------------------------------------------------

// skipComments is ucl_skip_comments. It loops so consecutive comments are all
// consumed in one call, handles nested block comments, and records the text
// when ParserSaveComments is set.
func (p *Parser) skipComments(ch *chunk) bool {
	beg := -1

start:
	beg = -1
	if ch.remain() > 0 && ch.cur() == '#' {
		beg = ch.pos
		for !ch.atEnd() {
			if ch.cur() == '\n' {
				if p.flags&ParserSaveComments != 0 {
					// The newline itself is not part of the saved text.
					p.saveComment(ch.data[beg:ch.pos])
					beg = -1
				}
				ch.skip()
				goto start
			}
			ch.skip()
		}
	} else if ch.remain() >= 2 && ch.cur() == '/' && ch.at(ch.pos+1) == '*' {
		beg = ch.pos
		nested := 1
		quoted := false
		ch.skip()
		ch.skip()
		for !ch.atEnd() {
			switch {
			// The C reads p[-1] unconditionally; at the start of the buffer it
			// reads the '/' of "/*", which is never a backslash, so bounding
			// the read is equivalent.
			case ch.cur() == '"' && (ch.pos == 0 || ch.at(ch.pos-1) != '\\'):
				quoted = !quoted
				ch.skip()
			case quoted:
				ch.skip()
			case ch.remain() >= 2 && ch.cur() == '*' && ch.at(ch.pos+1) == '/':
				ch.skip()
				ch.skip()
				nested--
				if nested == 0 {
					if p.flags&ParserSaveComments != 0 {
						// The C saves p - beg + 1 bytes, i.e. one byte past
						// the closing '/'. When the comment ends the chunk
						// that byte is outside the buffer; both mmap'd files
						// and the malloc'd read buffers libucl is fed in
						// practice have a zero there, and the goldens record
						// the resulting NUL, so it is reproduced explicitly
						// rather than left to chance.
						if ch.pos+1 <= len(ch.data) {
							p.saveComment(ch.data[beg : ch.pos+1])
						} else {
							p.saveComment(append(append([]byte(nil), ch.data[beg:]...), 0))
						}
						beg = -1
					}
					goto start
				}
			case ch.remain() >= 2 && ch.cur() == '/' && ch.at(ch.pos+1) == '*':
				nested++
				ch.skip()
				ch.skip()
			default:
				ch.skip()
			}
		}
		if nested != 0 {
			return p.setErr(ErrNested, "unfinished multiline comment")
		}
	}

	if beg >= 0 && ch.pos > beg && p.flags&ParserSaveComments != 0 {
		p.saveComment(ch.data[beg:ch.pos])
	}
	return true
}

// saveComment is ucl_save_comment: consecutive comments accumulate onto one
// implicit-array chain, which is attached to an object as a unit.
func (p *Parser) saveComment(text []byte) {
	if len(text) == 0 {
		return
	}
	nobj := FromStringCommon(text, StringRaw)
	if p.lastComment != nil {
		p.lastComment = dlAppend(p.lastComment, nobj)
	} else {
		p.lastComment = nobj
	}
}

// attachComment is ucl_attach_comment. FlagInherited on the comment chain head
// means "this comment came before the object"; the flag lives on the comment,
// not on the commented object, so it does not interact with the duplicate
// priority logic.
func (p *Parser) attachComment(obj *Object, before bool) {
	if p.lastComment == nil {
		return
	}
	if p.comments != nil {
		if existing := p.comments[obj]; existing != nil {
			p.comments[obj] = dlConcat(existing, p.lastComment)
		} else {
			p.comments[obj] = p.lastComment
		}
	}
	if before {
		p.lastComment.flags |= FlagInherited
	}
	p.lastComment = nil
}

// --- Keys -----------------------------------------------------------------

// parseKey is ucl_parse_key. It reads one key, resolves it against the current
// container (which is where duplicate handling happens) and leaves the cursor
// at the start of the value.
func (p *Parser) parseKey(ch *chunk, nextKey, endOfObject, gotContent *bool) bool {
	if ch.atEnd() {
		p.setErr(ErrSyntax, "unexpected end of input while parsing key")
		*endOfObject = true
		// Deliberately true: the driver goes to AFTER_VALUE and the already
		// set error state ends the parse on the next dispatch.
		return true
	}

	if ch.cur() == '.' && p.flags&ParserDisableMacro == 0 {
		ch.skip()
		p.prevState = p.state
		p.state = stateMacroName
		*endOfObject = false
		return true
	}

	c, end := -1, -1
	gotQuote, gotEq, gotSemicolon := false, false, false
	needUnescape, uclEscape, varExpand := false, false, false

	for !ch.atEnd() {
		if c < 0 {
			switch {
			case ch.commentStarts():
				if !p.skipComments(ch) {
					return false
				}
			case testChar(ch.cur(), chWhitespaceUnsafe):
				ch.skip()
			case testChar(ch.cur(), chKeyStart):
				c = ch.pos
				ch.skip()
				*gotContent = true
			case ch.cur() == '"':
				c = ch.pos + 1
				gotQuote = true
				*gotContent = true
				ch.skip()
			case ch.cur() == '}':
				*endOfObject = true
				return true
			case ch.cur() == '.' && p.flags&ParserDisableMacro == 0:
				ch.skip()
				p.prevState = p.state
				p.state = stateMacroName
				return true
			default:
				return p.setErr(ErrSyntax, "key must begin with a letter")
			}
			continue
		}

		if !gotQuote {
			switch {
			case testChar(ch.cur(), chKey):
				*gotContent = true
				ch.skip()
			case testChar(ch.cur(), chKeySep):
				end = ch.pos
			default:
				return p.setErr(ErrSyntax, "invalid character in a key")
			}
			if end >= 0 {
				break
			}
			continue
		}

		if !p.lexJSONString(ch, &needUnescape, &uclEscape, &varExpand) {
			return false
		}
		end = ch.pos - 1
		break
	}

	if ch.atEnd() && *gotContent {
		return p.setErr(ErrSyntax, "unfinished key")
	}
	if !*gotContent {
		// Nothing was read: the chunk held only comments and whitespace.
		return true
	}
	*endOfObject = false

	for !ch.atEnd() {
		switch {
		case testChar(ch.cur(), chWhitespace):
			ch.skip()
		case ch.cur() == '=':
			if gotEq || gotSemicolon {
				return p.setErr(ErrSyntax, "unexpected '=' character")
			}
			ch.skip()
			gotEq = true
		case ch.cur() == ':':
			if gotEq || gotSemicolon {
				return p.setErr(ErrSyntax, "unexpected ':' character")
			}
			ch.skip()
			gotSemicolon = true
		case ch.commentStarts():
			if !p.skipComments(ch) {
				return false
			}
		default:
			goto separatorDone
		}
	}

separatorDone:
	// The C tests a pointer that is always non-NULL here, so this is simply
	// "error at the end of the chunk".
	if ch.atEnd() {
		return p.setErr(ErrSyntax, "unfinished key")
	}

	if !gotEq && !gotSemicolon {
		// Decide between "key1 key2 { }" (nested) and "key1 value" (linear).
		t := ch.pos
		*nextKey = false
		for t < ch.end() && testChar(ch.at(t), chWhitespace) {
			t++
		}
		if t >= ch.end() || (ch.at(t) != '{' && ch.at(t) != '[') {
			for t < ch.end() {
				b := ch.at(t)
				if b == ',' || b == ';' || b == '\n' || b == '\r' {
					break
				}
				if b == '{' || b == '[' {
					*nextKey = true
					break
				}
				t++
			}
		}
	}

	nobj := NewFull(TypeNull, p.curChunk().priority)
	key := p.copyOrStore(ch.data[c:end], needUnescape, p.flags&ParserKeyLowercase != 0, false, false)
	if len(key) == 0 {
		return p.setErr(ErrSyntax, "empty keys are not allowed")
	}
	nobj.key = key

	if !p.processObjectElement(nobj) {
		return false
	}
	if uclEscape {
		nobj.flags |= FlagNeedKeyEscape
	}
	return true
}

// --- Duplicate handling ---------------------------------------------------

// processObjectElement is ucl_parser_process_object_element: it resolves a
// freshly parsed key against the container, before the value is known, which
// is what lets DuplicateMerge hand the existing object to the value parser.
func (p *Parser) processObjectElement(nobj *Object) bool {
	top := p.stackTop().obj
	if top.ov == nil {
		top.ov = newHash(p.flags&ParserKeyLowercase != 0)
	}
	container := top.ov

	// Walk the container's own implicit-array chain, but only accept a hit in
	// the same hash: two sibling "section_a { }" blocks stay separate.
	var tobj *Object
	for cur := top; cur != nil; cur = cur.Next {
		if cur.ObjType != TypeObject {
			continue
		}
		if found := cur.ov.Lookup(nobj.key); found != nil {
			if cur.ov != container {
				continue
			}
			tobj = found
			break
		}
	}

	if tobj == nil {
		container.Insert(nobj.key, nobj)
		nobj.Prev = nobj
		nobj.Next = nil
		top.length++
	} else {
		priold := tobj.Priority()
		prinew := nobj.Priority()

		switch p.curChunk().strategy {
		case DuplicateAppend:
			// An inherited value always loses, whatever the real priorities.
			if tobj.flags&FlagInherited != 0 {
				prinew = priold + 1
			}
			switch {
			case priold == prinew:
				p.appendElt(container, tobj, nobj)
			case priold > prinew:
				p.trashObjs = append(p.trashObjs, nobj)
			default:
				container.Replace(tobj.key, nobj)
				Unref(tobj)
			}

		case DuplicateRewrite:
			container.Replace(tobj.key, nobj)
			Unref(tobj)

		case DuplicateError:
			return p.setErr(ErrMerge, fmt.Sprintf("duplicate element for key '%s' found", nobj.key))

		case DuplicateMerge:
			switch {
			case tobj.ObjType == TypeObject || tobj.ObjType == TypeArray:
				// The point of the strategy: the value parser writes into the
				// container that is already there.
				Unref(nobj)
				nobj = tobj
			case priold == prinew:
				p.appendElt(container, tobj, nobj)
			case priold > prinew:
				p.trashObjs = append(p.trashObjs, nobj)
			default:
				container.Replace(tobj.key, nobj)
				Unref(tobj)
			}
		}
	}

	top.ov = container
	p.curObj = nobj
	p.attachComment(nobj, false)
	return true
}

// appendElt is ucl_parser_append_elt: the implicit-array chain by default, a
// real TypeArray under ParserNoImplicitArrays.
func (p *Parser) appendElt(cont *hash, top, elt *Object) {
	if p.flags&ParserNoImplicitArrays == 0 {
		top.flags |= FlagMultivalue
		dlAppend(top, elt)
		p.stackTop().obj.length++
		return
	}

	if top.flags&FlagMultivalue != 0 {
		ArrayAppend(top, elt)
		return
	}

	// The wrapper aliases the original key rather than copying it, and the
	// container's length is deliberately not incremented here -- an asymmetry
	// with the implicit path that upstream has and this port keeps.
	nobj := TypedNew(TypeArray)
	nobj.key = top.key
	nobj.flags |= FlagMultivalue
	ArrayAppend(nobj, top)
	ArrayAppend(nobj, elt)
	cont.Replace(top.key, nobj)
}

// --- Containers -----------------------------------------------------------

// addContainer is ucl_parser_add_container: create or reuse a container, push
// a stack frame for it and set the state the container implies.
//
// A non-nil obj is reused, which is how DuplicateMerge merges an object into
// an existing one -- and also how a scalar is silently converted into a
// container.
func (p *Parser) addContainer(obj *Object, isArray bool, level int, hasOBrace bool) *Object {
	var nobj *Object
	if obj == nil {
		t := TypeObject
		if isArray {
			t = TypeArray
		}
		nobj = NewFull(t, p.curChunk().priority)
	} else {
		opposite := TypeArray
		if isArray {
			opposite = TypeObject
		}
		if obj.ObjType == opposite {
			p.setErr(ErrMerge, "cannot merge an object with an array")
			return nil
		}
		nobj = obj
		if isArray {
			nobj.ObjType = TypeArray
		} else {
			nobj.ObjType = TypeObject
		}
	}

	if !isArray {
		if nobj.ov == nil {
			nobj.ov = newHash(p.flags&ParserKeyLowercase != 0)
		}
		p.state = stateKey
	} else {
		p.state = stateValue
	}

	if level >= 65535 {
		p.setErr(ErrNested, "objects are nesting too deep (over 65535 limit)")
		return nil
	}

	st := &stackFrame{obj: nobj, level: level, line: p.curChunk().line, chunk: p.curChunk()}
	if hasOBrace {
		st.flags = stackHasOBrace
	}
	p.stack = append(p.stack, st)
	p.curObj = nobj
	return nobj
}

// getContainer is ucl_parser_get_container: inside an array a fresh element is
// allocated and appended, otherwise the object the key parser created is
// returned.
func (p *Parser) getContainer() *Object {
	top := p.stackTop()
	if top == nil || top.obj == nil {
		return nil
	}
	if top.obj.ObjType == TypeArray {
		obj := NewFull(TypeNull, p.curChunk().priority)
		if !ArrayAppend(top.obj, obj) {
			return nil
		}
		p.curObj = obj
		p.attachComment(obj, false)
		return obj
	}
	return p.curObj
}

// --- Strings --------------------------------------------------------------

// lexJSONString is ucl_lex_json_string, entered with the cursor after the
// opening quote. It consumes the closing quote.
func (p *Parser) lexJSONString(ch *chunk, needUnescape, uclEscape, varExpand *bool) bool {
	for !ch.atEnd() {
		c := ch.cur()
		switch {
		case c < 0x1F:
			// Strictly less than 0x1F, so 0x1F itself is allowed.
			if c == '\n' {
				return p.setErr(ErrSyntax, "unexpected newline")
			}
			return p.setErr(ErrSyntax, "unexpected control character")

		case c == '\\':
			ch.skip()
			if ch.atEnd() {
				return p.setErr(ErrSyntax, "unfinished escape character")
			}
			if testChar(ch.cur(), chEscape) {
				if ch.cur() == 'u' {
					ch.skip()
					for i := 0; i < 4 && !ch.atEnd(); i++ {
						if !isXDigit(ch.cur()) {
							return p.setErr(ErrSyntax, "invalid utf escape")
						}
						ch.skip()
					}
					if ch.atEnd() {
						return p.setErr(ErrSyntax, "unfinished escape character")
					}
				} else {
					ch.skip()
				}
			}
			// A character outside the escape class is not consumed here: it is
			// re-examined as an ordinary character, so "\q" lexes fine.
			*needUnescape = true
			*uclEscape = true
			continue

		case c == '"':
			ch.skip()
			return true

		case testChar(c, chUCLUnsafe):
			*uclEscape = true

		case c == '$':
			*varExpand = true
		}
		ch.skip()
	}
	return p.setErr(ErrSyntax, "no quote at the end of json string")
}

// lexSquotedString is ucl_lex_squoted_string. A single-quoted string has only
// two rules and is never variable-expanded.
func (p *Parser) lexSquotedString(ch *chunk, needUnescape *bool) bool {
	for !ch.atEnd() {
		switch ch.cur() {
		case '\\':
			ch.skip()
			if ch.atEnd() {
				return p.setErr(ErrSyntax, "unfinished escape character")
			}
			ch.skip()
			*needUnescape = true
			continue
		case '\'':
			ch.skip()
			return true
		}
		ch.skip()
	}
	return p.setErr(ErrSyntax, "no quote at the end of single quoted string")
}

// parseStringValue is ucl_parse_string_value: scan an unquoted atom.
//
// The three brace counters mean that '}' and ']' end an atom only when they do
// not close a brace opened inside the same atom, so "a{b}c" is one string
// while "value }" ends at the brace.
func (p *Parser) parseStringValue(ch *chunk, varExpand, needUnescape *bool) bool {
	var figureOpen, figureClose, squareOpen, squareClose int

	for !ch.atEnd() {
		switch ch.cur() {
		case '{':
			figureOpen++
		case '}':
			figureClose++
			if figureClose <= figureOpen {
				ch.skip()
				continue
			}
		case '[':
			squareOpen++
		case ']':
			squareClose++
			if squareClose <= squareOpen {
				ch.skip()
				continue
			}
		case '$':
			*varExpand = true
		case '\\':
			*needUnescape = true
			ch.skip()
			if !ch.atEnd() {
				ch.skip()
			}
			continue
		}

		if testChar(ch.cur(), chValueEnd) || ch.commentStarts() {
			return true
		}
		ch.skip()
	}
	return true
}

// parseMultilineString is ucl_parse_multiline_string. It returns the content
// start index and length, where the length includes the newline preceding the
// terminator; the caller drops that byte.
func (p *Parser) parseMultilineString(ch *chunk, term []byte, varExpand *bool) (int, int) {
	c := ch.pos
	newline := false
	termLen := len(term)

	for !ch.atEnd() {
		if newline {
			if ch.remain() < termLen {
				return c, 0
			}
			if string(ch.data[ch.pos:ch.pos+termLen]) == string(term) {
				tend := ch.pos + termLen
				if tend < ch.end() && ch.at(tend) != '\n' && ch.at(tend) != ';' && ch.at(tend) != ',' {
					// Not a real terminator; keep scanning.
					ch.skip()
					continue
				}
				length := ch.pos - c
				// The C patches the counters by hand here: the column is set
				// to the terminator length rather than added to.
				ch.pos = tend
				ch.column = termLen
				return c, length
			}
		}
		if ch.cur() == '\n' {
			newline = true
		} else {
			if ch.cur() == '$' {
				*varExpand = true
			}
			newline = false
		}
		ch.skip()
	}
	return c, 0
}

// --- Values ---------------------------------------------------------------

// parseValue is ucl_parse_value. It sets the next state itself.
func (p *Parser) parseValue(ch *chunk) bool {
	needUnescape, uclEscape, varExpand := false, false, false

	// DELIBERATE DIVERGENCE from libucl -- see docs/findings.md item 3.
	//
	// The C skips whitespace and then comments exactly once here. A comment
	// that ends in a newline therefore leaves the cursor on the next line's
	// indentation, and the value lexer starts the atom there: inside an array,
	//
	//	arr [
	//	  # c
	//	  1,
	//	]
	//
	// parses in libucl as the string "  1", and the same shape with a block
	// comment is a hard "string value must not be empty" error. (Elsewhere the
	// bug is masked, because ucl_parse_key's separator loop already alternates
	// between whitespace and comments.) That is silent data corruption with no
	// plausible caller depending on it, so this port loops instead and treats
	// a leading comment as transparent. TestParserArrayLeadingComment pins
	// both the C's behaviour and ours.
	for !ch.atEnd() {
		if testChar(ch.cur(), chWhitespaceUnsafe) {
			ch.skip()
			continue
		}
		before := ch.pos
		if !p.skipComments(ch) {
			return false
		}
		if ch.pos == before {
			break
		}
	}

	for !ch.atEnd() {
		c := ch.pos
		var obj *Object

		switch ch.cur() {
		case '"':
			ch.skip()
			if !p.lexJSONString(ch, &needUnescape, &uclEscape, &varExpand) {
				return false
			}
			obj = p.getContainer()
			if obj == nil {
				return false
			}
			obj.ObjType = TypeString
			obj.sv = p.copyOrStore(ch.data[c+1:ch.pos-1], needUnescape, false, varExpand, false)
			obj.length = uint32(len(obj.sv))
			p.state = stateAfterValue
			return true

		case '\'':
			ch.skip()
			if !p.lexSquotedString(ch, &needUnescape) {
				return false
			}
			obj = p.getContainer()
			if obj == nil {
				return false
			}
			obj.ObjType = TypeString
			obj.flags |= FlagSquoted
			obj.sv = p.copyOrStore(ch.data[c+1:ch.pos-1], needUnescape, false, varExpand, true)
			obj.length = uint32(len(obj.sv))
			p.state = stateAfterValue
			return true

		case '{':
			obj = p.getContainer()
			if obj == nil {
				p.state = stateError
				return p.setErr(ErrSyntax, "object value must be a part of an object")
			}
			if len(p.stack) == 0 {
				return false
			}
			if p.addContainer(obj, false, p.stackTop().level, true) == nil {
				return false
			}
			ch.skip()
			return true

		case '[':
			obj = p.getContainer()
			if obj == nil {
				p.state = stateError
				return p.setErr(ErrSyntax, "array value must be a part of an object")
			}
			if len(p.stack) == 0 {
				return false
			}
			if p.addContainer(obj, true, p.stackTop().level, true) == nil {
				return false
			}
			ch.skip()
			return true

		case ']':
			if top := p.stackTop(); top != nil && top.obj.ObjType == TypeArray {
				// "[]" and a trailing comma both land here: the ']' is left
				// for AFTER_VALUE.
				p.state = stateAfterValue
				return true
			}

		case '<':
			obj = p.getContainer()
			if obj == nil {
				p.state = stateError
				return p.setErr(ErrSyntax, "multiline value must be a part of an object")
			}
			if ch.end()-c > 3 && ch.at(c) == '<' && ch.at(c+1) == '<' {
				t := c + 2
				for t < ch.end() && ch.at(t) >= 'A' && ch.at(t) <= 'Z' {
					t++
				}
				if t == ch.end() {
					return p.setErr(ErrSyntax, "unterminated multiline value")
				}
				if ch.at(t) == '\n' {
					term := ch.data[c+2 : t]
					ch.pos = t + 1
					ch.column = 0
					ch.line++
					start, length := p.parseMultilineString(ch, term, &varExpand)
					if length == 0 {
						return p.setErr(ErrSyntax, "unterminated multiline value")
					}
					obj.ObjType = TypeString
					obj.flags |= FlagMultiline
					obj.sv = p.copyOrStore(ch.data[start:start+length-1], false, false, varExpand, false)
					obj.length = uint32(len(obj.sv))
					p.state = stateAfterValue
					return true
				}
			}
		}

		// parse_string: everything that fell through above.
		if obj == nil {
			obj = p.getContainer()
		}
		if obj == nil {
			p.state = stateError
			return p.setErr(ErrSyntax, "value must be a part of an object")
		}

		if testChar(ch.cur(), chValueDigitStart) {
			if p.lexNumber(ch, obj) {
				p.state = stateAfterValue
				return true
			}
			if p.state == stateError {
				return false
			}
			// A malformed number falls back to being a string.
		}

		if !p.parseStringValue(ch, &varExpand, &needUnescape) {
			return false
		}

		// Trailing tabs and spaces are cut; newlines already stopped the scan.
		stripped := 0
		for ch.pos-1-stripped >= c && testChar(ch.at(ch.pos-1-stripped), chWhitespace) {
			stripped++
		}
		strLen := ch.pos - c - stripped
		atom := ch.data[c : c+maxInt(strLen, 0)]

		switch {
		case strLen <= 0:
			return p.setErr(ErrSyntax, "string value must not be empty")
		case strLen == 4 && string(atom) == "null":
			obj.length = 0
			obj.clearValue()
			obj.ObjType = TypeNull
		case strLen == 3 && string(atom) == "nan":
			obj.length = 0
			obj.setFloat(nan())
		case strLen == 3 && string(atom) == "inf":
			obj.length = 0
			obj.setFloat(inf())
		case maybeParseBoolean(obj, atom):
			// obj is now a boolean.
		default:
			obj.ObjType = TypeString
			obj.sv = p.copyOrStore(atom, needUnescape, false, varExpand, false)
			obj.length = uint32(len(obj.sv))
		}

		p.state = stateAfterValue
		return true
	}
	return true
}

// lexNumber is ucl_lex_number: it delegates to maybeParseNumber and patches
// the chunk counters by hand, which is why a number can never span a line.
func (p *Parser) lexNumber(ch *chunk, obj *Object) bool {
	n, err := maybeParseNumber(obj, ch.data[ch.pos:], true, false, p.flags&ParserNoTime == 0)
	if err == nil {
		ch.column += n
		ch.pos += n
		return true
	}
	if errors.Is(err, errNumberRange) {
		p.setErr(ErrSyntax, "numeric value out of range")
	}
	return false
}

// parseAfterValue is ucl_parse_after_value: consume separators and closing
// braces, and reject two values written without anything between them.
func (p *Parser) parseAfterValue(ch *chunk) bool {
	gotSep := false

	for !ch.atEnd() {
		switch {
		case testChar(ch.cur(), chWhitespace):
			ch.skip()

		case ch.commentStarts():
			if !p.skipComments(ch) {
				return false
			}
			// A comment counts as a separator.
			gotSep = true

		case testChar(ch.cur(), chValueEnd):
			b := ch.cur()
			if b != '}' && b != ']' {
				gotSep = true
				ch.skip()
				continue
			}

			if len(p.stack) == 0 {
				return p.setErr(ErrSyntax, "end of array or object detected without corresponding start")
			}
			st := p.stackTop()
			matches := (b == '}' && st.obj.ObjType == TypeObject) ||
				(b == ']' && st.obj.ObjType == TypeArray)
			if !matches {
				return p.setErr(ErrSyntax, "unexpected terminating symbol detected")
			}

			if st.flags&stackHasOBrace == 0 {
				p.errCode = ErrUnpaired
				// The message says '}' even when the byte was ']'.
				p.createErr("%s:%d object closed with } is not opened with { at line %d",
					ch.fnameOrMemory(), p.curChunk().line, st.line)
				return false
			}

			if st.flags&stackAutomatic != 0 {
				// The frame belongs to an inserted chunk: keep it.
				st.flags = 0
			} else {
				p.popStack()
			}

			if p.curObj != nil {
				p.attachComment(p.curObj, true)
			}

			// Collapse the frames created by the "key1 key2 {}" shorthand,
			// stopping when two adjacent frames share a level.
			for len(p.stack) != 0 {
				st = p.stackTop()
				next := p.stackNext()
				if next == nil || next.level == st.level {
					break
				}
				p.popStack()
				p.curObj = st.obj
			}

			if len(p.stack) == 0 {
				// Ignore everything after the top object.
				return true
			}
			ch.skip()
			gotSep = true

		default:
			if !gotSep {
				return p.setErr(ErrSyntax, "delimiter is missing")
			}
			return true
		}
	}
	return true
}

// --- String storage -------------------------------------------------------

// copyOrStore is ucl_copy_or_store_ptr, the funnel every key and string value
// passes through.
//
// The zero-copy branch returns a sub-slice of the chunk, which is safe in Go
// because the slice keeps the backing array alive; the C's NUL-termination
// caveat has no analogue.
func (p *Parser) copyOrStore(src []byte, needUnescape, needLowercase, needExpand, unescapeSquote bool) []byte {
	if !needUnescape && !needLowercase && !(needExpand && len(p.variables) > 0) &&
		p.flags&ParserZeroCopy != 0 {
		return src
	}

	var dst []byte
	if needLowercase {
		dst = strlcpyToLower(src)
	} else {
		dst = append([]byte(nil), src...)
	}
	if needUnescape {
		if unescapeSquote {
			dst = unescapeSquotedString(dst)
		} else {
			dst = unescapeJSONString(dst)
		}
	}
	if needExpand {
		if expanded, changed := p.expandVariable(dst); changed {
			dst = expanded
		}
	}
	return dst
}

// strlcpyToLower is ucl_strlcpy_tolower: an ASCII-only fold that, unlike the
// default copy path, stops at the first embedded NUL.
func strlcpyToLower(src []byte) []byte {
	out := make([]byte, 0, len(src))
	for _, c := range src {
		lc := asciiLower(c)
		out = append(out, lc)
		if lc == 0 {
			break
		}
	}
	return out
}

// unescapeJSONString is ucl_unescape_json_string.
//
// Surrogate pairs are not decoded: a 😀 pair produces two three-byte
// CESU-8 sequences rather than one emoji, exactly as upstream.
func unescapeJSONString(s []byte) []byte {
	if len(s) <= 1 {
		return s
	}
	out := make([]byte, 0, len(s))
	h := 0
	remaining := len(s)

	for remaining > 0 {
		if s[h] == '\\' {
			h++
			if remaining == 1 {
				// A trailing lone backslash is kept (issue #74).
				remaining--
				out = append(out, '\\')
				continue
			}
			switch s[h] {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 'b':
				out = append(out, '\b')
			case 't':
				out = append(out, '\t')
			case 'f':
				out = append(out, '\f')
			case '\\':
				out = append(out, '\\')
			case '"':
				out = append(out, '"')
			case 'u':
				h++
				remaining--
				// remaining counts down independently of h and does not
				// decrement for the backslash, so it can exceed the bytes
				// actually left. The C reads past the end here on a
				// truncated escape such as "\u000"; bound it instead.
				if remaining > 3 && h+4 <= len(s) {
					uval := 0
				digits:
					for i := 0; i < 4; i++ {
						uval <<= 4
						switch c := s[h+i]; {
						case isDigit(c):
							uval += int(c - '0')
						case c >= 'a' && c <= 'f':
							uval += int(c-'a') + 10
						case c >= 'A' && c <= 'F':
							uval += int(c-'A') + 10
						default:
							// The accumulated value is still used and four
							// source bytes are still consumed.
							break digits
						}
					}
					switch {
					case uval < 0x80:
						out = append(out, byte(uval))
					case uval < 0x800:
						out = append(out, byte(0xC0+((uval&0x7C0)>>6)), byte(0x80+(uval&0x3F)))
					case uval < 0x10000:
						out = append(out, byte(0xE0+((uval&0xF000)>>12)),
							byte(0x80+((uval&0x0FC0)>>6)), byte(0x80+(uval&0x3F)))
					default:
						out = append(out, '?')
					}
					h += 4
					remaining -= 4
					if remaining > 0 {
						remaining--
					}
					continue
				}
				out = append(out, 'u')
			default:
				// The backslash is dropped: "\/" is "/", "\q" is "q".
				out = append(out, s[h])
			}
			h++
			remaining--
		} else {
			out = append(out, s[h])
			h++
		}
		if remaining > 0 {
			remaining--
		}
	}
	return out
}

// unescapeSquotedString is ucl_unescape_squoted_string: only \' and a
// backslash-newline line continuation are special, and every other escape
// keeps its backslash.
func unescapeSquotedString(s []byte) []byte {
	if len(s) <= 1 {
		return s
	}
	out := make([]byte, 0, len(s))
	h := 0
	remaining := len(s)

	for remaining > 0 {
		if s[h] == '\\' {
			h++
			if remaining == 1 {
				remaining--
				out = append(out, '\\')
				continue
			}
			switch s[h] {
			case '\'':
				out = append(out, '\'')
			case '\n':
				// Line continuation: emit nothing.
			case '\r':
				if remaining > 1 && h+1 < len(s) && s[h+1] == '\n' {
					h++
					remaining--
				}
			default:
				out = append(out, '\\', s[h])
			}
			h++
			remaining--
		} else {
			out = append(out, s[h])
			h++
		}
		if remaining > 0 {
			remaining--
		}
	}
	return out
}

// --- Small helpers --------------------------------------------------------

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// cErrno renders a Go file error the way C's strerror does: the same text with
// the first letter capitalised.
func cErrno(err error) string {
	if err == nil {
		return ""
	}
	var msg string
	var pe *os.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		msg = pe.Err.Error()
	case errors.As(err, &le):
		msg = le.Err.Error()
	default:
		msg = err.Error()
	}
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

// addCSexpChunk feeds a chunk to the canonical S-expression decoder,
// mirroring the UCL_PARSE_CSEXP arm of ucl_parser_add_chunk_full.
//
// Like msgpack, csexp is decoded whole rather than driven through the UCL
// state machine, so this is the same shape as addMsgPackChunk.
func (p *Parser) addCSexpChunk(data []byte, priority uint) bool {
	obj, err := DecodeCSexpFull(data, priority)
	if err != nil {
		// Strip the Go error wrapper so the recorded text is the message
		// ucl_create_err would have stored.
		msg := err.Error()
		if _, rest, found := strings.Cut(msg, ErrCSexp.Error()+": "); found {
			msg = rest
		}
		p.createErr("%s", msg)
		p.errCode = ErrSyntax
		p.state = stateError
		return false
	}
	if p.topObj == nil {
		p.topObj = obj
		p.curObj = obj
		return true
	}
	return p.topObj.Merge(obj, false)
}
