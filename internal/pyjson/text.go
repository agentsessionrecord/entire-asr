package pyjson

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsSpace reports Python's str.isspace for one code point, which is also what
// a regular expression's \s matches in Python.
func IsSpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// IsWord reports whether Python's \w matches a code point.
func IsWord(r rune) bool {
	return r == '_' || (unicode.IsLetter(r) || unicode.IsNumber(r)) && assigned16(r)
}

// LStrip removes leading whitespace as Python's str.lstrip() does.
func LStrip(s string) string {
	return strings.TrimLeftFunc(s, IsSpace)
}

// Strip removes surrounding whitespace as Python's str.strip() does.
func Strip(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

// DecodeError mirrors Python's UnicodeDecodeError message.
type DecodeError struct {
	Data       []byte
	Start, End int
	Reason     string
}

func (e *DecodeError) Error() string {
	if e.End-e.Start == 1 {
		return fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: %s", e.Data[e.Start], e.Start, e.Reason)
	}
	return fmt.Sprintf("'utf-8' codec can't decode bytes in position %d-%d: %s", e.Start, e.End-1, e.Reason)
}

// invalidAt returns the end of the maximal invalid subpart at i and the
// reason Python reports for it, or -1 when a valid sequence starts at i.
func invalidAt(b []byte, i int) (int, string) {
	c := b[i]
	if c < 0x80 {
		return -1, ""
	}
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		need, lo = 2, 0xa0
	case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
		need = 2
	case c == 0xed:
		need, hi = 2, 0x9f
	case c == 0xf0:
		need, lo = 3, 0x90
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	case c == 0xf4:
		need, hi = 3, 0x8f
	default:
		return i + 1, "invalid start byte"
	}
	j := i + 1
	for k := 0; k < need; k++ {
		if j >= len(b) {
			return j, "unexpected end of data"
		}
		low, high := byte(0x80), byte(0xbf)
		if k == 0 {
			low, high = lo, hi
		}
		if b[j] < low || b[j] > high {
			return j, "invalid continuation byte"
		}
		j++
	}
	return -1, ""
}

// DecodeStrict checks that bytes are UTF-8, as bytes.decode("utf-8") does.
func DecodeStrict(b []byte) (string, error) {
	if utf8.Valid(b) {
		return string(b), nil
	}
	for i := 0; i < len(b); {
		if end, reason := invalidAt(b, i); end >= 0 {
			return "", &DecodeError{b, i, end, reason}
		}
		_, n := utf8.DecodeRune(b[i:])
		i += n
	}
	return string(b), nil
}

// DecodeReplace decodes UTF-8 and replaces each maximal invalid subpart with
// U+FFFD, as bytes.decode("utf-8", "replace") does.
func DecodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		if end, _ := invalidAt(b, i); end >= 0 {
			out.WriteRune(utf8.RuneError)
			i = end
			continue
		}
		_, n := utf8.DecodeRune(b[i:])
		out.Write(b[i : i+n])
		i += n
	}
	return out.String()
}
