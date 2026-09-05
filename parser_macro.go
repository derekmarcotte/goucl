package goucl

// Macros, includes and variable expansion: the parts of the parser that reach
// outside the current chunk. Ports the macro half of src/ucl_parser.c
// (ucl_parse_macro_value, ucl_parse_macro_arguments, ucl_skip_macro_as_comment,
// ucl_expand_variable and friends) plus the built-in macro handlers and
// ucl_parser_set_filevars from src/ucl_util.c.

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }

// --- Macro registration ---------------------------------------------------

// RegisterMacro registers a plain macro, mirroring
// ucl_parser_register_macro.
//
// As in the C, registering a name twice adds a second entry and lookup returns
// the first, so a user cannot shadow a built-in by re-registering its name.
func (p *Parser) RegisterMacro(name string, h MacroHandler) bool {
	if p == nil || name == "" || h == nil {
		return false
	}
	return p.registerMacro(name, h)
}

func (p *Parser) registerMacro(name string, h MacroHandler) bool {
	p.macros = append(p.macros, &macroDef{name: name, handler: h})
	return true
}

// RegisterContextMacro registers a macro that also receives the top object,
// mirroring ucl_parser_register_context_macro.
func (p *Parser) RegisterContextMacro(name string, h ContextMacroHandler) bool {
	if p == nil || name == "" || h == nil {
		return false
	}
	return p.registerContextMacro(name, h)
}

func (p *Parser) registerContextMacro(name string, h ContextMacroHandler) bool {
	p.macros = append(p.macros, &macroDef{name: name, contextHandler: h, isContext: true})
	return true
}

// findMacro is the HASH_FIND on parser->macroes: an exact, case-sensitive byte
// match.
//
// Registering a name twice adds a second entry, and uthash inserts at the head
// of the bucket chain, so the *last* registration is what HASH_FIND returns.
// That is how a user macro named "include" shadows the built-in; verified
// against the C rather than inferred. Hence the backwards walk.
func (p *Parser) findMacro(name []byte) *macroDef {
	for i := len(p.macros) - 1; i >= 0; i-- {
		if p.macros[i].name == string(name) {
			return p.macros[i]
		}
	}
	return nil
}

// --- Macro syntax ---------------------------------------------------------

// parseMacroValue is ucl_parse_macro_value. A quoted body is handed to the
// handler raw -- escapes are not decoded, only variables are expanded.
func (p *Parser) parseMacroValue(ch *chunk) ([]byte, bool) {
	if ch.atEnd() {
		p.setErr(ErrSyntax, "unexpected end of input while parsing macro value")
		return nil, false
	}

	var body []byte

	switch ch.cur() {
	case '"':
		c := ch.pos
		ch.skip()
		var needUnescape, uclEscape, varExpand bool
		if !p.lexJSONString(ch, &needUnescape, &uclEscape, &varExpand) {
			return nil, false
		}
		body = ch.data[c+1 : ch.pos-1]

	case '{':
		ch.skip()
		for !ch.atEnd() && testChar(ch.cur(), chWhitespaceUnsafe) {
			ch.skip()
		}
		c := ch.pos
		for !ch.atEnd() && ch.cur() != '}' {
			ch.skip()
		}
		if ch.atEnd() {
			p.setErr(ErrSyntax, "unterminated macro value, missing '}'")
			return nil, false
		}
		body = ch.data[c:ch.pos]
		ch.skip()

	default:
		c := ch.pos
		for !ch.atEnd() && !testChar(ch.cur(), chValueEnd) {
			ch.skip()
		}
		body = ch.data[c:ch.pos]
	}

	// Trailing whitespace and semicolons belong to the macro.
	for !ch.atEnd() && (testChar(ch.cur(), chWhitespaceUnsafe) || ch.cur() == ';') {
		ch.skip()
	}
	return body, true
}

