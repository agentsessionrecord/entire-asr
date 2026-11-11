package pyjson

import "unicode"

// unicode17 holds the letters, digits and other characters that Unicode 17.0
// assigned and Unicode 16.0, the version of the Python reference, had not.
// Text classes leave them out so that word boundaries and digits match Python.
var unicode17 = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x088F, Hi: 0x088F, Stride: 1},
		{Lo: 0x0C5C, Hi: 0x0C5C, Stride: 1},
		{Lo: 0x0CDC, Hi: 0x0CDC, Stride: 1},
		{Lo: 0x1ACF, Hi: 0x1ADD, Stride: 1},
		{Lo: 0x1AE0, Hi: 0x1AEB, Stride: 1},
		{Lo: 0x20C1, Hi: 0x20C1, Stride: 1},
		{Lo: 0x2B96, Hi: 0x2B96, Stride: 1},
		{Lo: 0xA7CE, Hi: 0xA7CF, Stride: 1},
		{Lo: 0xA7D2, Hi: 0xA7D2, Stride: 1},
		{Lo: 0xA7D4, Hi: 0xA7D4, Stride: 1},
		{Lo: 0xA7F1, Hi: 0xA7F1, Stride: 1},
		{Lo: 0xFBC3, Hi: 0xFBD2, Stride: 1},
		{Lo: 0xFD90, Hi: 0xFD91, Stride: 1},
		{Lo: 0xFDC8, Hi: 0xFDCE, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x10940, Hi: 0x10959, Stride: 1},
		{Lo: 0x10EC5, Hi: 0x10EC7, Stride: 1},
		{Lo: 0x10ED0, Hi: 0x10ED8, Stride: 1},
		{Lo: 0x10EFA, Hi: 0x10EFB, Stride: 1},
		{Lo: 0x11B60, Hi: 0x11B67, Stride: 1},
		{Lo: 0x11DB0, Hi: 0x11DDB, Stride: 1},
		{Lo: 0x11DE0, Hi: 0x11DE9, Stride: 1},
		{Lo: 0x16EA0, Hi: 0x16EB8, Stride: 1},
		{Lo: 0x16EBB, Hi: 0x16ED3, Stride: 1},
		{Lo: 0x16FF2, Hi: 0x16FF6, Stride: 1},
		{Lo: 0x187F8, Hi: 0x187FF, Stride: 1},
		{Lo: 0x18D09, Hi: 0x18D1E, Stride: 1},
		{Lo: 0x18D80, Hi: 0x18DF2, Stride: 1},
		{Lo: 0x1CCFA, Hi: 0x1CCFC, Stride: 1},
		{Lo: 0x1CEBA, Hi: 0x1CED0, Stride: 1},
		{Lo: 0x1CEE0, Hi: 0x1CEF0, Stride: 1},
		{Lo: 0x1E6C0, Hi: 0x1E6DE, Stride: 1},
		{Lo: 0x1E6E0, Hi: 0x1E6F5, Stride: 1},
		{Lo: 0x1E6FE, Hi: 0x1E6FF, Stride: 1},
		{Lo: 0x1F6D8, Hi: 0x1F6D8, Stride: 1},
		{Lo: 0x1F777, Hi: 0x1F77A, Stride: 1},
		{Lo: 0x1F8D0, Hi: 0x1F8D8, Stride: 1},
		{Lo: 0x1FA54, Hi: 0x1FA57, Stride: 1},
		{Lo: 0x1FA8A, Hi: 0x1FA8A, Stride: 1},
		{Lo: 0x1FA8E, Hi: 0x1FA8E, Stride: 1},
		{Lo: 0x1FAC8, Hi: 0x1FAC8, Stride: 1},
		{Lo: 0x1FACD, Hi: 0x1FACD, Stride: 1},
		{Lo: 0x1FAEA, Hi: 0x1FAEA, Stride: 1},
		{Lo: 0x1FAEF, Hi: 0x1FAEF, Stride: 1},
		{Lo: 0x1FBFA, Hi: 0x1FBFA, Stride: 1},
		{Lo: 0x2B73A, Hi: 0x2B73F, Stride: 1},
		{Lo: 0x2CEA2, Hi: 0x2CEAD, Stride: 1},
		{Lo: 0x323B0, Hi: 0x33479, Stride: 1},
	},
	LatinOffset: 0,
}

// assigned16 reports whether Unicode 16.0 assigned a code point that Go
// classifies as a letter, digit or printable character.
func assigned16(r rune) bool { return !unicode.Is(unicode17, r) }

// IsDigit reports whether a code point is a decimal digit in Unicode 16.0.
func IsDigit(r rune) bool { return unicode.IsDigit(r) && assigned16(r) }

// IsPrint reports whether a code point is printable in Unicode 16.0.
func IsPrint(r rune) bool { return unicode.IsPrint(r) && assigned16(r) }
