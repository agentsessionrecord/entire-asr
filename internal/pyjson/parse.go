package pyjson

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDepth bounds nesting so that a hostile document cannot exhaust the stack.
// Python's decoder stops at a similar depth with a RecursionError.
const MaxDepth = 50000

// maxIntDigits is Python's default limit for integer string conversion.
const maxIntDigits = 4300

// SyntaxError reports a document that Python's json module would reject,
// with the position Python's JSONDecodeError reports.
type SyntaxError struct {
	Msg               string
	Line, Column, Pos int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Line, e.Column, e.Pos)
}

// Parse reads one JSON document. Strict parsing is importer-core's loads:
// duplicate keys and NaN or Infinity are errors. Lenient parsing is Python's
// plain json.loads: the last duplicate wins and constants are kept.
// Lone surrogate escapes are kept as WTF-8 so that output can refuse them.
func Parse(s string, strict bool) (any, error) {
	p := &parser{s: s, strict: strict}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, p.fail("Extra data")
	}
	return v, nil
}

type parser struct {
	s      string
	i      int
	strict bool
}

func (p *parser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) fail(msg string) error { return p.failAt(msg, p.i) }

// failAt reports an error at a byte offset, counted in code points as Python does.
func (p *parser) failAt(msg string, at int) error {
	if at > len(p.s) {
		at = len(p.s)
	}
	pos := RuneLen(p.s[:at])
	line := strings.Count(p.s[:at], "\n") + 1
	col := pos + 1
	if i := strings.LastIndexByte(p.s[:at], '\n'); i >= 0 {
		col = pos - RuneLen(p.s[:i+1]) + 1
	}
	return &SyntaxError{Msg: msg, Line: line, Column: col, Pos: pos}
}

func (p *parser) value(depth int) (any, error) {
	if p.i >= len(p.s) {
		return nil, p.fail("Expecting value")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		if depth >= MaxDepth {
			return nil, p.fail("maximum recursion depth exceeded")
		}
		return p.object(depth + 1)
	case c == '[':
		if depth >= MaxDepth {
			return nil, p.fail("maximum recursion depth exceeded")
		}
		return p.array(depth + 1)
	case c == '"':
		p.i++
		return p.str()
	case c == 'n' && strings.HasPrefix(p.s[p.i:], "null"):
		p.i += 4
		return nil, nil
	case c == 't' && strings.HasPrefix(p.s[p.i:], "true"):
		p.i += 4
		return true, nil
	case c == 'f' && strings.HasPrefix(p.s[p.i:], "false"):
		p.i += 5
		return false, nil
	case c == 'N' && strings.HasPrefix(p.s[p.i:], "NaN"):
		p.i += 3
		return p.constant("NaN")
	case c == 'I' && strings.HasPrefix(p.s[p.i:], "Infinity"):
		p.i += 8
		return p.constant("Infinity")
	case c == '-' && strings.HasPrefix(p.s[p.i:], "-Infinity"):
		p.i += 9
		return p.constant("-Infinity")
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail("Expecting value")
}

func (p *parser) constant(name string) (any, error) {
	if p.strict {
		return nil, &ValueError{"non-finite JSON number"}
	}
	return Constant{name}, nil
}

func (p *parser) object(depth int) (any, error) {
	p.i++
	o := NewObject()
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return o, nil
	}
	for {
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.fail("Expecting property name enclosed in double quotes")
		}
		p.i++
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.fail("Expecting ':' delimiter")
		}
		p.i++
		p.ws()
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		k := key.(string)
		if p.strict && o.Has(k) {
			return nil, &ValueError{"duplicate JSON key"}
		}
		o.Set(k, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.fail("Expecting ',' delimiter")
		}
		if p.s[p.i] == '}' {
			p.i++
			return o, nil
		}
		if p.s[p.i] != ',' {
			return nil, p.fail("Expecting ',' delimiter")
		}
		p.i++
		p.ws()
	}
}

func (p *parser) array(depth int) (any, error) {
	p.i++
	list := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return list, nil
	}
	for {
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.fail("Expecting ',' delimiter")
		}
		if p.s[p.i] == ']' {
			p.i++
			return list, nil
		}
		if p.s[p.i] != ',' {
			return nil, p.fail("Expecting ',' delimiter")
		}
		p.i++
		p.ws()
	}
}

func hexValue(s string) (rune, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var r rune
	for i := 0; i < 4; i++ {
		c := s[i]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(d)
	}
	return r, true
}

func (p *parser) str() (any, error) {
	var b strings.Builder
	start := p.i
	quote := p.i - 1
	for {
		if p.i >= len(p.s) {
			return nil, p.failAt("Unterminated string starting at", quote)
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			b.WriteString(p.s[start:p.i])
			p.i++
			return b.String(), nil
		case c == '\\':
			b.WriteString(p.s[start:p.i])
			p.i++
			if p.i >= len(p.s) {
				return nil, p.failAt("Unterminated string starting at", quote)
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				if p.i+4 > len(p.s) {
					return nil, p.failAt("Invalid \\uXXXX escape", p.i-1)
				}
				r, ok := hexValue(p.s[p.i : p.i+4])
				if !ok {
					return nil, p.failAt("Invalid \\uXXXX escape", p.i-1)
				}
				p.i += 4
				if r >= 0xd800 && r <= 0xdbff && p.i+6 <= len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
					if low, ok := hexValue(p.s[p.i+2 : p.i+6]); ok && low >= 0xdc00 && low <= 0xdfff {
						r = 0x10000 + (r-0xd800)<<10 + (low - 0xdc00)
						p.i += 6
					}
				}
				b.WriteString(EncodeRune(r))
			default:
				return nil, p.failAt("Invalid \\escape", p.i-2)
			}
			start = p.i
		case c < 0x20:
			// json.loads rejects raw control characters in both modes.
			return nil, p.fail("Invalid control character at")
		default:
			p.i++
		}
	}
}