// parseMacroArguments is ucl_parse_macro_arguments: the parenthesised part is
// balanced, extracted and parsed as an independent UCL fragment.
//
// The sub-parser inherits the flags but nothing else, so ${VAR} inside macro
// arguments is not expanded.
//
// The C saves the chunk position so it can roll back on "remain == 0", but the
// scan loop already requires at least one byte, so that branch is unreachable
// and is not reproduced here. Running off the end of the chunk returns nil
// with the cursor left where it stopped, exactly as upstream.
func (p *Parser) parseMacroArguments(ch *chunk) *Object {
	if ch.remain() < 2 || ch.cur() != '(' {
		return nil
	}

	ch.skip()
	c := ch.pos
	obraces, ebraces, state := 1, 0, 0
	argsLen := 0

	for !ch.atEnd() {
		switch state {
		case 0:
			switch ch.cur() {
			case '(':
				obraces++
			case ')':
				ebraces++
			case '"':
				state = 1
			}
			if obraces == ebraces {
				// The balancing test runs before the increment, so the
				// closing paren is consumed but not counted.
				state = 99
			} else {
				argsLen++
			}
			ch.skip()
		case 1:
			// The C reads p[-1]; the byte before is always inside the chunk
			// here because state 1 is only entered after consuming a quote.
			if ch.cur() == '"' && (ch.pos == 0 || ch.at(ch.pos-1) != '\\') {
				state = 0
			}
			argsLen++
			ch.skip()
		}

		if state == 99 {
			args := ch.data[c : c+argsLen]
			sub := NewParser(p.flags)
			var res *Object
			if !sub.AddChunk(args) {
				p.setErr(ErrSyntax, "macro arguments parsing error")
			} else {
				res = sub.GetObject()
			}
			return res
		}
	}
	return nil
}

// skipMacroAsComment is ucl_skip_macro_as_comment: swallow a macro line as if
// it were a comment. It is only reachable with ParserDisableMacro set, and in
// practice not even then -- see docs/reference/parser.md section 7.6.
func (p *Parser) skipMacroAsComment(ch *chunk) bool {
	const (
		skipStart = iota
		hasSymbols
		hasOBrace
		hasQuote
		hasBackslash
		hasSqBrace
		save
	)
	state, prevState := skipStart, skipStart

	for !ch.atEnd() {
		switch state {
		case skipStart:
			if !testChar(ch.cur(), chWhitespace) {
				state = hasSymbols
			} else if testChar(ch.cur(), chWhitespaceUnsafe) {
				state = save
				continue
			}
			ch.skip()

		case hasSymbols:
			switch ch.cur() {
			case '{':
				state = hasSqBrace
			case '(':
				state = hasOBrace
			case '"':
				state = hasQuote
			case '\n':
				state = save
				continue
			}
			ch.skip()

		case hasOBrace:
			switch ch.cur() {
			case '\\':
				prevState = state
				state = hasBackslash
			case ')':
				state = hasSymbols
			}
			ch.skip()

		case hasSqBrace:
			switch ch.cur() {
			case '\\':
				prevState = state
				state = hasBackslash
			case '}':
				state = save
			}
			ch.skip()

		case hasQuote:
			switch ch.cur() {
			case '\\':
				prevState = state
				state = hasBackslash
			case '"':
				state = save
			}
			ch.skip()

		case hasBackslash:
			state = prevState
			ch.skip()

		case save:
			// A skipped macro is deliberately not recorded as a comment.
			// The range would start after the '.' and carry no comment
			// delimiter, unlike every other saveComment call site, so
			// emitting it would write bare macro text back out as though it
			// were a comment and the result would not reparse.
			return true
		}
	}
	return false
}

// --- Variables ------------------------------------------------------------

// RegisterVariable defines or replaces a variable, mirroring
// ucl_parser_register_variable. An existing variable keeps its position in the
// lookup order, which matters for the loose "$NAME" form.
func (p *Parser) RegisterVariable(name, value string) {
	if p == nil {
		return
	}
	for _, v := range p.variables {
		if v.name == name {
			v.value = value
			return
		}
	}
	p.variables = append(p.variables, &variable{name: name, value: value})
}

