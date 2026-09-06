package core

// JSON Schema draft-4 validation, a port of libucl/src/ucl_schema.c.
//
// The structure follows the C file function for function so the two can be
// diffed side by side; each Go function names the C function it mirrors.
//
// libucl's validator is not a conforming draft-4 implementation and this port
// does not try to make it one -- see docs/reference/schema.md for the catalogue of
// deliberate deviations (no boolean schemas, $ref is additive rather than
// exclusive, no RFC 6901 ~0/~1 unescaping, "number" accepts integers and
// times, malformed constraint values are silently ignored rather than
// rejected). Where this port departs from the C as well, the departure is a
// C defect that the upstream test corpus does not reach; every one of them is
// marked "Divergence" below and listed in docs/notes/schema.md.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// SchemaErrorCode mirrors enum ucl_schema_error_code.
type SchemaErrorCode int

const (
	// SchemaOK is the zero value: no error.
	SchemaOK SchemaErrorCode = iota
	// SchemaTypeMismatch is a "type" keyword violation.
	SchemaTypeMismatch
	// SchemaInvalidSchema is a malformed schema, including an unresolvable
	// $ref and a pattern that will not compile.
	SchemaInvalidSchema
	// SchemaMissingProperty is a "required" violation.
	SchemaMissingProperty
	// SchemaConstraint is any other data-constraint violation.
	SchemaConstraint
	// SchemaMissingDependency is a "dependencies" violation.
	SchemaMissingDependency
	// SchemaExternalRefMissing exists in the C enum but ucl_schema.c never
	// produces it; kept for API parity and never returned here either.
	SchemaExternalRefMissing
	// SchemaExternalRefInvalid is the second never-produced C code.
	SchemaExternalRefInvalid
	// SchemaInternalError covers allocation failure in the C. This port uses
	// it only for the recursion-depth guard, which the C does not have.
	SchemaInternalError
	// SchemaUnknown is the C's generic code.
	SchemaUnknown
)

// String names the code the way the C enum spells it.
func (c SchemaErrorCode) String() string {
	switch c {
	case SchemaOK:
		return "UCL_SCHEMA_OK"
	case SchemaTypeMismatch:
		return "UCL_SCHEMA_TYPE_MISMATCH"
	case SchemaInvalidSchema:
		return "UCL_SCHEMA_INVALID_SCHEMA"
	case SchemaMissingProperty:
		return "UCL_SCHEMA_MISSING_PROPERTY"
	case SchemaConstraint:
		return "UCL_SCHEMA_CONSTRAINT"
	case SchemaMissingDependency:
		return "UCL_SCHEMA_MISSING_DEPENDENCY"
	case SchemaExternalRefMissing:
		return "UCL_SCHEMA_EXTERNAL_REF_MISSING"
	case SchemaExternalRefInvalid:
		return "UCL_SCHEMA_EXTERNAL_REF_INVALID"
	case SchemaInternalError:
		return "UCL_SCHEMA_INTERNAL_ERROR"
	case SchemaUnknown:
		return "UCL_SCHEMA_UNKNOWN"
	}
	return "UCL_SCHEMA_UNKNOWN"
}

// SchemaError mirrors struct ucl_schema_error.
//
// Obj is the node the failing check was about: the offending schema keyword
// for schema-shape errors, the offending data node for constraint errors.
type SchemaError struct {
	Code SchemaErrorCode
	Msg  string
	Obj  *Object
}

// Error implements error.
func (e *SchemaError) Error() string { return e.Msg }

// schemaErrMsgSize is the size of the C's char msg[128]. Messages are
// truncated to the same budget so a Go caller cannot come to depend on text
// the C would have cut off.
const schemaErrMsgSize = 128

// maxSchemaDepth bounds the recursion. This has no counterpart in
// ucl_schema.c, which recurses without any limit and will overflow the stack
// on a self-referential schema such as {"$ref": "#"} at the top level (see
// docs/reference/schema.md section 4, "Cycles and recursion depth"). The guard is a
// deliberate addition; it reports SchemaInternalError rather than crashing,
// and is set far above anything a real schema reaches.
const maxSchemaDepth = 1024

// RefFetcher retrieves the bytes of an external $ref document.
//
// This has no counterpart in the C, where ucl_fetch_url/ucl_fetch_file are
// called directly. Making it an interface keeps a schema-validation call from
// reaching the network unless the caller asked for it, matching upstream's own
// posture of gating URL includes behind the ENABLE_URL_INCLUDE build option
// and shipping tests/schema/refRemote.json disabled.
type RefFetcher interface {
	// FetchRef returns the raw bytes of the document named by ref, which is
	// the part of a "$ref" value before the '#'.
	FetchRef(ref string) ([]byte, error)
}