func (p *parser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, p.failAt("Expecting value", start)
	}
	switch c := p.s[p.i]; {
	case c == '0':
		p.i++
	case c >= '1' && c <= '9':
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
	default:
		return nil, p.failAt("Expecting value", start)
	}
	isFloat := false
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		isFloat = true
		p.i++
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		save := p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		digits := p.i
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		if p.i == digits {
			p.i = save
		} else {
			isFloat = true
		}
	}
	text := p.s[start:p.i]
	if isFloat {
		d, ok := parseDecimal(text)
		if !ok {
			return nil, p.fail("invalid decimal")
		}
		return Decimal{d.String()}, nil
	}
	digits := strings.TrimPrefix(text, "-")
	if len(digits) > maxIntDigits {
		return nil, &ValueError{"Exceeds the limit (4300 digits) for integer string conversion: value has " + strconv.Itoa(len(digits)) + " digits; use sys.set_int_max_str_digits() to increase the limit"}
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, p.fail("invalid integer")
	}
	return n, nil
}

type dec struct {
	neg  bool
	coef string // digits without leading zeros, "0" for zero
	exp  int64
}

// parseDecimal reads a decimal literal: sign, digits, optional fraction and exponent.
func parseDecimal(s string) (dec, bool) {
	var d dec
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		d.neg = s[i] == '-'
		i++
	}
	var digits strings.Builder
	frac := 0
	seen := false
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digits.WriteByte(s[i])
		i++
		seen = true
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			digits.WriteByte(s[i])
			i++
			frac++
			seen = true
		}
	}
	if !seen {
		return d, false
	}
	var exp int64
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		neg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			neg = s[i] == '-'
			i++
		}
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return d, false
		}
		text := strings.TrimLeft(s[start:i], "0")
		if len(text) > 15 {
			return d, false
		}
		if text != "" {
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return d, false
			}
			exp = n
		}
		if neg {
			exp = -exp
		}
	}
	if i != len(s) {
		return d, false
	}
	d.coef = strings.TrimLeft(digits.String(), "0")
	if d.coef == "" {
		d.coef = "0"
	}
	d.exp = exp - int64(frac)
	return d, true
}

// String prints a decimal as Python's Decimal.__str__ does.
func (d dec) String() string {
	sign := ""
	if d.neg {
		sign = "-"
	}
	n := int64(len(d.coef))
	leftdigits := d.exp + n
	var dotplace int64
	if d.exp <= 0 && leftdigits > -6 {
		dotplace = leftdigits
	} else {
		dotplace = 1
	}
	var intpart, fracpart string
	switch {
	case dotplace <= 0:
		intpart = "0"
		fracpart = "." + strings.Repeat("0", int(-dotplace)) + d.coef
	case dotplace >= n:
		intpart = d.coef + strings.Repeat("0", int(dotplace-n))
	default:
		intpart = d.coef[:dotplace]
		fracpart = "." + d.coef[dotplace:]
	}
	exp := ""
	if leftdigits != dotplace {
		e := leftdigits - dotplace
		if e >= 0 {
			exp = "E+" + strconv.FormatInt(e, 10)
		} else {
			exp = "E" + strconv.FormatInt(e, 10)
		}
	}
	return sign + intpart + fracpart + exp
}

// EncodeRune encodes a code point as UTF-8, or a lone surrogate as WTF-8.
func EncodeRune(r rune) string {
	if r >= 0xd800 && r <= 0xdfff {
		return string([]byte{0xed, byte(0xa0 | (r>>6)&0x1f), byte(0x80 | r&0x3f)})
	}
	return string(r)
}

// DecodeRune decodes one code point from WTF-8. An invalid byte decodes as
// itself plus 0xDC00, as Python's surrogateescape does, with width one.
func DecodeRune(s string) (rune, int) {
	if len(s) >= 3 && s[0] == 0xed && s[1] >= 0xa0 && s[1] <= 0xbf && s[2] >= 0x80 && s[2] <= 0xbf {
		return 0xd000 | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f), 3
	}
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n <= 1 {
		if len(s) == 0 {
			return utf8.RuneError, 0
		}
		return 0xdc00 + rune(s[0]), 1
	}
	return r, n
}

// Runes decodes a WTF-8 string into code points.
func Runes(s string) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		r, n := DecodeRune(s[i:])
		out = append(out, r)
		i += n
	}
	return out
}

// FromRunes encodes code points, keeping lone surrogates as WTF-8.
func FromRunes(rs []rune) string {
	var b strings.Builder
	b.Grow(len(rs))
	for _, r := range rs {
		if r >= 0xd800 && r <= 0xdfff {
			b.WriteString(EncodeRune(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RuneLen counts code points as Python's len does.
func RuneLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, w := DecodeRune(s[i:])
		i += w
		n++
	}
	return n
}
