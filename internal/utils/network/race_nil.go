package network

import "reflect"

// isNilValue is the reflective half of isNil, kept separate so the fast path
// above does not import reflect's cost into every successful dial.
//
// It exists for one shape: a dialler that returns a typed nil pointer with a
// nil error. That is a misbehaving dialler, and it is worth surviving rather
// than crashing on, because the caller of a race is a reconnect loop and the
// failure would be a panic in the middle of a recovery.
func isNilValue(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return rv.IsNil()
	}
	return false
}