// LocalRefFetcher reads $ref documents from the filesystem and refuses
// anything that looks like a URL. It is the default fetcher.
//
// Paths are used exactly as written, resolved against the process working
// directory, mirroring ucl_fetch_file. libucl does not implement draft-4's
// "id" keyword, so there is no base URI to resolve a relative ref against.
type LocalRefFetcher struct{}

// FetchRef implements RefFetcher.
func (LocalRefFetcher) FetchRef(ref string) ([]byte, error) {
	if strings.Contains(ref, "://") {
		return nil, fmt.Errorf("fetching remote references is not supported")
	}
	return os.ReadFile(ref)
}

// Validate reports whether obj satisfies schema, mirroring
// ucl_object_validate. Internal "#" references resolve against schema itself.
//
// The returned error is nil exactly when the first result is true.
func Validate(schema, obj *Object) (bool, *SchemaError) {
	return ValidateRootExt(schema, obj, schema, nil)
}

// ValidateRoot is Validate with a separate document for resolving internal
// references, mirroring ucl_object_validate_root. Use it to validate against
// a sub-schema whose "#/..." refs point into the enclosing document.
func ValidateRoot(schema, obj, root *Object) (bool, *SchemaError) {
	return ValidateRootExt(schema, obj, root, nil)
}

// ValidateRootExt is ValidateRoot with a caller-owned cache of fetched
// external documents, mirroring ucl_object_validate_root_ext.
//
// extRefs is a TypeObject used as a map from the pre-'#' part of a $ref to the
// parsed document; passing the same object to several calls amortises the
// fetches. A nil extRefs gets a fresh cache for the duration of the call.
func ValidateRootExt(schema, obj, root, extRefs *Object) (bool, *SchemaError) {
	return ValidateRootExtFetch(schema, obj, root, extRefs, nil)
}

// ValidateRootExtFetch is ValidateRootExt with an explicit fetcher for
// external references. A nil fetcher means LocalRefFetcher.
//
// There is no C counterpart: libucl calls ucl_fetch_url/ucl_fetch_file
// unconditionally.
func ValidateRootExtFetch(schema, obj, root, extRefs *Object, fetcher RefFetcher) (bool, *SchemaError) {
	if extRefs == nil {
		extRefs = TypedNew(TypeObject)
	}
	if fetcher == nil {
		fetcher = LocalRefFetcher{}
	}
	ctx := &schemaCtx{
		root:    root,
		extRefs: extRefs,
		fetcher: fetcher,
		err:     &SchemaError{},
	}
	if ctx.validate(schema, obj, true) {
		return true, nil
	}
	// The C leaves err->msg and err->obj holding whatever the last failed
	// sub-attempt wrote even when the overall result is success; returning a
	// fresh error only on failure removes that footgun (docs/reference/schema.md 5).
	e := *ctx.err
	return false, &e
}

// schemaCtx carries the parameters ucl_schema_validate threads through the
// recursion: the root document for internal refs, the external-ref cache and
// the single mutable error slot.
type schemaCtx struct {
	root    *Object
	extRefs *Object
	fetcher RefFetcher
	err     *SchemaError
	depth   int
}

// errorf mirrors ucl_schema_create_error, including its fixed message budget.
func (c *schemaCtx) errorf(code SchemaErrorCode, obj *Object, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(msg) >= schemaErrMsgSize {
		msg = msg[:schemaErrMsgSize-1]
	}
	c.err.Code = code
	c.err.Obj = obj
	c.err.Msg = msg
}

// schemaKey returns o's key as the C sees it. ucl_object_key hands back a
// NUL-terminated string, so anything past an embedded NUL is invisible to the
// strcmp dispatch in ucl_schema_validate_object and friends.
func schemaKey(o *Object) string { return string(cStr(o.Key())) }

