package goucl

import (
	"errors"
	"strconv"
)

// String-to-value parsing, shared by the object API (FromStringCommon) and,
// later, the parser. Mirrors ucl_maybe_parse_number (src/ucl_parser.c) and
// ucl_maybe_parse_boolean (src/ucl_internal.h).
//
// These helpers are deliberately literal transliterations of the C, quirks
// included: see docs/object-model.md section 5.

// errNotNumber mirrors the C's EINVAL return: the input is not a number and
// the caller should leave the value as a string.
var errNotNumber = errors.New("goucl: not a number")

// errNumberRange mirrors the C's ERANGE return: the input looked like a number
// but strtod/strtoimax overflowed.
var errNumberRange = errors.New("goucl: numeric value out of range")

// Character classes decoded from ucl_chartable.h. Verified byte-for-byte
// against the compiled table; see docs/object-model.md section 5.5.

// isAtomEnd reports whether c ends a value atom (UCL_CHARACTER_VALUE_END).
func isAtomEnd(c byte) bool {
	switch c {
	case 0x00, '\n', '\r', '#', ',', ';', ']', '}':
		return true
	}
	return false
}

// isWhitespace reports UCL_CHARACTER_WHITESPACE: tab and space only.
func isWhitespace(c byte) bool {
	return c == '\t' || c == ' '
}

