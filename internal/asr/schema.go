package asr

import (
	_ "embed"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/dlclark/regexp2"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// The ASR 0.1 record schema, from schema/v0 in agentsessionrecord/asr.
//
//go:embed schema/agent-session.schema.json
var recordSchema []byte

// The reference writer validates each record with Python's jsonschema and
// reports the instance path of the first error. This validator walks the
// schema in the same order, keyword by keyword as they appear in the schema
// document, so that it rejects the same records and names the same path.

var (
	schemaOnce sync.Once
	schemaRoot *pyjson.Object
	schemaErr  error
	patterns   sync.Map
)

// pySpace is the body of a character class for Python's \s.
const pySpace = `\t\n\v\f\r\x1c-\x20\x85\xa0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000`

// pythonPattern rewrites \s and \S to Python's whitespace set, so that
// patterns match as Python's re.search does.
func pythonPattern(p string) (string, error) {
	p = strings.ReplaceAll(p, `[\s\S]`, `(?s:.)`)
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			next := p[i+1]
			i++
			switch {
			case next == 's' && inClass:
				b.WriteString(pySpace)
			case next == 's':
				b.WriteString("[" + pySpace + "]")
			case next == 'S' && !inClass:
				b.WriteString("[^" + pySpace + "]")
			case next == 'S':
				return "", fmt.Errorf("unsupported pattern %q", p)
			default:
				b.WriteByte(c)
				b.WriteByte(next)
			}
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			if i+1 < len(p) && p[i+1] == '^' {
				b.WriteByte('^')
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				b.WriteByte(']')
				i++
			}
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

func pattern(src string) (*regexp2.Regexp, error) {
	if re, ok := patterns.Load(src); ok {
		return re.(*regexp2.Regexp), nil
	}
	translated, err := pythonPattern(src)
	if err != nil {
		return nil, err
	}
	re, err := regexp2.Compile(translated, regexp2.None)
	if err != nil {
		return nil, err
	}
	patterns.Store(src, re)
	return re, nil
}

// base64Format is the reference writer's asr-base64 check: standard
// alphabet, a length divisible by four and at most two padding characters.
func base64Format(s string) bool {
	if pyjson.RuneLen(s)%4 != 0 {
		return false
	}
	r := pyjson.Runes(s)
	e := len(r)
	for k := 0; k < 2 && e > 0 && r[e-1] == '='; k++ {
		e--
	}
	for _, c := range r[:e] {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return true
}

// dateTimeFormat is jsonschema's date-time check, which upper-cases the
// value before rfc3339-validator reads it.
func dateTimeFormat(s string) bool {
	return ValidateRFC3339(strings.ToUpper(s))
}

func loadSchema() (*pyjson.Object, error) {
	schemaOnce.Do(func() {
		doc, err := pyjson.Parse(string(recordSchema), true)
		if err != nil {
			schemaErr = err
			return
		}
		schemaRoot = doc.(*pyjson.Object)
		schemaErr = checkKeywords(schemaRoot)
	})
	return schemaRoot, schemaErr
}

var ignored = map[string]bool{"$schema": true, "$id": true, "$defs": true, "title": true, "description": true,
	"x-asr": true, "contentEncoding": true, "contentMediaType": true, "then": true, "else": true, "examples": true}

var applied = map[string]bool{"$ref": true, "allOf": true, "anyOf": true, "not": true, "if": true, "properties": true,
	"items": true, "propertyNames": true, "type": true, "required": true, "const": true, "enum": true, "pattern": true,
	"minLength": true, "maxLength": true, "minimum": true, "format": true}

// checkKeywords refuses a schema with a keyword this validator does not apply.
func checkKeywords(schema any) error {
	switch s := schema.(type) {
	case bool:
		return nil
	case *pyjson.Object:
		for _, k := range s.Keys() {
			v := s.Value(k)
			switch {
			case k == "$defs" || k == "properties":
				for _, name := range pyjson.AsObject(v).Keys() {
					if err := checkKeywords(pyjson.AsObject(v).Value(name)); err != nil {
						return err
					}
				}
			case k == "allOf" || k == "anyOf":
				for _, sub := range v.([]any) {
					if err := checkKeywords(sub); err != nil {
						return err
					}
				}
			case k == "not" || k == "if" || k == "then" || k == "else" || k == "items" || k == "propertyNames":
				if err := checkKeywords(v); err != nil {
					return err
				}
			case k == "pattern":
				if _, err := pattern(v.(string)); err != nil {
					return err
				}
			case k == "format":
				if f := v.(string); f != "date-time" && f != "asr-base64" {
					return fmt.Errorf("unsupported format %q", f)
				}
			case ignored[k] || applied[k]:
			default:
				return fmt.Errorf("unsupported schema keyword %q", k)
			}
		}
		return nil
	}
	return fmt.Errorf("unsupported schema %T", schema)
}

type validator struct{ root *pyjson.Object }

func (v *validator) resolve(ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported reference %q", ref)
	}
	var node any = v.root
	for _, token := range strings.Split(ref[2:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		obj := pyjson.AsObject(node)
		if obj == nil || !obj.Has(token) {
			return nil, fmt.Errorf("unresolvable reference %q", ref)
		}
		node = obj.Value(token)
	}
	return node, nil
}

func isType(instance any, name string) bool {
	switch name {
	case "object":
		_, ok := instance.(*pyjson.Object)
		return ok
	case "array":
		_, ok := instance.([]any)
		return ok
	case "string":
		_, ok := instance.(string)
		return ok
	case "boolean":
		_, ok := instance.(bool)
		return ok
	case "null":
		return instance == nil
	case "integer":
		_, ok := pyjson.Int(instance)
		return ok
	case "number":
		_, ok := pyjson.Int(instance)
		_, dec := instance.(pyjson.Decimal)
		return ok || dec
	}
	return false
}

// equal is jsonschema's equality: booleans never equal numbers.
func equal(a, b any) bool {
	_, ab := a.(bool)
	_, bb := b.(bool)
	if ab || bb {
		return ab && bb && a == b
	}
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case *pyjson.Object:
		y, ok := b.(*pyjson.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.Keys() {
			if !y.Has(k) || !equal(x.Value(k), y.Value(k)) {
				return false
			}
		}
		return true
	}
	return pyjson.Equal(a, b)
}

func lessThan(a, b any) bool {
	x, ok1 := pyjson.Int(a)
	y, ok2 := pyjson.Int(b)
	if ok1 && ok2 {
		return x.Cmp(y) < 0
	}
	fx, _ := new(big.Float).SetString(numText(a))
	fy, _ := new(big.Float).SetString(numText(b))
	return fx != nil && fy != nil && fx.Cmp(fy) < 0
}

func numText(v any) string {
	switch x := v.(type) {
	case pyjson.Decimal:
		return x.Text
	case int64:
		return strconv.FormatInt(x, 10)
	case *big.Int:
		return x.String()
	}
	return "nan"
}

// first returns the instance path of the first error, or nil when the
// instance is valid. A schema problem is returned as an error.
func (v *validator) first(instance, schema any, path []string) ([]string, bool, error) {
	if b, ok := schema.(bool); ok {
		if b {
			return nil, false, nil
		}
		return path, true, nil
	}
	s := schema.(*pyjson.Object)
	for _, k := range s.Keys() {
		value := s.Value(k)
		fail := false
		switch k {
		case "$ref":
			target, err := v.resolve(value.(string))
			if err != nil {
				return nil, false, err
			}
			if p, bad, err := v.first(instance, target, path); bad || err != nil {
				return p, bad, err
			}
		case "allOf":
			for _, sub := range value.([]any) {
				if p, bad, err := v.first(instance, sub, path); bad || err != nil {
					return p, bad, err
				}
			}
		case "anyOf":
			fail = true
			for _, sub := range value.([]any) {
				_, bad, err := v.first(instance, sub, path)
				if err != nil {
					return nil, false, err
				}
				if !bad {
					fail = false
					break
				}
			}
		case "not":
			_, bad, err := v.first(instance, value, path)
			if err != nil {
				return nil, false, err
			}
			fail = !bad
		case "if":
			_, bad, err := v.first(instance, value, path)
			if err != nil {
				return nil, false, err
			}
			branch := "then"
			if bad {
				branch = "else"
			}
			if sub, ok := s.Get(branch); ok {
				if p, bad, err := v.first(instance, sub, path); bad || err != nil {
					return p, bad, err
				}
			}
		case "properties":
			obj, ok := instance.(*pyjson.Object)
			if !ok {
				break
			}
			props := pyjson.AsObject(value)
			for _, name := range props.Keys() {
				if !obj.Has(name) {
					continue
				}
				if p, bad, err := v.first(obj.Value(name), props.Value(name), append(path[:len(path):len(path)], name)); bad || err != nil {
					return p, bad, err
				}
			}
		case "items":
			list, ok := instance.([]any)
			if !ok {
				break
			}
			for i, item := range list {
				if p, bad, err := v.first(item, value, append(path[:len(path):len(path)], strconv.Itoa(i))); bad || err != nil {
					return p, bad, err
				}
			}
		case "propertyNames":
			obj, ok := instance.(*pyjson.Object)
			if !ok {
				break
			}
			for _, name := range obj.Keys() {
				if p, bad, err := v.first(name, value, path); bad || err != nil {
					return p, bad, err
				}
			}
		case "type":
			fail = true
			switch t := value.(type) {
			case string:
				fail = !isType(instance, t)
			case []any:
				for _, name := range t {
					if isType(instance, name.(string)) {
						fail = false
					}
				}
			}
		case "required":
			if obj, ok := instance.(*pyjson.Object); ok {
				for _, name := range value.([]any) {
					if !obj.Has(name.(string)) {
						fail = true
						break
					}
				}
			}
		case "const":
			fail = !equal(instance, value)
		case "enum":
			fail = true
			for _, option := range value.([]any) {
				if equal(instance, option) {
					fail = false
				}
			}
		case "pattern":
			if str, ok := instance.(string); ok {
				re, err := pattern(value.(string))
				if err != nil {
					return nil, false, err
				}
				found, err := re.MatchString(str)
				if err != nil {
					return nil, false, err
				}
				fail = !found
			}
		case "minLength", "maxLength":
			if str, ok := instance.(string); ok {
				limit, _ := pyjson.Int(value)
				n := big.NewInt(int64(pyjson.RuneLen(str)))
				if k == "minLength" {
					fail = n.Cmp(limit) < 0
				} else {
					fail = n.Cmp(limit) > 0
				}
			}
		case "minimum":
			if isType(instance, "number") {
				fail = lessThan(instance, value)
			}
		case "format":
			if str, ok := instance.(string); ok {
				if value == "date-time" {
					fail = !dateTimeFormat(str)
				} else {
					fail = !base64Format(str)
				}
			}
		}
		if fail {
			return path, true, nil
		}
	}
	return nil, false, nil
}

// CheckSchema validates one record as the reference writer does.
func CheckSchema(record *pyjson.Object) error {
	root, err := loadSchema()
	if err != nil {
		return err
	}
	path, bad, err := (&validator{root}).first(record, root, nil)
	if err != nil {
		return err
	}
	if bad {
		return &pyjson.ValueError{Msg: "Record fails schema at /" + strings.Join(path, "/")}
	}
	return nil
}