// validate mirrors the static ucl_schema_validate.
//
// tryArray is true at every "validate this value against this schema" call
// site in the C; it enables the UCL-only implicit-array handling described in
// docs/reference/schema.md section 2.
func (c *schemaCtx) validate(schema, obj *Object, tryArray bool) bool {
	if schema.Type() != TypeObject {
		c.errorf(SchemaInvalidSchema, schema, "schema is %s instead of object",
			schema.TypeString())
		return false
	}

	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxSchemaDepth {
		// Not in the C; see maxSchemaDepth.
		c.errorf(SchemaInternalError, schema,
			"schema recursion is too deep: more than %d levels", maxSchemaDepth)
		return false
	}

	if tryArray {
		// Special case for multiple values: UCL threads duplicate keys onto
		// one Next chain, and every element must satisfy the schema.
		if !c.validateValues(schema, obj) {
			return false
		}
		for cur := obj; cur != nil; cur = cur.Next {
			if !c.validate(schema, cur, false) {
				return false
			}
		}
		return true
	}

	if elt := schema.Lookup("enum"); elt != nil && elt.Type() == TypeArray {
		if !c.validateEnum(elt, obj) {
			return false
		}
	}

	if elt := schema.Lookup("allOf"); elt != nil && elt.Type() == TypeArray {
		var it Iter
		for {
			cur := Iterate(elt, &it, true)
			if cur == nil {
				break
			}
			if !c.validate(cur, obj, true) {
				return false
			}
		}
	}

	if elt := schema.Lookup("anyOf"); elt != nil && elt.Type() == TypeArray {
		// The C leaves `ret` uninitialised when the array is empty, so an
		// empty anyOf is undefined there. Treating it as "matched nothing" is
		// the only defined reading.
		ret := false
		var it Iter
		for {
			cur := Iterate(elt, &it, true)
			if cur == nil {
				break
			}
			if ret = c.validate(cur, obj, true); ret {
				break
			}
		}
		if !ret {
			return false
		}
		c.err.Code = SchemaOK
	}

	if elt := schema.Lookup("oneOf"); elt != nil && elt.Type() == TypeArray {
		// Exactly one must match. Once one has, the remaining sub-schemas are
		// still evaluated, purely to detect a second match. There is no error
		// distinguishing "none matched" from "more than one matched": the
		// error left behind by the last attempt is what surfaces.
		ret := false
		var it Iter
		for {
			cur := Iterate(elt, &it, true)
			if cur == nil {
				break
			}
			if !ret {
				ret = c.validate(cur, obj, true)
			} else if c.validate(cur, obj, true) {
				ret = false
				break
			}
		}
		if !ret {
			return false
		}
	}

	// "not" is honoured only when it is an object: a "not" holding an array or
	// a string is silently ignored, as in the C.
	if elt := schema.Lookup("not"); elt != nil && elt.Type() == TypeObject {
		if c.validate(elt, obj, true) {
			// No error is recorded for this failure; the C leaves err holding
			// whatever the successful inner validation did.
			return false
		}
		c.err.Code = SchemaOK
	}

	// $ref is evaluated before "type" and before the per-type body, and the
	// original schema then continues to be evaluated: in libucl $ref is
	// additive, not exclusive.
	if elt := schema.Lookup("$ref"); elt != nil {
		if elt.Type() != TypeString {
			// Divergence: the C passes a non-string $ref straight to
			// ucl_object_tostring, which yields NULL, and then indexes it.
			c.errorf(SchemaInvalidSchema, elt, "$ref is %s instead of string",
				elt.TypeString())
			return false
		}
		target, refRoot := c.resolveRef(c.root, ToString(elt))
		if target == nil {
			return false
		}
		saved := c.root
		c.root = refRoot
		ok := c.validate(target, obj, tryArray)
		c.root = saved
		if !ok {
			return false
		}
	}

	if !c.typeIsAllowed(schema.Lookup("type"), obj) {
		return false
	}

	switch obj.Type() {
	case TypeObject:
		return c.validateObject(schema, obj)
	case TypeArray:
		return c.validateArray(schema, obj)
	case TypeInt, TypeFloat:
		return c.validateNumber(schema, obj)
	case TypeString:
		return c.validateString(schema, obj)
	}
	// Booleans, nulls, times and userdata have no body validator: "type",
	// "enum" and the combinators are the only keywords that ever see them.
	return true
}

// validateValues mirrors ucl_schema_validate_values, which implements the
// UCL-only minValues/maxValues keywords over an implicit array.
//
// Both are read only when present and of integer type; any other type is
// silently ignored. Neither appears anywhere in tests/schema, so this whole
// function is exercised only by this port's own unit tests.
func (c *schemaCtx) validateValues(schema, obj *Object) bool {
	if elt := schema.Lookup("maxValues"); elt != nil && elt.Type() == TypeInt {
		constraint := ToInt(elt)
		var i int64
		for cur := obj; cur != nil; cur = cur.Next {
			// The C compares before counting the current element, so a chain
			// of constraint+1 elements is already a failure and the message
			// prints the constraint rather than the count.
			if i > constraint {
				c.errorf(SchemaConstraint, obj,
					"object has more values than defined: %d", constraint)
				return false
			}
			i++
		}
	}
	if elt := schema.Lookup("minValues"); elt != nil && elt.Type() == TypeInt {
		constraint := ToInt(elt)
		var i int64
		for cur := obj; cur != nil && i < constraint; cur = cur.Next {
			i++
		}
		if i < constraint {
			c.errorf(SchemaConstraint, obj,
				"object has less values than defined: %d", constraint)
			return false
		}
	}
	return true
}