// isWhitespaceUnsafe reports UCL_CHARACTER_WHITESPACE_UNSAFE, the class used
// for UCL_STRING_TRIM.
func isWhitespaceUnsafe(c byte) bool {
	switch c {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}

// isJSONUnsafe reports UCL_CHARACTER_JSON_UNSAFE. Note it does not include
// '\v', which reaches the escaper only via the whitespace-unsafe class.
func isJSONUnsafe(c byte) bool {
	switch c {
	case 0x00, '\b', '\t', '\n', '\f', '\r', '"', '\\':
		return true
	}
	return false
}

// isUCLUnsafe reports UCL_CHARACTER_UCL_UNSAFE, the class that makes
// insert_key set FlagNeedKeyEscape. Note it contains '+', ':', '=', '[', '{'
// and space, but not '}', ']', ',', ';', '#' or the single-quote byte.
func isUCLUnsafe(c byte) bool {
	switch c {
	case 0x00, '\b', '\t', '\n', '\f', '\r', ' ', '"', '+', ':', '=', '[', '\\', '{':
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isXDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// asciiLower folds a single byte the way C's tolower does in the "C" locale,
// and the way lc_map[] does in ucl_hash.c: ASCII only, bytes >= 0x80 untouched.
func asciiLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// lexNumMultiplier mirrors ucl_lex_num_multiplier: the k/m/g scale factors,
// decimal by default and binary when isBytes is set.
func lexNumMultiplier(c byte, isBytes bool) int64 {
	switch asciiLower(c) {
	case 'k':
		if isBytes {
			return 1024
		}
		return 1000
	case 'm':
		if isBytes {
			return 1024 * 1024
		}
		return 1000 * 1000
	case 'g':
		if isBytes {
			return 1024 * 1024 * 1024
		}
		return 1000 * 1000 * 1000
	}
	return 1
}

// lexTimeMultiplier mirrors ucl_lex_time_multiplier: the number of seconds in
// one unit.
//
// The 'm' (minutes) entry is dead code upstream: 'm' is always claimed by the
// k/m/g case in maybeParseNumber, so minutes only ever come from the literal
// "min" spelling, which hard-codes *= 60. The 'y' entry is 365 days exactly,
// not 365.25.
func lexTimeMultiplier(c byte) float64 {
	switch asciiLower(c) {
	case 'm':
		return 60
	case 'h':
		return 60 * 60
	case 'd':
		return 60 * 60 * 24
	case 'w':
		return 60 * 60 * 24 * 7
	case 'y':
		return 60 * 60 * 24 * 365
	}
	return 1
}

// maybeParseBoolean mirrors ucl_maybe_parse_boolean.
//
// The match is length-dispatched and then case-insensitive, so exactly six
// whole words are booleans: false, true, yes, off, no, on. Nothing else is --
// not "y", "n", "1", "0", "t", "f" or "enabled". On a match obj (which may be
// nil, for a pure test) becomes TypeBoolean.
func maybeParseBoolean(obj *Object, b []byte) bool {
	var val, ok bool

	switch len(b) {
	case 5:
		if asciiEqualFold(b, "false") {
			ok, val = true, false
		}
	case 4:
		if asciiEqualFold(b, "true") {
			ok, val = true, true
		}
	case 3:
		if asciiEqualFold(b, "yes") {
			ok, val = true, true
		} else if asciiEqualFold(b, "off") {
			ok, val = true, false
		}
	case 2:
		if asciiEqualFold(b, "no") {
			ok, val = true, false
		} else if asciiEqualFold(b, "on") {
			ok, val = true, true
		}
	}

	if ok && obj != nil {
		obj.setBool(val)
	}
	return ok
}

// asciiEqualFold reports whether b equals s ignoring ASCII case, mirroring
// strncasecmp in the C locale. strings.EqualFold is Unicode-aware and would
// match inputs the C rejects.
func asciiEqualFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if asciiLower(b[i]) != asciiLower(s[i]) {
			return false
		}
	}
	return true
}

// maybeParseNumber mirrors ucl_maybe_parse_number.
//
// It scans b for a numeric literal with an optional multiplier, byte-size or
// time suffix, sets obj's type and value on success, and returns the index of
// the first byte it did not consume. On failure it returns errNotNumber (the
// C's EINVAL) or errNumberRange (ERANGE) together with the position the C
// would have stored in *pos.
//
// allowDouble permits '.'/exponent forms (and is what lets a time suffix
// produce TypeTime rather than a bare TypeInt); numberBytes makes a k/m/g
// suffix binary; allowTime enables the s/min/h/d/w/y suffixes.
//
// obj may be nil, in which case the input is only validated.
func maybeParseNumber(obj *Object, b []byte, allowDouble, numberBytes, allowTime bool) (int, error) {
	end := len(b)
	p, c := 0, 0
	var gotDot, gotExp, needDouble, isTime, validStart, isHex bool
	isNeg := 0
	var dv float64
	var lv int64

	// The C reads *p before checking p < end; the buffer it is handed is
	// always NUL-terminated, so an empty input reads the NUL rather than
	// running off the end.
	if end > 0 && b[0] == '-' {
		isNeg = 1
		c++
		p++
	}

scan:
	for p < end {
		switch {
		case isHex && isXDigit(b[p]):
			// Note: hex digits do not set validStart, so the leading '0' of
			// "0x..." is what makes the literal valid; a bare "xFF" is not.
			p++
		case isDigit(b[p]):
			validStart = true
			p++
		case !isHex && (b[p] == 'x' || b[p] == 'X'):
			isHex = true
			allowDouble = false
			c = p + 1
			p++
		case allowDouble:
			switch {
			case p == c:
				return 0, errNotNumber // empty digit sequence
			case b[p] == '.':
				if gotDot {
					return 0, errNotNumber
				}
				gotDot, needDouble = true, true
				p++
			case b[p] == 'e' || b[p] == 'E':
				if gotExp {
					return 0, errNotNumber
				}
				gotExp, needDouble = true, true
				p++
				if p >= end {
					return 0, errNotNumber
				}
				if !isDigit(b[p]) && b[p] != '+' && b[p] != '-' {
					return 0, errNotNumber
				}
				p++
			default:
				break scan
			}
		case b[p] == '.': // !allowDouble
			return 0, errNotNumber
		default:
			break scan
		}
	}

	if !validStart || p == c {
		return 0, errNotNumber
	}

	// The C copies the digits into a 128-byte stack buffer, so a literal of
	// 127 or more significant characters is rejected outright.
	if p-c+1 >= 128 {
		return 0, errNotNumber
	}
	numbuf := make([]byte, 0, 128)
	if isNeg != 0 {
		numbuf = append(numbuf, '-')
	}
	numbuf = append(numbuf, b[c:p]...)

	var consumed int
	var rangeErr bool
	if needDouble {
		dv, consumed, rangeErr = cStrtod(numbuf)
	} else {
		base := 10
		if isHex {
			// strtoimax is called with an explicit base, so a leading zero is
			// never octal: libucl reads "0755" as decimal 755.
			base = 16
		}
		lv, consumed, rangeErr = cStrtoimax(numbuf, base)
	}
	if rangeErr {
		return 0, errNumberRange
	}

	// setObj mirrors the C's set_obj label. It closes over dv/lv/needDouble/
	// isTime so the suffix handling below can adjust them first.
	setObj := func(pos int) (int, error) {
		if obj != nil {
			if allowDouble && (needDouble || isTime) {
				if isTime {
					obj.setTime(dv)
				} else {
					obj.setFloat(dv)
				}
			} else {
				obj.setInt(lv)
			}
		}
		return pos, nil
	}

	// Map the end pointer back from numbuf into the source buffer.
	endptr := c + consumed - isNeg
	if endptr >= end {
		return setObj(end)
	}
	if isAtomEnd(b[endptr]) || b[endptr] == 0 {
		return setObj(endptr)
	}

	if endptr != 0 {
		p = endptr
		switch b[p] {
		case 'm', 'M', 'g', 'G', 'k', 'K':
			if end-p >= 2 {
				switch {
				case b[p+1] == 's' || b[p+1] == 'S':
					// Milliseconds ("ms"), or seconds scaled by k/g.
					if !needDouble {
						needDouble = true
						dv = float64(lv)
					}
					isTime = true
					if b[p] == 'm' || b[p] == 'M' {
						dv /= 1000.
					} else {
						dv *= float64(lexNumMultiplier(b[p], false))
					}
					p += 2
					if end-p > 0 && !isAtomEnd(b[p]) {
						return 0, errNotNumber
					}
					return setObj(p)

				case numberBytes || b[p+1] == 'b' || b[p+1] == 'B':
					// Bytes. Quirk: with numberBytes set the second byte is
					// consumed whatever it is, so "10k;" yields 10240 and
					// swallows the ';'.
					if needDouble {
						needDouble = false
						lv = int64(dv)
					}
					lv *= lexNumMultiplier(b[p], true)
					p += 2
					if end-p > 0 && !isAtomEnd(b[p]) {
						return 0, errNotNumber
					}
					return setObj(p)

				case isAtomEnd(b[p+1]):
					// Plain multiplier: "10k" is 10000, or 10240 with
					// numberBytes. Never minutes -- see below.
					if needDouble {
						dv *= float64(lexNumMultiplier(b[p], false))
					} else {
						lv *= lexNumMultiplier(b[p], numberBytes)
					}
					p++
					return setObj(p)

				case allowTime && end-p >= 3:
					if asciiLower(b[p]) == 'm' && asciiLower(b[p+1]) == 'i' &&
						asciiLower(b[p+2]) == 'n' {
						// Minutes are spelled "min" and nothing else: "10m" is
						// ten million, because 'm' is claimed above.
						if !needDouble {
							needDouble = true
							dv = float64(lv)
						}
						isTime = true
						dv *= 60.
						p += 3
						if end-p > 0 && !isAtomEnd(b[p]) {
							return 0, errNotNumber
						}
						return setObj(p)
					}
				}
			} else {
				if needDouble {
					dv *= float64(lexNumMultiplier(b[p], false))
				} else {
					lv *= lexNumMultiplier(b[p], numberBytes)
				}
				p++
				if end-p > 0 && !isAtomEnd(b[p]) {
					return 0, errNotNumber
				}
				return setObj(p)
			}

		case 's', 'S':
			if allowTime && (p == end-1 || isAtomEnd(b[p+1])) {
				// Seconds are the base unit, so there is no scaling here.
				if !needDouble {
					needDouble = true
					dv = float64(lv)
				}
				p++
				isTime = true
				return setObj(p)
			}

		case 'h', 'H', 'd', 'D', 'w', 'W', 'y', 'Y':
			if allowTime && (p == end-1 || isAtomEnd(b[p+1])) {
				if !needDouble {
					needDouble = true
					dv = float64(lv)
				}
				isTime = true
				dv *= lexTimeMultiplier(b[p])
				p++
				return setObj(p)
			}

		case '\t', ' ':
			for p < end && isWhitespace(b[p]) {
				p++
			}
			if p == end || isAtomEnd(b[p]) {
				return setObj(p)
			}
		}
	}

	// Fallthrough: the C sets *pos to c, not to start.
	return c, errNotNumber
}

// cStrtod mirrors strtod(s, &endptr) for the restricted inputs
// maybeParseNumber can produce: an optional sign, decimal digits, an optional
// fraction and an optional exponent. It returns the value, the number of bytes
// consumed (0 if no conversion was performed) and whether the conversion
// overflowed (the C's ERANGE).
//
// Hex-float, "inf" and "nan" spellings are unreachable here: maybeParseNumber
// strips the "0x" prefix before building the buffer and only ever copies
// digits, '.', 'e', 'E' and signs into it.
func cStrtod(s []byte) (float64, int, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := 0
	for i < len(s) && isDigit(s[i]) {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0, 0, false
	}
	mantissa := i
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		expDigits := 0
		for j < len(s) && isDigit(s[j]) {
			j++
			expDigits++
		}
		if expDigits > 0 {
			i = j
		} else {
			i = mantissa
		}
	}

	v, err := strconv.ParseFloat(string(s[:i]), 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return v, i, true
		}
		return 0, 0, false
	}
	return v, i, false
}

