// Package asr writes Agent Session Record streams with the same record IDs,
// declared losses, secret filtering and checks as the ASR Python importer core.
package asr

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// DeriveID returns the opaque ID the ASR writers derive for a source position.
func DeriveID(scope, recordType string, position, collision int) string {
	data := "[" + pyjson.MustDumps(scope, pyjson.Options{Compact: true}) + "," +
		pyjson.MustDumps(recordType, pyjson.Options{Compact: true}) + "," +
		strconv.Itoa(position) + "," + strconv.Itoa(collision) + "]"
	sum := sha256.Sum256([]byte(data))
	return "asr-" + hex.EncodeToString(sum[:])
}

// ValidateRFC3339 is rfc3339-validator 0.1.4: an uppercase T and Z, an
// optional final newline, and a real calendar day.
func ValidateRFC3339(s string) bool {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	digit := func(i int) bool { return i < len(s) && s[i] >= '0' && s[i] <= '9' }
	if len(s) < 20 || !digit(0) || !digit(1) || !digit(2) || !digit(3) || s[4] != '-' {
		return false
	}
	year := int(s[0]-'0')*1000 + int(s[1]-'0')*100 + int(s[2]-'0')*10 + int(s[3]-'0')
	if !digit(5) || !digit(6) || s[7] != '-' || !digit(8) || !digit(9) || s[10] != 'T' {
		return false
	}
	month := int(s[5]-'0')*10 + int(s[6]-'0')
	day := int(s[8]-'0')*10 + int(s[9]-'0')
	if month < 1 || month > 12 {
		return false
	}
	hour := int(s[11]-'0')*10 + int(s[12]-'0')
	if !digit(11) || !digit(12) || hour > 23 || s[13] != ':' {
		return false
	}
	if !digit(14) || !digit(15) || s[14] > '5' || s[16] != ':' || !digit(17) || !digit(18) || s[17] > '5' {
		return false
	}
	i := 19
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for digit(i) {
			i++
		}
		if i == start {
			return false
		}
	}
	switch {
	case i < len(s) && s[i] == 'Z':
		i++
	case i < len(s) && (s[i] == '+' || s[i] == '-'):
		if i+6 > len(s) || !digit(i+1) || !digit(i+2) || s[i+3] != ':' || !digit(i+4) || !digit(i+5) || s[i+4] > '5' {
			return false
		}
		if int(s[i+1]-'0')*10+int(s[i+2]-'0') > 23 {
			return false
		}
		i += 6
	default:
		return false
	}
	if i != len(s) || year == 0 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month-1]
	if month == 2 && year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days = 29
	}
	return day >= 1 && day <= days
}

// Stamp reports whether a value is a timestamp importer-core keeps.
func Stamp(v any) bool {
	s, ok := v.(string)
	return ok && ValidateRFC3339(s)
}