// validateEnum mirrors ucl_schema_validate_enum. Membership is decided by
// Compare, which is ucl_object_compare and not a general deep equality; see
// its doc comment for what that means for numbers and strings.
func (c *schemaCtx) validateEnum(en, obj *Object) bool {
	var it Iter
	for {
		elt := Iterate(en, &it, true)
		if elt == nil {
			break
		}
		if Compare(elt, obj) == 0 {
			return true
		}
	}
	c.errorf(SchemaConstraint, obj, "object is not one of enumerated patterns")
	return false
}

// typeIsAllowed mirrors ucl_schema_type_is_allowed.
//
// An absent "type" allows everything. An array of names allows the object if
// any one of them matches; only the single-string branch records an error, so
// after a failed array the message describes the last alternative tried. A
// "type" that is neither array nor string fails with no message at all, which
// is what the C does.
func (c *schemaCtx) typeIsAllowed(typ, obj *Object) bool {
	if typ == nil {
		return true
	}

	switch typ.Type() {
	case TypeArray:
		var it Iter
		for {
			elt := Iterate(typ, &it, true)
			if elt == nil {
				break
			}
			if c.typeIsAllowed(elt, obj) {
				return true
			}
		}
	case TypeString:
		typeStr := ToString(typ)
		t, ok := StringToType(typeStr)
		if !ok {
			c.errorf(SchemaInvalidSchema, typ, "Type attribute is invalid in schema")
			return false
		}
		if obj.Type() == t {
			return true
		}
		// Compatible pairs: "number" is TypeFloat, and both an integer and a
		// UCL time satisfy it. "integer" stays strict.
		if (obj.Type() == TypeTime || obj.Type() == TypeInt) && t == TypeFloat {
			return true
		}
		c.errorf(SchemaTypeMismatch, obj, "Invalid type of %s, expected %s",
			obj.TypeString(), schemaTypeName(t))
	}

	return false
}

// schemaTypeName names a type the way ucl_object_type_to_string does, which is
// what the "expected %s" half of a type-mismatch message uses. It differs from
// Type.String: TypeFloat is called "number", not "float".
func schemaTypeName(t Type) string {
	return (&Object{ObjType: t}).TypeString()
}

// validateObject mirrors ucl_schema_validate_object.
//
// The C walks the schema's keys in hash order and dispatches on each key name.
// The order of the branches below is irrelevant because a key can match only
// one of them; what does matter, and is preserved, is that the
// additionalProperties sweep and the "required" check run after the whole
// keyword loop.
func (c *schemaCtx) validateObject(schema, obj *Object) bool {
	var (
		additionalSchema *Object
		required         *Object
		allowAdditional  = true
		ret              = true
	)

	var it Iter
	for ret {
		elt := Iterate(schema, &it, true)
		if elt == nil {
			break
		}
		switch key := schemaKey(elt); {
		case key == "properties" && elt.Type() == TypeObject:
			var pit Iter
			for ret {
				prop := Iterate(elt, &pit, true)
				if prop == nil {
					break
				}
				// Declared properties absent from the data are "required"'s
				// business, not this loop's.
				if found := obj.Lookup(schemaKey(prop)); found != nil {
					ret = c.validate(prop, found, true)
				}
			}

		case key == "additionalProperties":
			switch elt.Type() {
			case TypeBoolean:
				if !ToBoolean(elt) {
					allowAdditional = false
				}
			case TypeObject:
				additionalSchema = elt
			default:
				c.errorf(SchemaInvalidSchema, elt,
					"additionalProperties attribute is invalid in schema")
				ret = false
			}

		case key == "required":
			if elt.Type() == TypeArray {
				required = elt
			} else {
				c.errorf(SchemaInvalidSchema, elt,
					"required attribute is invalid in schema")
				ret = false
			}

		case key == "minProperties":
			// A non-numeric minProperties is silently ignored rather than
			// reported as a bad schema, matching ucl_object_toint_safe
			// guarding the whole branch in the C.
			if minmax, ok := ToIntSafe(elt); ok && int64(obj.length) < minmax {
				c.errorf(SchemaConstraint, obj,
					"object has not enough properties: %d, minimum is: %d",
					obj.length, uint32(minmax))
				ret = false
			}

		case key == "maxProperties":
			if minmax, ok := ToIntSafe(elt); ok && int64(obj.length) > minmax {
				c.errorf(SchemaConstraint, obj,
					"object has too many properties: %d, maximum is: %d",
					obj.length, uint32(minmax))
				ret = false
			}

		case key == "patternProperties":
			// Every declared pattern is tried against every property key;
			// patterns are independent, so a key matching two of them must
			// satisfy both sub-schemas.
			var pit Iter
			for ret {
				prop := Iterate(elt, &pit, true)
				if prop == nil {
					break
				}
				re, err := compilePattern(schemaKey(prop))
				if err != nil {
					// Divergence: ucl_schema_test_pattern swallows a regcomp
					// failure and reports "no match", so an uncompilable
					// patternProperties key silently constrains nothing --
					// and, with additionalProperties:false, silently rejects
					// everything. Report it the same way "pattern" does.
					c.errorf(SchemaInvalidSchema, prop, "cannot compile pattern %s",
						schemaKey(prop))
					ret = false
					break
				}
				var vit Iter
				for ret {
					vobj := Iterate(obj, &vit, true)
					if vobj == nil {
						break
					}
					if re.Match(cStr(vobj.Key())) {
						ret = c.validate(prop, vobj, true)
					}
				}
			}

		case key == "dependencies" && elt.Type() == TypeObject:
			ret = c.validateDependencies(elt, obj)
		}
	}

	if ret {
		if !allowAdditional || additionalSchema != nil {
			ret = c.validateAdditionalProperties(schema, obj, allowAdditional, additionalSchema)
		}
		// Not guarded by ret: the C runs the required sweep even after the
		// additional-properties sweep failed, so a missing required property
		// overwrites the message (never the false result).
		if required != nil {
			var rit Iter
			for {
				elt := Iterate(required, &rit, true)
				if elt == nil {
					break
				}
				if obj.Lookup(ToString(elt)) == nil {
					c.errorf(SchemaMissingProperty, obj,
						"object has missing property %s", ToString(elt))
					ret = false
					break
				}
			}
		}
	}

	return ret
}