// cStrtoimax mirrors strtoimax(s, &endptr, base) for base 10 and 16: an
// optional sign followed by digits in that base. It returns the value, the
// number of bytes consumed (0 if no conversion was performed) and whether the
// conversion overflowed (the C's ERANGE).
func cStrtoimax(s []byte, base int) (int64, int, bool) {
	i := 0
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for i < len(s) && digitValue(s[i]) >= 0 && digitValue(s[i]) < base {
		i++
	}
	if i == start {
		return 0, 0, false
	}

	text := string(s[start:i])
	if neg {
		text = "-" + text
	}
	v, err := strconv.ParseInt(text, base, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			// glibc clamps to INTMAX_MAX/INTMAX_MIN and sets ERANGE, which is
			// what ParseInt returns alongside the error.
			return v, i, true
		}
		return 0, 0, false
	}
	return v, i, false
}

// digitValue returns the value of an ASCII digit in base 16, or -1.
func digitValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// cStrtoul mirrors strtoul(s, &endptr, 10): optional whitespace, an optional
// sign, then decimal digits, with wrap-around negation and saturation on
// overflow. It returns the value and the index of the first unconsumed byte
// (0 when no conversion was performed, matching endptr == nptr).
//
// LookupPathChar needs the endptr semantics to decide whether an array index
// segment was fully numeric.
func cStrtoul(s string) (uint64, int) {
	i := 0
	// isspace() in the C locale is exactly the WHITESPACE_UNSAFE class.
	for i < len(s) && isWhitespaceUnsafe(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var v uint64
	overflow := false
	for i < len(s) && isDigit(s[i]) {
		d := uint64(s[i] - '0')
		if v > (^uint64(0)-d)/10 {
			overflow = true
		}
		v = v*10 + d
		i++
	}
	if i == start {
		return 0, 0
	}
	if overflow {
		v = ^uint64(0)
	}
	if neg {
		v = -v
	}
	return v, i
}

// escapeJSONString mirrors the UCL_STRING_ESCAPE branch of
// ucl_object_fromstring_common.
//
// libucl's escaper is deliberately narrow: only the bytes in
// JSON_UNSAFE|WHITESPACE_UNSAFE are touched, NUL and vertical tab become
// the six-byte JSON escapes for those code points, space is passed through
// unchanged, and every other control character is copied verbatim.
func escapeJSONString(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if !isJSONUnsafe(c) && !isWhitespaceUnsafe(c) {
			out = append(out, c)
			continue
		}
		switch c {
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\b':
			out = append(out, '\\', 'b')
		case '\t':
			out = append(out, '\\', 't')
		case '\f':
			out = append(out, '\\', 'f')
		case 0x00:
			out = append(out, '\\', 'u', '0', '0', '0', '0')
		case '\v':
			out = append(out, '\\', 'u', '0', '0', '0', 'B')
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		case ' ':
			out = append(out, ' ')
		}
	}
	return out
}
