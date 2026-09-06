package goucl

import (
	"encoding"
	"reflect"
	"time"
)

// The reflect types the encoder and decoder compare against, by Type equality
// or Implements rather than by name, computed once.
//
// They live here rather than beside either user because both need
// durationType: a Duration is the one Go type that maps to a UCL kind
// (KindTime) rather than to a Go kind, so both directions have to recognise
// it. Declaring it twice does not compile, and picking one file to hold it
// would leave the other reaching across for no stated reason.
var (
	durationType        = reflect.TypeOf(time.Duration(0))
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)