// validateAdditionalProperties is the post-loop sweep at the bottom of
// ucl_schema_validate_object: every property not covered by "properties" or by
// some "patternProperties" pattern is either denied outright or validated
// against the additionalProperties sub-schema.
func (c *schemaCtx) validateAdditionalProperties(schema, obj *Object, allowAdditional bool, additionalSchema *Object) bool {
	props := schema.Lookup("properties")
	pat := schema.Lookup("patternProperties")

	it := IterateNew(obj)
	for {
		elt := IterateSafe(it, true)
		if elt == nil {
			break
		}
		found := props.Lookup(schemaKey(elt)) != nil
		if !found && pat != nil {
			pit := IterateNew(pat)
			for {
				pelt := IterateSafe(pit, true)
				if pelt == nil {
					break
				}
				// Divergence: the C calls ucl_schema_test_pattern(obj, ...,
				// recursive=true) here, which asks whether *any* key of the
				// object matches the pattern rather than whether this one
				// does -- so one matching key exempts every other key from
				// the additionalProperties check. A compile failure is
				// likewise swallowed there; here it is reported, as in the
				// patternProperties loop above.
				re, err := compilePattern(schemaKey(pelt))
				if err != nil {
					c.errorf(SchemaInvalidSchema, pelt, "cannot compile pattern %s",
						schemaKey(pelt))
					return false
				}
				if re.Match(cStr(elt.Key())) {
					found = true
					break
				}
			}
		}
		if !found {
			if !allowAdditional {
				c.errorf(SchemaConstraint, obj, "object has non-allowed property %s",
					schemaKey(elt))
				return false
			}
			if additionalSchema != nil {
				if !c.validate(additionalSchema, elt, true) {
					return false
				}
			}
		}
	}
	return true
}

// validateDependencies mirrors ucl_schema_validate_dependencies.
//
// A dependency entry is only acted on when the data actually has the key it
// hangs off. An array entry names properties that must then also be present; an
// object entry is a schema the *whole* object must satisfy. Any other type is
// silently ignored, which draft-4 would reject.
func (c *schemaCtx) validateDependencies(deps, obj *Object) bool {
	ret := true
	var it Iter
	for ret {
		cur := Iterate(deps, &it, true)
		if cur == nil {
			break
		}
		elt := obj.Lookup(schemaKey(cur))
		if elt == nil {
			continue
		}
		switch cur.Type() {
		case TypeArray:
			var pit Iter
			for ret {
				curDep := Iterate(cur, &pit, true)
				if curDep == nil {
					break
				}
				if obj.Lookup(ToString(curDep)) == nil {
					c.errorf(SchemaMissingDependency, elt,
						"dependency %s is missing for key %s",
						ToString(curDep), schemaKey(cur))
					ret = false
				}
			}
		case TypeObject:
			ret = c.validate(cur, obj, true)
		}
	}
	return ret
}