// UnregisterVariable removes a variable.
//
// The C spells this ucl_parser_register_variable(parser, var, NULL); Go has no
// nil string, so deletion gets its own method.
func (p *Parser) UnregisterVariable(name string) {
	if p == nil {
		return
	}
	for i, v := range p.variables {
		if v.name == name {
			p.variables = append(p.variables[:i], p.variables[i+1:]...)
			return
		}
	}
}

// SetFilevars defines FILENAME and CURDIR from filename, mirroring
// ucl_parser_set_filevars.
//
// An empty filename stands for the C's NULL: FILENAME becomes the literal
// "undef", CURDIR becomes the process working directory, and cur_file is left
// alone. needExpand resolves the name through realpath first.
func (p *Parser) SetFilevars(filename string, needExpand bool) bool {
	if p == nil {
		return false
	}
	if filename != "" {
		real := filename
		if needExpand {
			r, err := realpath(filename)
			if err != nil {
				return false
			}
			real = r
		}
		p.curFile = real
		p.RegisterVariable("FILENAME", real)
		p.RegisterVariable("CURDIR", filepath.Dir(real))
		return true
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	p.RegisterVariable("FILENAME", "undef")
	p.RegisterVariable("CURDIR", cwd)
	return true
}

// expandVariable is ucl_expand_variable. It reports false when nothing was
// found, in which case the caller keeps the original buffer -- the "trivial
// case" that leaves "$$" sequences untouched in a string with no real
// variables.
func (p *Parser) expandVariable(src []byte) ([]byte, bool) {
	if p.flags&ParserDisableMacro != 0 {
		return nil, false
	}

	end := len(src)
	outLen := 0
	varsFound := false

	for i := 0; i != end; {
		if src[i] == '$' && i+1 != end {
			i = p.checkVariable(src, i+1, end-i-1, &outLen, &varsFound)
		} else {
			i++
			outLen++
		}
	}

	if !varsFound {
		return nil, false
	}

	dst := make([]byte, outLen)
	d := 0
	// The C's loop condition is "p != end", which a variable handler can step
	// straight past (see checkVariableSafe); it then reads one byte beyond the
	// buffer. The bound here is "<" so the read is impossible.
	for i := 0; i < end && d != outLen; {
		if src[i] == '$' && i+1 != end {
			i, d = p.expandSingleVariable(src, i, end-i, dst, d, outLen-d)
		} else {
			dst[d] = src[i]
			d++
			i++
		}
	}
	// The result is the measured length, not what was written: the C
	// NUL-terminates at d and returns out_len, so an under-count from the
	// checkVariableSafe assignment bug leaves trailing bytes in the value.
	// Those bytes are zero here and uninitialised in the C.
	return dst, true
}

// checkVariable is ucl_check_variable: the measuring pass. ptr points at the
// byte after the '$'.
func (p *Parser) checkVariable(src []byte, ptr, remain int, outLen *int, varsFound *bool) int {
	ret := ptr
	found := false

	switch {
	case src[ptr] == '{':
		q := ptr + 1
		end := ptr + remain
		for q < end {
			if src[q] == '}' {
				ret = p.checkVariableSafe(src, ptr+1, q-ptr-1, outLen, true, &found)
				if found {
					// The braces are excluded from the rescan.
					ret++
					*varsFound = true
				} else {
					// Count '$' and '{'; the name and '}' are recounted by the
					// outer loop, which is what leaves ${UNKNOWN} verbatim.
					*outLen += 2
				}
				break
			}
			q++
		}
		if q == end {
			*outLen++
		}

	case src[ptr] != '$':
		ret = p.checkVariableSafe(src, ptr, remain, outLen, false, &found)
		if found {
			*varsFound = true
		} else {
			*outLen++
		}

	default: // "$$"
		ret++
		*outLen++
	}

	return ret
}

// checkVariableSafe is ucl_check_variable_safe: strict matching requires the
// whole braced name, loose matching takes the first registered variable that
// is a prefix of the remaining text.
func (p *Parser) checkVariableSafe(src []byte, ptr, remain int, outLen *int, strict bool, found *bool) int {
	for _, v := range p.variables {
		vl := len(v.name)
		if strict && remain != vl {
			continue
		}
		if !strict && remain < vl {
			continue
		}
		if string(src[ptr:ptr+vl]) == v.name {
			*outLen += len(v.value)
			*found = true
			return ptr + vl
		}
	}

	if !*found && p.varHandler != nil && strict {
		if val, ok := p.varHandler(src[ptr : ptr+remain]); ok {
			*found = true
			// Assignment, not +=: an upstream bug that under-counts when a
			// handled variable is not the first thing in the string. Pass two
			// is length-bounded, so this truncates rather than corrupts.
			*outLen = len(val)
			return ptr + remain
		}
	}
	return ptr
}

// expandSingleVariable is ucl_expand_single_variable: the writing pass. ptr
// points at the '$'.
func (p *Parser) expandSingleVariable(src []byte, ptr, inLen int, dst []byte, d, outLen int) (int, int) {
	q := ptr + 1
	ret := ptr + 1
	inLen--
	strict := false

	if src[q] == '$' {
		dst[d] = src[q]
		return q + 1, d + 1
	}
	if src[q] == '{' {
		q++
		inLen--
		strict = true
		ret += 2
	}

	found := false
	for _, v := range p.variables {
		vl := len(v.name)
		need := vl
		if strict {
			need = vl + 1
		}
		if outLen >= len(v.value) && inLen >= need && string(src[q:q+vl]) == v.name {
			if !strict || src[q+vl] == '}' {
				copy(dst[d:], v.value)
				ret += vl
				d += len(v.value)
				found = true
				break
			}
		}
	}

	if !found && strict && p.varHandler != nil {
		// Note the handler is given the rest of the string, not just the name.
		if val, ok := p.varHandler(src[q : q+inLen]); ok && len(val) <= outLen {
			copy(dst[d:], val)
			ret += inLen
			d += len(val)
			found = true
		}
	}

	if !found {
		if strict && outLen >= 2 {
			// Emit "${" and rescan the name, which reproduces it verbatim.
			dst[d] = src[ptr]
			dst[d+1] = src[ptr+1]
			d += 2
			ret--
		} else {
			dst[d] = src[ptr]
			d++
		}
	}
	return ret, d
}

// --- Include support ------------------------------------------------------

// IncludeTraceFunc is called once per include, mirroring
// ucl_include_trace_func_t.
type IncludeTraceFunc func(p *Parser, parent, args *Object, path []byte)

// SetIncludeTracer installs an include tracer, mirroring
// ucl_parser_set_include_tracer.
func (p *Parser) SetIncludeTracer(f IncludeTraceFunc) {
	if p != nil {
		p.includeTrace = f
	}
}

// includeParams is struct ucl_include_params.
type includeParams struct {
	softFail       bool
	mustExist      bool
	checkSignature bool
	allowGlob      bool
	usePrefix      bool
	prefix         string
	hasPrefix      bool
	target         string
	priority       uint
	parseType      ParseType
	strat          DuplicateStrategy
}

// argMatch is the strncmp(param->key, name, param->keylen) comparison the
// include and load handlers use: a prefix match on the key length, so "t",
// "tr" and "try" all select "try".
func argMatch(key []byte, name string) bool {
	return len(key) <= len(name) && string(key) == name[:len(key)]
}

// includeCommon is ucl_include_common, shared by .include, .try_include and
// .includes.
func (p *Parser) includeCommon(data []byte, args *Object, defaultTry, defaultSign bool) bool {
	params := includeParams{
		softFail:       defaultTry,
		mustExist:      !defaultTry,
		checkSignature: defaultSign,
		target:         "object",
		parseType:      ParseUCL,
		strat:          DuplicateAppend,
	}
	allowURL := false

	if p.includeTrace != nil {
		var parent *Object
		if top := p.stackTop(); top != nil {
			parent = top.obj
		}
		p.includeTrace(p, parent, args, data)
	}

	if args != nil && args.ObjType == TypeObject {
		var it Iter
		for param := Iterate(args, &it, true); param != nil; param = Iterate(args, &it, true) {
			switch param.ObjType {
			case TypeBoolean:
				switch {
				case argMatch(param.key, "try"):
					// Note this does not touch softFail, matching the C.
					params.mustExist = !ToBoolean(param)
				case argMatch(param.key, "sign"):
					params.checkSignature = ToBoolean(param)
				case argMatch(param.key, "glob"):
					params.allowGlob = ToBoolean(param)
				case argMatch(param.key, "url"):
					allowURL = ToBoolean(param)
				case argMatch(param.key, "prefix"):
					params.usePrefix = ToBoolean(param)
				}
			case TypeString:
				switch {
				case argMatch(param.key, "key"):
					params.prefix = ToString(param)
					params.hasPrefix = true
				case argMatch(param.key, "target"):
					params.target = ToString(param)
				case argMatch(param.key, "duplicate"):
					switch ToString(param) {
					case "append":
						params.strat = DuplicateAppend
					case "merge":
						params.strat = DuplicateMerge
					case "rewrite":
						params.strat = DuplicateRewrite
					case "error":
						params.strat = DuplicateError
					}
				}
			case TypeArray:
				if argMatch(param.key, "path") {
					p.SetIncludePath(param)
				}
			case TypeInt:
				if argMatch(param.key, "priority") {
					params.priority = uint(ToInt(param))
				}
			}
		}
	}

	if len(p.includePaths) == 0 {
		if allowURL && strings.Contains(string(data), "://") {
			return p.includeURL(data)
		}
		return p.includeFile(data, &params)
	}

	if allowURL && strings.Contains(string(data), "://") {
		return p.includeURL(data)
	}
	search := false
	for _, ipath := range p.includePaths {
		candidate := []byte(ipath + "/" + string(data))
		if p.includeFile(candidate, &params) {
			search = true
			if !params.allowGlob {
				break
			}
		}
	}
	if search {
		return true
	}
	p.createErr("cannot find file: %s in search path", data)
	return false
}

// includeURL is ucl_include_url. libucl built without libfetch or libcurl
// rejects every URL with this message, and so does this port.
func (p *Parser) includeURL(data []byte) bool {
	p.createErr("URL support is disabled")
	return false
}

// includeFile is ucl_include_file: it expands a glob pattern when globbing is
// enabled and the path contains '*' or '?'.
func (p *Parser) includeFile(data []byte, params *includeParams) bool {
	if !params.allowGlob {
		return p.includeFileSingle(data, params)
	}

	needGlob := false
	for _, c := range data {
		if c == '*' || c == '?' {
			needGlob = true
			break
		}
	}
	if !needGlob {
		return p.includeFileSingle(data, params)
	}

	matches, err := filepath.Glob(string(data))
	if err != nil || len(matches) == 0 {
		// glob(3) failing is not an error unless the file must exist.
		return !params.mustExist
	}
	cnt := 0
	for _, m := range matches {
		if p.includeTrace != nil {
			var parent *Object
			if top := p.stackTop(); top != nil {
				parent = top.obj
			}
			p.includeTrace(p, parent, nil, []byte(m))
		}
		if !p.includeFileSingle([]byte(m), params) {
			if params.softFail {
				continue
			}
			return false
		}
		cnt++
	}
	if cnt == 0 && params.mustExist {
		p.createErr("cannot match any files for pattern %s", data)
		return false
	}
	return true
}

// includeFileSingle is ucl_include_file_single: resolve one path, swap in its
// file variables, optionally nest the content under a key, parse it, and put
// everything back.
func (p *Parser) includeFileSingle(data []byte, params *includeParams) bool {
	if p.state == stateError {
		return false
	}

	filename := string(data)
	real, err := realpath(filename)
	if err != nil {
		if params.softFail {
			return false
		}
		if !params.mustExist && !isPermissionErr(err) {
			// Silently skipped: this is what makes ".include(try=true)" of a
			// missing file a no-op.
			return true
		}
		p.createErr("cannot open file %s: %s", filename, cErrno(err))
		return false
	}

	if p.curFile != "" && real == p.curFile {
		if params.softFail {
			return false
		}
		p.createErr("trying to include the file %s from itself", real)
		return false
	}

	buf, ok := p.fetchFile(real, params.mustExist)
	if !ok {
		if params.softFail {
			return false
		}
		if params.mustExist || p.hasErr {
			return false
		}
		p.ClearError()
		return true
	}

	// Signature checking is compiled out in a build without OpenSSL, which is
	// the build this port matches, so params.checkSignature is a no-op here.

	oldCurFile := p.curFile
	p.curFile = ""
	oldCurdir, oldFilename := p.detachFilevars()
	p.SetFilevars(real, false)

	prevState := p.state
	p.state = stateInit

	prefix := params.prefix
	hasPrefix := params.hasPrefix
	if params.usePrefix && !hasPrefix {
		prefix = filepath.Base(real)
		if ext := filepath.Ext(prefix); ext == ".conf" || ext == ".ucl" {
			prefix = strings.TrimSuffix(prefix, ext)
		}
		hasPrefix = true
	}

	stIdx := -1
	if hasPrefix {
		nestObj, ok := p.prefixedIncludeContainer(prefix, params)
		if !ok {
			return false
		}
		top := p.stackTop()
		stIdx = len(p.stack)
		p.stack = append(p.stack, &stackFrame{
			obj:   nestObj,
			level: top.level,
			flags: top.flags,
			line:  top.line,
			chunk: p.curChunk(),
		})
		p.curObj = nestObj
	}

	res := p.AddChunkFull(buf, params.priority, params.strat, params.parseType)

	if res {
		if stIdx >= 0 {
			p.stack = p.stack[:stIdx]
		}
		if len(p.chunks) > 0 {
			p.popChunk()
			p.recursion--
		}
		p.curFile = oldCurFile
		p.restoreFilevars(oldCurdir, oldFilename)
		p.state = prevState
	}
	// On failure nothing is restored: the parser is in an error state anyway.
	return res
}

// prefixedIncludeContainer implements the "key=" / "prefix=true" part of
// ucl_include_file_single: it finds or builds the object the included content
// is nested into.
func (p *Parser) prefixedIncludeContainer(prefix string, params *includeParams) (*Object, bool) {
	top := p.stackTop().obj
	if top.ov == nil {
		top.ov = newHash(p.flags&ParserKeyLowercase != 0)
	}
	container := top.ov
	oldObj := container.Lookup([]byte(prefix))

	if asciiEqualFold([]byte(params.target), "array") {
		switch {
		case oldObj == nil:
			arr := NewFull(TypeArray, params.priority)
			arr.key = []byte(prefix)
			arr.Prev = arr
			arr.Next = nil
			container.Insert(arr.key, arr)
			top.length++

			nest := NewFull(TypeObject, params.priority)
			nest.Prev = nest
			ArrayAppend(arr, nest)
			return nest, true

		case oldObj.ObjType == TypeArray:
			nest := NewFull(TypeObject, params.priority)
			nest.Prev = nest
			ArrayAppend(oldObj, nest)
			return nest, true

		default:
			// Wrap the existing value in an array. The wrapper aliases the old
			// key rather than copying it, as in the C.
			newObj := TypedNew(TypeArray)
			newObj.key = oldObj.key
			newObj.flags |= FlagMultivalue
			newObj.Prev = newObj

			nest := NewFull(TypeObject, params.priority)
			nest.Prev = nest
			ArrayAppend(newObj, oldObj)
			ArrayAppend(newObj, nest)
			container.Replace(oldObj.key, newObj)
			return nest, true
		}
	}

	switch {
	case oldObj == nil:
		nest := NewFull(TypeObject, params.priority)
		nest.key = []byte(prefix)
		nest.Prev = nest
		container.Insert(nest.key, nest)
		top.length++
		return nest, true

	case oldObj.ObjType == TypeObject:
		return oldObj, true

	default:
		p.createErr("Conflicting type for key: %s, asked %s, has %s",
			prefix, params.target, oldObj.TypeString())
		return nil, false
	}
}

// detachFilevars removes CURDIR and FILENAME from the variable list, keeping
// them so the include can put them back afterwards.
func (p *Parser) detachFilevars() (curdir, filename *variable) {
	kept := p.variables[:0]
	for _, v := range p.variables {
		switch v.name {
		case "CURDIR":
			curdir = v
		case "FILENAME":
			filename = v
		default:
			kept = append(kept, v)
		}
	}
	p.variables = kept
	return curdir, filename
}

// restoreFilevars drops the include's own CURDIR/FILENAME and re-appends the
// saved ones, which moves them to the end of the lookup order exactly as the C
// does.
func (p *Parser) restoreFilevars(curdir, filename *variable) {
	kept := p.variables[:0]
	for _, v := range p.variables {
		if (v.name == "CURDIR" && curdir != nil) || (v.name == "FILENAME" && filename != nil) {
			continue
		}
		kept = append(kept, v)
	}
	p.variables = kept
	if filename != nil {
		p.variables = append(p.variables, filename)
	}
	if curdir != nil {
		p.variables = append(p.variables, curdir)
	}
}

// --- Built-in macro handlers ----------------------------------------------

// priorityHandler is ucl_priority_handler: it changes the current chunk's
// priority for the rest of that chunk.
func (p *Parser) priorityHandler(data []byte, args *Object) bool {
	priority := uint(255)
	found := false

	if args != nil && args.ObjType == TypeObject {
		var it Iter
		for param := Iterate(args, &it, true); param != nil; param = Iterate(args, &it, true) {
			if param.ObjType == TypeInt && argMatch(param.key, "priority") {
				priority = uint(ToInt(param))
				found = true
			}
		}
	}

	if len(data) > 0 {
		v, n := cStrtol(string(data))
		if n != len(data) {
			p.createErr("Invalid priority value in macro: %s", data)
			return false
		}
		priority = uint(v)
		found = true
	}

	if found {
		p.curChunk().priority = priority
		return true
	}
	p.createErr("Unable to parse priority macro")
	return false
}

// loadHandler is ucl_load_handler: it reads a file and stores its contents as
// the value of a key.
func (p *Parser) loadHandler(data []byte, args *Object) bool {
	tryLoad, multiline := false, false
	target := "string"
	prefix := ""
	priority := uint(0)
	var flags StringFlag

	if args != nil && args.ObjType == TypeObject {
		var it Iter
		for param := Iterate(args, &it, true); param != nil; param = Iterate(args, &it, true) {
			switch param.ObjType {
			case TypeBoolean:
				switch {
				case argMatch(param.key, "try"):
					tryLoad = ToBoolean(param)
				case argMatch(param.key, "multiline"):
					multiline = ToBoolean(param)
				case argMatch(param.key, "escape"):
					if ToBoolean(param) {
						flags |= StringEscape
					}
				case argMatch(param.key, "trim"):
					if ToBoolean(param) {
						flags |= StringTrim
					}
				}
			case TypeString:
				switch {
				case argMatch(param.key, "key"):
					prefix = ToString(param)
				case argMatch(param.key, "target"):
					target = ToString(param)
				}
			case TypeInt:
				if argMatch(param.key, "priority") {
					priority = uint(ToInt(param))
				}
			}
		}
	}

	if prefix == "" {
		p.createErr("No Key specified in load macro")
		return false
	}
	if len(data) == 0 {
		p.createErr("Unable to parse load macro")
		return false
	}

	buf, ok := p.fetchFile(string(data), !tryLoad)
	if !ok {
		if tryLoad {
			p.ClearError()
		}
		return tryLoad
	}

	container := p.stackTop().obj
	if container.Lookup(prefix) != nil {
		p.createErr("Key %s already exists", prefix)
		return false
	}

	var obj *Object
	switch {
	case asciiEqualFold([]byte(target), "string"):
		obj = FromStringCommon(buf, flags)
		if obj != nil && multiline {
			obj.flags |= FlagMultiline
		}
	case asciiEqualFold([]byte(target), "int"):
		iv, _ := cStrtol(string(buf))
		obj = FromInt(iv)
	default:
		// Any other target silently produces no object at all.
	}

	if obj != nil {
		obj.key = []byte(prefix)
		obj.Prev = obj
		obj.Next = nil
		obj.SetPriority(priority)
		container.InsertKey(obj, obj.key, false)
	}
	return true
}

// inheritHandler is ucl_inherit_handler: copy the keys of another object into
// the one being built.
//
// Copies made without replace=true are flagged FlagInherited, which is what
// makes a later real assignment win regardless of priority.
func (p *Parser) inheritHandler(data []byte, args, ctx *Object) bool {
	parent := ctx.LookupLen(data)
	if parent == nil || parent.ObjType != TypeObject {
		p.createErr("Unable to find inherited object %s", data)
		return false
	}

	top := p.stackTop()
	if top == nil || top.obj == nil || top.obj.ObjType != TypeObject {
		p.createErr("Invalid inherit context")
		return false
	}
	target := top.obj

	replace := false
	if args != nil {
		if cur := args.Lookup("replace"); cur != nil {
			replace = ToBoolean(cur)
		}
	}

	var it Iter
	for cur := Iterate(parent, &it, true); cur != nil; cur = Iterate(parent, &it, true) {
		if !replace && target.LookupLen(cur.key) != nil {
			continue
		}
		cp := Copy(cur)
		if !replace {
			cp.flags |= FlagInherited
		}
		target.InsertKey(cp, cp.key, false)
	}
	return true
}

// --- Filesystem -----------------------------------------------------------

// realpath is C realpath(3): every path component must exist, and the result
// is absolute with symlinks resolved.
func realpath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func isPermissionErr(err error) bool { return os.IsPermission(err) }

// fetchFile is ucl_fetch_file. An empty file yields a nil buffer and success,
// mirroring the "do not map empty files" branch.
func (p *Parser) fetchFile(path string, mustExist bool) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		// The C always records this one, whatever must_exist says.
		p.createErr("cannot open file %s: %s", path, cErrno(err))
		return nil, false
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		if mustExist || isPermissionErr(err) {
			p.createErr("cannot stat file %s: %s", path, cErrno(err))
		}
		return nil, false
	}
	if !st.Mode().IsRegular() {
		if mustExist {
			p.createErr("file %s is not a regular file", path)
		}
		return nil, false
	}
	if st.Size() == 0 {
		return nil, true
	}

	buf := make([]byte, st.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		p.createErr("cannot mmap file %s: %s", path, cErrno(err))
		return nil, false
	}
	return buf, true
}

// cStrtol is strtol(s, &endptr, 10): it returns the value and the index of the
// first unconsumed byte, so a caller can tell whether the whole string was
// numeric.
func cStrtol(s string) (int64, int) {
	i := 0
	for i < len(s) && isWhitespaceUnsafe(s[i]) {
		i++
	}
	start := i
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == digits {
		return 0, 0
	}
	v, err := strconv.ParseInt(s[start:i], 10, 64)
	if err != nil {
		// strtol saturates on overflow.
		if s[start] == '-' {
			return math.MinInt64, i
		}
		return math.MaxInt64, i
	}
	return v, i
}
