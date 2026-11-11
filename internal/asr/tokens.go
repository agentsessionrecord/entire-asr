package asr

import "github.com/agentsessionrecord/entire-asr/internal/pyjson"

// The matchers in this file reproduce the regular expressions of the ASR
// Python privacy module and importer core, which use lookbehind, backreferences
// and Unicode word boundaries that Go's regexp package does not support. Each
// returns the end of the match that Python's backtracking engine finds at p,
// or -1. All of them consume at least one code point.

func at(r []rune, i int) rune {
	if i < 0 || i >= len(r) {
		return -1
	}
	return r[i]
}

func isWordAt(r []rune, i int) bool {
	c := at(r, i)
	return c >= 0 && pyjson.IsWord(c)
}

// boundary is Python's \b in a Unicode pattern.
func boundary(r []rune, i int) bool { return isWordAt(r, i-1) != isWordAt(r, i) }

func isSpaceAt(r []rune, i int) bool {
	c := at(r, i)
	return c >= 0 && pyjson.IsSpace(c)
}

// foldEq compares a code point with a lowercase ASCII letter under re.IGNORECASE.
func foldEq(c rune, letter byte) bool {
	l := rune(letter)
	if c == l || c == l-0x20 {
		return true
	}
	switch letter {
	case 'i':
		return c == 0x130 || c == 0x131
	case 's':
		return c == 0x17f
	case 'k':
		return c == 0x212a
	}
	return false
}

// literal matches s exactly at p. Lowercase letters in s match either case
// when fold is set.
func literal(r []rune, p int, s string, fold bool) int {
	for i := 0; i < len(s); i++ {
		c := at(r, p+i)
		if c < 0 {
			return -1
		}
		ch := s[i]
		if fold && ch >= 'a' && ch <= 'z' {
			if !foldEq(c, ch) {
				return -1
			}
		} else if c != rune(ch) {
			return -1
		}
	}
	return p + len(s)
}

// run returns the end of the longest run from p whose code points satisfy in.
func run(r []rune, p int, in func(rune) bool) int {
	for p < len(r) && in(r[p]) {
		p++
	}
	return p
}

func alnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// keyword matches a case-insensitive word in which "|" stands for an optional
// separator from seps, as in api[_ -]?key.
func keyword(r []rune, p int, word, seps string) int {
	for i := 0; i < len(word); i++ {
		if word[i] != '|' {
			continue
		}
		q := literal(r, p, word[:i], true)
		if q < 0 {
			return -1
		}
		rest := word[i+1:]
		if c := at(r, q); c >= 0 && c < 0x80 && containsByte(seps, byte(c)) {
			if e := literal(r, q+1, rest, true); e >= 0 {
				return e
			}
		}
		return literal(r, q, rest, true)
	}
	return literal(r, p, word, true)
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// greedyBounded backtracks a greedy run of at least min code points from q
// until a word boundary follows, as `X{min,}\b` does.
func greedyBounded(r []rune, q, min int, in func(rune) bool) int {
	e := run(r, q, in)
	for ; e-q >= min; e-- {
		if boundary(r, e) {
			return e
		}
	}
	return -1
}

func tokenWord(r []rune, p int) int {
	if !boundary(r, p) {
		return -1
	}
	dash := func(c rune) bool { return alnum(c) || c == '_' || c == '-' }
	if q := literal(r, p, "sk-", false); q >= 0 {
		if e := greedyBounded(r, q, 12, dash); e >= 0 {
			return e
		}
	}
	if q := literal(r, p, "sess-", false); q >= 0 {
		if e := greedyBounded(r, q, 20, dash); e >= 0 {
			return e
		}
	}
	if literal(r, p, "gh", false) >= 0 && containsRune("pousr", at(r, p+2)) && at(r, p+3) == '_' {
		if e := greedyBounded(r, p+4, 16, alnum); e >= 0 {
			return e
		}
	}
	if q := literal(r, p, "github_pat_", false); q >= 0 {
		if e := greedyBounded(r, q, 16, func(c rune) bool { return alnum(c) || c == '_' }); e >= 0 {
			return e
		}
	}
	if literal(r, p, "xox", false) >= 0 && containsRune("baprs", at(r, p+3)) && at(r, p+4) == '-' {
		if e := greedyBounded(r, p+5, 16, func(c rune) bool { return alnum(c) || c == '-' }); e >= 0 {
			return e
		}
	}
	if literal(r, p, "AKIA", false) >= 0 || literal(r, p, "ASIA", false) >= 0 {
		upper := func(c rune) bool { return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
		if run(r, p+4, upper)-(p+4) >= 16 && boundary(r, p+20) {
			return p + 20
		}
	}
	if q := literal(r, p, "eyJ", false); q >= 0 {
		q1 := run(r, q, dash)
		if q1 > q && at(r, q1) == '.' {
			q2 := run(r, q1+1, dash)
			if q2 > q1+1 && at(r, q2) == '.' {
				if e := greedyBounded(r, q2+1, 1, dash); e >= 0 {
					return e
				}
			}
		}
	}
	return -1
}

func containsRune(s string, c rune) bool {
	return c >= 0 && c < 0x80 && containsByte(s, byte(c))
}

// spaced matches a case-insensitive word followed by Python whitespace.
func spaced(r []rune, p int, word string) int {
	q := literal(r, p, word, true)
	if q < 0 {
		return -1
	}
	e := run(r, q, pyjson.IsSpace)
	if e == q {
		return -1
	}
	return e
}

func tokenBearer(r []rune, p int) int {
	q := spaced(r, p, "bearer")
	if q < 0 {
		return -1
	}
	e := run(r, q, func(c rune) bool { return alnum(c) || containsRune("._~+/=-", c) })
	if e == q {
		return -1
	}
	return e
}

func tokenBasic(r []rune, p int) int {
	q := spaced(r, p, "basic")
	if q < 0 {
		return -1
	}
	e := run(r, q, func(c rune) bool { return alnum(c) || c == '+' || c == '/' })
	if e-q < 16 {
		return -1
	}
	for i := 0; i < 2 && at(r, e) == '='; i++ {
		e++
	}
	return e
}

// pemLabel matches prefix, then [^-]*PRIVATE KEY-----.
func pemLabel(r []rune, p int, prefix string) int {
	q := literal(r, p, prefix, false)
	if q < 0 {
		return -1
	}
	dash := run(r, q, func(c rune) bool { return c != '-' })
	start := dash - len("PRIVATE KEY")
	if start < q || literal(r, start, "PRIVATE KEY-----", false) < 0 {
		return -1
	}
	return dash + 5
}

func tokenPEM(r []rune, p int) int {
	q := pemLabel(r, p, "-----BEGIN ")
	if q < 0 {
		return -1
	}
	for j := q; j <= len(r); j++ {
		if e := pemLabel(r, j, "-----END "); e >= 0 {
			return e
		}
		if j == len(r) || (j == len(r)-1 && r[j] == '\n') {
			return j
		}
	}
	return -1
}

var assignmentWords = []string{"password", "passwd", "api|key", "access|token", "refresh|token", "auth|token",
	"session|token", "authorization", "client|secret", "secret", "cookie", "token"}

// quoted matches "..." or '...' with backslash escapes that stop at a newline.
func quoted(r []rune, v int, quote rune) int {
	if at(r, v) != quote {
		return -1
	}
	j := v + 1
	for j < len(r) {
		c := r[j]
		if c == '\\' && j+1 < len(r) && r[j+1] != '\n' {
			j += 2
		} else if c != quote && c != '\\' && c != '\r' && c != '\n' {
			j++
		} else {
			break
		}
	}
	if at(r, j) == quote {
		return j + 1
	}
	return -1
}

func tokenAssignment(r []rune, p int) int {
	if !boundary(r, p) {
		return -1
	}
	for _, word := range assignmentWords {
		q := keyword(r, p, word, "_ -")
		if q < 0 {
			continue
		}
		if c := at(r, q); c == '"' || c == '\'' {
			q++
		}
		q = run(r, q, pyjson.IsSpace)
		if c := at(r, q); c != ':' && c != '=' {
			continue
		}
		v := run(r, q+1, pyjson.IsSpace)
		if e := quoted(r, v, '"'); e >= 0 {
			return e
		}
		if e := quoted(r, v, '\''); e >= 0 {
			return e
		}
		if e := run(r, v, func(c rune) bool { return !pyjson.IsSpace(c) && !containsRune(",;}\"'", c) }); e > v {
			return e
		}
	}
	return -1
}

func afterScheme(r []rune, p int) bool {
	return p >= 3 && r[p-3] == ':' && r[p-2] == '/' && r[p-1] == '/'
}

func tokenUserinfo(r []rune, p int) int {
	if !afterScheme(r, p) {
		return -1
	}
	e := run(r, p, func(c rune) bool { return c != '/' && c != '@' && !pyjson.IsSpace(c) })
	if e > p && at(r, e) == '@' {
		return e + 1
	}
	return -1
}

var queryWords = []string{"api|key", "access|token", "refresh|token", "auth|token", "session|token", "client|secret",
	"password", "secret", "token"}

func tokenQuery(r []rune, p int) int {
	if c := at(r, p); c != '?' && c != '&' {
		return -1
	}
	for _, word := range queryWords {
		q := keyword(r, p+1, word, "_-")
		if q < 0 || at(r, q) != '=' {
			continue
		}
		return run(r, q+1, func(c rune) bool { return !pyjson.IsSpace(c) && !containsRune("&#\"'", c) })
	}
	return -1
}

// matchToken is the privacy module's TOKEN pattern, alternative by alternative.
func matchToken(r []rune, p int) int {
	for _, alt := range [...]func([]rune, int) int{tokenWord, tokenBearer, tokenBasic, tokenPEM, tokenAssignment,
		tokenUserinfo, tokenQuery} {
		if e := alt(r, p); e >= 0 {
			return e
		}
	}
	return -1
}

var assignmentKeys = []string{"api|key", "api|token", "access|key", "access|token", "auth|key", "auth|token",
	"refresh|key", "refresh|token", "secret|key", "secret|token", "password", "session|token"}

// matchAssignment is importer-core's ASSIGNMENT pattern. It returns the match
// end and the replacement, which keeps the key and separator.
func matchAssignment(r []rune, p int) (int, []rune) {
	if !boundary(r, p) {
		return -1, nil
	}
	for _, word := range assignmentKeys {
		q := keyword(r, p, word, "_-")
		if q < 0 {
			continue
		}
		s := run(r, q, pyjson.IsSpace)
		if c := at(r, s); c != ':' && c != '=' {
			continue
		}
		v := run(r, s+1, pyjson.IsSpace)
		quote := at(r, v)
		start := v
		if quote == '"' || quote == '\'' {
			start++
		} else {
			quote = -1
		}
		e := run(r, start, func(c rune) bool { return !pyjson.IsSpace(c) && !containsRune("\"',;}", c) })
		if e == start || (quote >= 0 && at(r, e) != quote) {
			continue
		}
		if quote >= 0 {
			e++
		}
		repl := append(append([]rune(nil), r[p:v]...), []rune("[redacted]")...)
		return e, repl
	}
	return -1, nil
}

// matchURLCredentials is importer-core's URL_CREDENTIALS pattern, user:secret@ after ://.
func matchURLCredentials(r []rune, p int) (int, []rune) {
	if !afterScheme(r, p) {
		return -1, nil
	}
	e := run(r, p, func(c rune) bool { return c != '/' && c != '@' && !pyjson.IsSpace(c) })
	if at(r, e) != '@' {
		return -1, nil
	}
	for c := e - 2; c > p; c-- {
		if r[c] == ':' {
			return e + 1, []rune("[redacted]")
		}
	}
	return -1, nil
}