// validateNumber mirrors ucl_schema_validate_number.
//
// Every comparison happens in float64, as the C's ucl_object_todouble does, so
// int64 values above 2^53 lose precision here exactly as they do there. A
// non-numeric multipleOf/maximum/minimum is silently ignored.
func (c *schemaCtx) validateNumber(schema, obj *Object) bool {
	const alpha = 1e-16

	var it Iter
	for {
		elt := Iterate(schema, &it, true)
		if elt == nil {
			break
		}
		if elt.Type() != TypeFloat && elt.Type() != TypeInt {
			continue
		}
		switch schemaKey(elt) {
		case "multipleOf":
			constraint := ToDouble(elt)
			if constraint <= 0 {
				c.errorf(SchemaInvalidSchema, elt, "multipleOf must be greater than zero")
				return false
			}
			val := ToDouble(obj)
			// C99 remainder, not fmod: the result lands in
			// [-constraint/2, constraint/2].
			rem := math.Remainder(val, constraint)
			if math.Abs(rem) > alpha {
				c.errorf(SchemaConstraint, obj,
					"number %.4f is not multiple of %.4f, remainder is %.7f",
					val, constraint, rem)
				return false
			}

		case "maximum":
			constraint := ToDouble(elt)
			// Divergence: the C hoists `exclusive` out of the loop and only
			// ever assigns it, so an exclusiveMaximum:true leaks into a
			// "minimum" processed later in hash order and silently makes it
			// exclusive too. Each bound reads its own flag here.
			exclusive := false
			if test := schema.Lookup("exclusiveMaximum"); test != nil && test.Type() == TypeBoolean {
				exclusive = ToBoolean(test)
			}
			val := ToDouble(obj)
			if val > constraint || (exclusive && val >= constraint) {
				c.errorf(SchemaConstraint, obj,
					"number is too big: %.3f, maximum is: %.3f", val, constraint)
				return false
			}

		case "minimum":
			constraint := ToDouble(elt)
			exclusive := false
			if test := schema.Lookup("exclusiveMinimum"); test != nil && test.Type() == TypeBoolean {
				exclusive = ToBoolean(test)
			}
			val := ToDouble(obj)
			if val < constraint || (exclusive && val <= constraint) {
				c.errorf(SchemaConstraint, obj,
					"number is too small: %.3f, minimum is: %.3f", val, constraint)
				return false
			}
		}
	}
	return true
}

// validateString mirrors ucl_schema_validate_string.
//
// maxLength/minLength are honoured only for an integer constraint -- unlike the
// numeric keywords there is no float or time coercion here -- and they count
// bytes, not runes, because the C compares against obj->len.
func (c *schemaCtx) validateString(schema, obj *Object) bool {
	var it Iter
	for {
		elt := Iterate(schema, &it, true)
		if elt == nil {
			break
		}
		key := schemaKey(elt)
		switch {
		case elt.Type() == TypeInt && key == "maxLength":
			constraint := ToInt(elt)
			if int64(obj.length) > constraint {
				c.errorf(SchemaConstraint, obj, "string is too big: %d, maximum is: %d",
					obj.length, constraint)
				return false
			}

		case elt.Type() == TypeInt && key == "minLength":
			constraint := ToInt(elt)
			if int64(obj.length) < constraint {
				c.errorf(SchemaConstraint, obj, "string is too short: %d, minimum is: %d",
					obj.length, constraint)
				return false
			}

		case elt.Type() == TypeString && key == "pattern":
			pattern := ToString(elt)
			re, err := compilePattern(pattern)
			if err != nil {
				c.errorf(SchemaInvalidSchema, elt, "cannot compile pattern %s", pattern)
				return false
			}
			// regexec is handed a NUL-terminated string, so matching stops at
			// an embedded NUL.
			if !re.MatchString(ToString(obj)) {
				c.errorf(SchemaConstraint, obj, "string doesn't match regexp %s", pattern)
				return false
			}
		}
	}
	return true
}

