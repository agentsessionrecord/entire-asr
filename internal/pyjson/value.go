// Package pyjson holds JSON values the way the ASR Python reference reads and
// writes them: objects keep key order, integers stay exact, other numbers keep
// Python's Decimal text, and strings use Python's escaping rules.
package pyjson

import (
	"math/big"
	"strconv"
	"strings"
)

// MaxInt is the largest integer ASR writes as a JSON number.
const MaxInt = 1<<53 - 1

// Object is a JSON object that keeps the order in which keys were added.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Obj builds an object from alternating keys and values.
func Obj(kv ...any) *Object {
	o := NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Len returns the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Keys returns the keys in order. The slice must not be changed.
func (o *Object) Keys() []string { return o.keys }

// Get returns a value and whether the key is present.
func (o *Object) Get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// Value returns a value, or nil when the key is absent.
func (o *Object) Value(k string) any { return o.vals[k] }

// Has reports whether a key is present.
func (o *Object) Has(k string) bool {
	_, ok := o.vals[k]
	return ok
}

// Set adds a key at the end, or replaces the value of an existing key in place.
func (o *Object) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Delete removes a key.
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Copy returns a shallow copy.
func (o *Object) Copy() *Object {
	c := &Object{keys: append([]string(nil), o.keys...), vals: make(map[string]any, len(o.vals))}
	for k, v := range o.vals {
		c.vals[k] = v
	}
	return c
}

// Without returns a shallow copy without the named keys.
func (o *Object) Without(names ...string) *Object {
	c := NewObject()
	for _, k := range o.keys {
		skip := false
		for _, n := range names {
			if k == n {
				skip = true
				break
			}
		}
		if !skip {
			c.Set(k, o.vals[k])
		}
	}
	return c
}

// Update sets every key of other in order, as Python's dict.update does.
func (o *Object) Update(other *Object) {
	for _, k := range other.keys {
		o.Set(k, other.vals[k])
	}
}

// Decimal is a JSON number with a fraction or an exponent. Text is the value
// as Python's str(Decimal(source)) prints it.
type Decimal struct{ Text string }

// Constant is NaN, Infinity or -Infinity, which only lenient parsing accepts.
type Constant struct{ Name string }

// Str returns a string value and whether v is a string.
func Str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// NonEmptyStr returns a string value and whether v is a non-empty string.
func NonEmptyStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

// AsObject returns v as an object, or nil.
func AsObject(v any) *Object {
	o, _ := v.(*Object)
	return o
}

// IsList reports whether v is a JSON array.
func IsList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// Int returns an exact integer (not a boolean) and whether v is one.
func Int(v any) (*big.Int, bool) {
	switch x := v.(type) {
	case int64:
		return big.NewInt(x), true
	case int:
		return big.NewInt(int64(x)), true
	case *big.Int:
		return x, true
	}
	return nil, false
}

// Count reports whether v is an integer from 0 to MaxInt, as importer-core's count does.
func Count(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, x >= 0 && x <= MaxInt
	case int:
		return int64(x), x >= 0 && x <= MaxInt
	}
	return 0, false
}

// Truthy reports Python truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case Decimal:
		c, _ := numCanon(x)
		return c != "0"
	case Constant:
		return true
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *Object:
		return x.Len() > 0
	}
	return true
}

// Equal reports Python equality of two parsed JSON values. Booleans, integers
// and decimals compare by numeric value, as they do in Python.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.vals[k]
			if !ok || !Equal(x.vals[k], yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case Constant:
		return false
	}
	ca, ok1 := numCanon(a)
	cb, ok2 := numCanon(b)
	return ok1 && ok2 && ca == cb
}

// numCanon returns one spelling per numeric value: coefficient digits without
// trailing zeros, then the exponent. Zero is "0".
func numCanon(v any) (string, bool) {
	var neg bool
	var digits string
	var exp int64
	switch x := v.(type) {
	case bool:
		if x {
			return "1e0", true
		}
		return "0", true
	case int64:
		if x < 0 {
			neg = true
			digits = strconv.FormatUint(uint64(-(x+1))+1, 10)
		} else {
			digits = strconv.FormatInt(x, 10)
		}
	case int:
		return numCanon(int64(x))
	case *big.Int:
		neg = x.Sign() < 0
		digits = new(big.Int).Abs(x).String()
	case Decimal:
		d, ok := parseDecimal(x.Text)
		if !ok {
			return "", false
		}
		neg, digits, exp = d.neg, d.coef, d.exp
	default:
		return "", false
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", true
	}
	trimmed := strings.TrimRight(digits, "0")
	exp += int64(len(digits) - len(trimmed))
	sign := ""
	if neg {
		sign = "-"
	}
	return sign + trimmed + "e" + strconv.FormatInt(exp, 10), true
}

// Native copies a value for ASR output: decimals become their text and
// integers beyond MaxInt become strings, as importer-core's native does.
func Native(v any) any {
	switch x := v.(type) {
	case Decimal:
		return x.Text
	case Constant:
		return x.Name
	case int64:
		if x > MaxInt || x < -MaxInt {
			return strconv.FormatInt(x, 10)
		}
		return x
	case int:
		return Native(int64(x))
	case *big.Int:
		if x.IsInt64() {
			return Native(x.Int64())
		}
		return x.String()
	case *Object:
		c := &Object{keys: append([]string(nil), x.keys...), vals: make(map[string]any, len(x.vals))}
		for k, item := range x.vals {
			c.vals[k] = Native(item)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, item := range x {
			c[i] = Native(item)
		}
		return c
	}
	return v
}

// HashKey returns a key that matches when Python dict keys match. Lists and
// objects are unhashable, as in Python.
func HashKey(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "N", nil
	case string:
		return "s" + x, nil
	case []any:
		return "", &TypeError{"unhashable type: 'list'"}
	case *Object:
		return "", &TypeError{"unhashable type: 'dict'"}
	case Constant:
		return "c" + x.Name, nil
	}
	c, ok := numCanon(v)
	if !ok {
		return "", &TypeError{"unhashable type"}
	}
	return "n" + c, nil
}

// TypeError mirrors a Python TypeError.
type TypeError struct{ Msg string }

func (e *TypeError) Error() string { return e.Msg }

// ValueError mirrors a Python ValueError.
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }
