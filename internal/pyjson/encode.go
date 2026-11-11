package pyjson

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Options select one of Python's json.dumps forms.
type Options struct {
	// EnsureASCII escapes every character outside printable ASCII.
	EnsureASCII bool
	// Compact uses "," and ":" separators. Otherwise ", " and ": ".
	Compact bool
	// SortKeys orders object keys by code point.
	SortKeys bool
	// DecimalAsString writes decimals as strings, as default=str does.
	DecimalAsString bool
	// Indent, when positive, pretty-prints with this many spaces.
	Indent int
}

// Dumps serializes a value as Python's json.dumps does with these options.
// Strings may hold lone surrogates. Use CheckUTF8 before writing bytes.
func Dumps(v any, o Options) (string, error) {
	var b strings.Builder
	if err := dump(&b, v, o, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

// MustDumps is Dumps for values known to be serializable.
func MustDumps(v any, o Options) string {
	s, err := Dumps(v, o)
	if err != nil {
		panic(err)
	}
	return s
}

func dump(b *strings.Builder, v any, o Options, level int) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case *big.Int:
		b.WriteString(x.String())
	case Decimal:
		if !o.DecimalAsString {
			return &TypeError{"Object of type Decimal is not JSON serializable"}
		}
		writeString(b, x.Text, o.EnsureASCII)
	case Constant:
		return &ValueError{"Out of range float values are not JSON compliant"}
	case string:
		writeString(b, x, o.EnsureASCII)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(itemSep(o))
			}
			newline(b, o, level+1)
			if err := dump(b, item, o, level+1); err != nil {
				return err
			}
		}
		newline(b, o, level)
		b.WriteByte(']')
	case *Object:
		if x.Len() == 0 {
			b.WriteString("{}")
			return nil
		}
		keys := x.keys
		if o.SortKeys {
			keys = append([]string(nil), keys...)
			sort.Strings(keys)
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(itemSep(o))
			}
			newline(b, o, level+1)
			writeString(b, k, o.EnsureASCII)
			if o.Compact {
				b.WriteByte(':')
			} else {
				b.WriteString(": ")
			}
			if err := dump(b, x.vals[k], o, level+1); err != nil {
				return err
			}
		}
		newline(b, o, level)
		b.WriteByte('}')
	default:
		return &TypeError{fmt.Sprintf("Object of type %T is not JSON serializable", v)}
	}
	return nil
}

func itemSep(o Options) string {
	if o.Compact || o.Indent > 0 {
		return ","
	}
	return ", "
}

func newline(b *strings.Builder, o Options, level int) {
	if o.Indent > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", o.Indent*level))
	}
}

const hexDigits = "0123456789abcdef"

func writeU(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[r>>12&0xf])
	b.WriteByte(hexDigits[r>>8&0xf])
	b.WriteByte(hexDigits[r>>4&0xf])
	b.WriteByte(hexDigits[r&0xf])
}

func writeString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			i++
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				if c < 0x20 || (ascii && c == 0x7f) {
					writeU(b, rune(c))
				} else {
					b.WriteByte(c)
				}
			}
			continue
		}
		r, n := DecodeRune(s[i:])
		if !ascii {
			b.WriteString(s[i : i+n])
		} else if r >= 0x10000 {
			r -= 0x10000
			writeU(b, 0xd800|(r>>10)&0x3ff)
			writeU(b, 0xdc00|r&0x3ff)
		} else {
			writeU(b, r)
		}
		i += n
	}
	b.WriteByte('"')
}

// CheckUTF8 fails as Python's str.encode("utf-8") does on a lone surrogate.
func CheckUTF8(s string) error {
	pos := 0
	for i := 0; i < len(s); {
		r, n := DecodeRune(s[i:])
		if r >= 0xd800 && r <= 0xdfff {
			return &ValueError{fmt.Sprintf("'utf-8' codec can't encode character '\\u%04x' in position %d: surrogates not allowed", r, pos)}
		}
		i += n
		pos++
	}
	return nil
}

// PyStr returns Python's str() of a parsed value.
func PyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return PyRepr(v)
}

// PyRepr returns Python's repr() of a parsed value.
func PyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case *big.Int:
		return x.String()
	case Decimal:
		return "Decimal('" + x.Text + "')"
	case Constant:
		return "Decimal('" + x.Name + "')"
	case string:
		return reprString(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = PyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		parts := make([]string, 0, x.Len())
		for _, k := range x.keys {
			parts = append(parts, reprString(k)+": "+PyRepr(x.vals[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func reprString(s string) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		r, n := DecodeRune(s[i:])
		i += n
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r < 0xd800 || r > 0xdfff:
			if IsPrint(r) {
				b.WriteRune(r)
			} else if r <= 0xff {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else if r <= 0xffff {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