// validateArray mirrors ucl_schema_validate_array.
func (c *schemaCtx) validateArray(schema, obj *Object) bool {
	var (
		additionalSchema *Object
		allowAdditional  = true
		needUnique       = false
		haveUnvalidated  = false
		ret              = true
		idx              uint32
	)

	var it Iter
	for ret {
		elt := Iterate(schema, &it, true)
		if elt == nil {
			break
		}
		switch key := schemaKey(elt); {
		case key == "items":
			switch elt.Type() {
			case TypeArray:
				// Tuple form: schema entries and data elements advance in
				// lockstep. Schema entries past the end of the data impose no
				// constraint; data past the end of the schema is what
				// additionalItems then governs.
				found := ArrayHead(obj)
				var pit Iter
				for ret {
					item := Iterate(elt, &pit, true)
					if item == nil {
						break
					}
					if found != nil {
						ret = c.validate(item, found, false)
						idx++
						found = ArrayFindIndex(obj, idx)
					}
				}
				if found != nil {
					haveUnvalidated = true
				}
			case TypeObject:
				// Single schema for every element, regardless of
				// additionalItems.
				var pit Iter
				for ret {
					item := Iterate(obj, &pit, true)
					if item == nil {
						break
					}
					ret = c.validate(elt, item, false)
				}
			default:
				c.errorf(SchemaInvalidSchema, elt, "items attribute is invalid in schema")
				ret = false
			}

		case key == "additionalItems":
			switch elt.Type() {
			case TypeBoolean:
				if !ToBoolean(elt) {
					allowAdditional = false
				}
			case TypeObject:
				additionalSchema = elt
			default:
				c.errorf(SchemaInvalidSchema, elt,
					"additionalItems attribute is invalid in schema")
				ret = false
			}

		case key == "uniqueItems" && elt.Type() == TypeBoolean:
			needUnique = ToBoolean(elt)

		case key == "minItems":
			if minmax, ok := ToIntSafe(elt); ok && int64(obj.length) < minmax {
				c.errorf(SchemaConstraint, obj,
					"array has not enough items: %d, minimum is: %d",
					obj.length, uint32(minmax))
				ret = false
			}

		case key == "maxItems":
			if minmax, ok := ToIntSafe(elt); ok && int64(obj.length) > minmax {
				c.errorf(SchemaConstraint, obj,
					"array has too many items: %d, maximum is: %d",
					obj.length, uint32(minmax))
				ret = false
			}
		}
	}

	if ret {
		// additionalItems only bites alongside tuple-form items: nothing else
		// leaves elements unvalidated.
		if (!allowAdditional || additionalSchema != nil) && haveUnvalidated {
			if !allowAdditional {
				c.errorf(SchemaConstraint, obj, "array has undefined item")
				ret = false
			} else if additionalSchema != nil {
				// The C's loop re-validates the first additional item because
				// of a post-increment used in the re-lookup; validation has no
				// side effects, so validating each item exactly once here is
				// observably identical.
				for i := idx; ; i++ {
					elt := ArrayFindIndex(obj, i)
					if elt == nil {
						break
					}
					if !c.validate(additionalSchema, elt, false) {
						ret = false
						break
					}
				}
			}
		}
		if ret && needUnique {
			ret = c.arrayIsUnique(obj)
		}
	}

	return ret
}

// arrayIsUnique mirrors ucl_schema_array_is_unique.
//
// The C inserts each element into a binary search tree ordered by
// ucl_object_compare. Compare is not a total order (see its doc comment), so a
// tree walk can miss a duplicate that a direct comparison finds; this does a
// pairwise scan instead, which reports the same later-of-the-pair element as
// the C does for every case the corpus covers and cannot miss a collision.
func (c *schemaCtx) arrayIsUnique(obj *Object) bool {
	var seen []*Object
	var it Iter
	for {
		elt := Iterate(obj, &it, true)
		if elt == nil {
			break
		}
		for _, prev := range seen {
			if Compare(prev, elt) == 0 {
				c.errorf(SchemaConstraint, elt,
					"duplicate values detected while uniqueItems is true")
				return false
			}
		}
		seen = append(seen, elt)
	}
	return true
}

// --- $ref resolution -------------------------------------------------------

// resolveRef mirrors ucl_schema_resolve_ref. It returns the referenced schema
// and the root that further refs found inside it resolve against, or nil after
// recording an error.
//
// The fragment is walked as a sequence of raw byte spans: there is deliberately
// no RFC 6901 ~0/~1 unescaping and no percent-decoding, because libucl has
// none and tests/schema/ref.json's "escaped pointer ref" suite depends on the
// resulting lookups failing.
func (c *schemaCtx) resolveRef(root *Object, ref string) (target, nroot *Object) {
	var extObj *Object
	var frag string

	if len(ref) == 0 || ref[0] != '#' {
		// An external document, optionally with a fragment. The C splits on
		// the *last* '#'.
		hash := strings.LastIndexByte(ref, '#')
		url := ref
		if hash >= 0 {
			url = ref[:hash]
		}

		extObj = c.extRefs.Lookup(url)
		if extObj == nil {
			data, err := c.fetcher.FetchRef(url)
			if err != nil {
				c.errorf(SchemaInvalidSchema, root, "cannot fetch reference %s: %s", url, err)
				return nil, nil
			}
			parser := NewParser(0)
			if !parser.AddChunk(data) {
				c.errorf(SchemaInvalidSchema, root, "cannot fetch reference %s: %s",
					url, parser.ErrorString())
				return nil, nil
			}
			extObj = parser.GetObject()
			// Cached under the raw pre-'#' substring, with no normalisation.
			c.extRefs.InsertKey(extObj, []byte(url), true)
		}

		if hash >= 0 {
			frag = ref[hash+1:]
		}
	} else {
		frag = ref[1:]
	}

	res := root
	if extObj != nil {
		res = extObj
	}
	nroot = res

	switch {
	case len(frag) == 0:
		// "#" alone, or an external document with no fragment: the whole
		// document is the target. Note the C returns here without the
		// is-it-an-object check applied below.
		return res, nroot
	case frag[0] == '/':
		frag = frag[1:]
	}

	// Components are byte spans of the fragment; rest is what the C's error
	// messages print, which is the tail of the ref from the component's start.
	rest := frag
	for {
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			break
		}
		if slash == 0 {
			c.errorf(SchemaInvalidSchema, res,
				"reference %s is invalid, empty path component", ref)
			return nil, nil
		}
		res = c.resolveRefComponent(res, rest[:slash], rest)
		if res == nil {
			return nil, nil
		}
		rest = rest[slash+1:]
	}
	if len(rest) != 0 {
		res = c.resolveRefComponent(res, rest, rest)
	}

	if res == nil || res.Type() != TypeObject {
		c.errorf(SchemaInvalidSchema, res,
			"reference %s is invalid, cannot find specified object", ref)
		return nil, nil
	}
	return res, nroot
}

// resolveRefComponent mirrors ucl_schema_resolve_ref_component. comp is the
// component to resolve; rest is the remainder of the fragment starting at comp,
// which is what the C prints in its messages.
func (c *schemaCtx) resolveRefComponent(cur *Object, comp, rest string) *Object {
	switch cur.Type() {
	case TypeObject:
		// An exact, literal, byte-for-byte key lookup.
		res := cur.LookupLen([]byte(comp))
		if res == nil {
			c.errorf(SchemaInvalidSchema, cur,
				"reference %s is invalid, missing path component", rest)
			return nil
		}
		return res

	case TypeArray:
		num, ok := parseRefIndex(comp)
		if !ok {
			c.errorf(SchemaInvalidSchema, cur,
				"reference %s is invalid, invalid item number", rest)
			return nil
		}
		// Divergence: the C's walk never increments its counter, so only
		// index 0 ever resolves and every other index reports "does not
		// exist". ref.json only uses "#/items/0", so the corpus does not
		// notice. Index properly.
		res := ArrayFindIndex(cur, uint32(num))
		if res == nil {
			c.errorf(SchemaInvalidSchema, cur,
				"reference %s is invalid, item number %d does not exist", rest, num)
			return nil
		}
		return res

	default:
		c.errorf(SchemaInvalidSchema, nil,
			"reference %s is invalid, contains primitive object in the path", rest)
		return nil
	}
}

// parseRefIndex reads an array index out of a pointer component the way the C's
// strtoul plus its "trailing character is neither '/' nor NUL" check does:
// every byte of the component must be a digit.
func parseRefIndex(comp string) (int, bool) {
	if comp == "" {
		return 0, true
	}
	num := 0
	for i := 0; i < len(comp); i++ {
		ch := comp[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		num = num*10 + int(ch-'0')
		if num > 1<<30 {
			// Well past any real array; keep it away from overflow.
			return num, true
		}
	}
	return num, true
}

// --- regexp ---------------------------------------------------------------

// patternCache memoises compiled patterns. The C recompiles on every call;
// caching is a pure optimisation because compilation is deterministic, and
// failures are cached too so the same schema keeps reporting the same error.
var patternCache sync.Map // string -> patternEntry

// patternCacheMax bounds that cache. The keys are pattern strings taken
// straight from the schema, so a process validating untrusted schemas would
// otherwise retain a compiled regexp for every distinct pattern it ever saw,
// for the life of the process. Past the limit compilation still succeeds, it
// just stops being memoised.
const patternCacheMax = 1024

var patternCacheLen atomic.Int64

type patternEntry struct {
	re  *regexp.Regexp
	err error
}

// compilePattern compiles a schema pattern the way regcomp(REG_EXTENDED |
// REG_NOSUB) does.
//
// regexp.CompilePOSIX is used rather than regexp.Compile because POSIX ERE is
// what libucl asks for: Perl-only syntax (\d, \w, \b, (?:...), non-greedy
// quantifiers, lookaround) is rejected here, just as it is not part of ERE.
// Under glibc such a pattern would compile with a *different* meaning -- \d
// matches a literal "d" -- so failing loudly beats matching differently.
//
// Everything the upstream corpus uses is common to both flavours. The
// constructs POSIX ERE has and RE2 does not are collating symbols [.x.] and
// equivalence classes [=x=], which glibc also handles only with full locale
// collation data; both implementations refuse them in practice. Matching is a
// search: the pattern is unanchored and case-sensitive unless it says
// otherwise, which is REG_NOSUB regexec's behaviour too.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := patternCache.Load(pattern); ok {
		e := v.(patternEntry)
		return e.re, e.err
	}
	re, err := regexp.CompilePOSIX(pattern)
	if patternCacheLen.Load() < patternCacheMax {
		if _, loaded := patternCache.LoadOrStore(pattern, patternEntry{re: re, err: err}); !loaded {
			patternCacheLen.Add(1)
		}
	}
	return re, err
}
