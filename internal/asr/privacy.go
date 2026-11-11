package asr

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// ci turns an ASCII pattern into one that matches as Python's re.IGNORECASE
// does, including the four non-ASCII letters Python folds onto i, s and k.
func ci(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch lower := r | 0x20; {
		case lower == 'i' && (r == 'i' || r == 'I'):
			b.WriteString(`[iI\x{130}\x{131}]`)
		case lower == 's' && (r == 's' || r == 'S'):
			b.WriteString(`[sS\x{17f}]`)
		case lower == 'k' && (r == 'k' || r == 'K'):
			b.WriteString(`[kK\x{212a}]`)
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			b.WriteString("[" + string(lower) + string(lower-0x20) + "]")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// end is Python's $ without MULTILINE: the end, or before a final newline.
const end = `(?:\n?\z)`

var (
	camelCase = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	keySep    = regexp.MustCompile(`[-. ]`)
	credName  = regexp.MustCompile(`(?:^|_)(?:` + ci("api_key|access_token|refresh_token|auth_token|session_token|"+
		"authorization|password|passwd|secret|secrets|credential|credentials|"+
		"cookies|cookie|set_cookie|private_key|client_secret|secret_access_key") + `)` + end + `|^` + ci("token") + end)
	envName = regexp.MustCompile(`(?:^|_)(?:` + ci("KEY|TOKEN|PASSWORD|PASSWD|SECRET|CREDENTIALS?") + `)(?:_|` + end + `)`)
	// importer-core's vendor credential and opaque provider-state keys.
	vendorKey = regexp.MustCompile(`\A(?:` + ci("api|access|auth|refresh|secret") + `)[_-]?(?:` + ci("key|token") + `)\z`)
	opaqueKey = regexp.MustCompile(`\A(?:` + ci("signature|encrypted_content|encrypted_thinking|resume_token") + `)\z`)
)

// opaqueNames are keys whose values the credential scan does not enter.
var opaqueNames = map[string]bool{"signature": true, "encrypted_content": true, "encryptedContent": true}

// CredentialKey reports whether a key names a credential.
func CredentialKey(value string) bool {
	name := camelCase.ReplaceAllString(value, "${1}_${2}")
	name = keySep.ReplaceAllString(name, "_")
	return credName.MatchString(name)
}

func sortSecrets(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := pyjson.RuneLen(out[i]), pyjson.RuneLen(out[j])
		if li != lj {
			return li > lj
		}
		return out[i] < out[j]
	})
	return out
}

// EnvironmentSecrets returns credential-like environment values of eight or
// more characters, longest first.
func EnvironmentSecrets() []string {
	set := map[string]bool{}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if (CredentialKey(name) || envName.MatchString(name)) && pyjson.RuneLen(value) >= 8 {
			set[value] = true
		}
	}
	return sortSecrets(set)
}

// CredentialValues learns the values of credential-named keys anywhere in a
// value, including JSON documents held in strings. It fails beyond 100,000
// values or 40 levels, as the Python scan does.
func CredentialValues(value any) ([]string, error) {
	set := map[string]bool{}
	nodes, size := 0, 0
	capacity := &pyjson.ValueError{Msg: "credential_scan_capacity"}
	var walk func(item any, depth int) error
	walk = func(item any, depth int) error {
		nodes++
		if nodes > 100_000 || depth > 40 {
			return capacity
		}
		switch x := item.(type) {
		case *pyjson.Object:
			for _, key := range x.Keys() {
				child := x.Value(key)
				s, isStr := child.(string)
				if CredentialKey(key) && isStr && s != "" && !isPlaceholder(s) {
					if !set[s] {
						size += pyjson.RuneLen(s)
						if size > 1024*1024 {
							return capacity
						}
						set[s] = true
					}
				} else if !opaqueNames[key] {
					if err := walk(child, depth+1); err != nil {
						return err
					}
				}
			}
		case []any:
			for _, child := range x {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case string:
			if t := pyjson.LStrip(x); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
				decoded, err := pyjson.Parse(x, false)
				if err != nil {
					return nil
				}
				return walk(decoded, depth+1)
			}
		}
		return nil
	}
	if err := walk(value, 0); err != nil {
		return nil, err
	}
	return sortSecrets(set), nil
}

func isPlaceholder(s string) bool {
	lower := strings.ToLower(s)
	return lower == "[redacted]" || lower == "[omitted]"
}

// RedactText replaces known secrets, then recognized credential forms.
func RedactText(value string, secrets []string, replacement string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, replacement)
		}
	}
	repl := []rune(replacement)
	return substitute(value, func(r []rune, p int) (int, []rune) {
		if e := matchToken(r, p); e > p {
			return e, repl
		}
		return -1, nil
	})
}

// substitute replaces every non-overlapping match, scanning left to right as
// Python's re.sub does. match returns the end of a match at p and its
// replacement, or -1.
func substitute(value string, match func(r []rune, p int) (int, []rune)) string {
	r := pyjson.Runes(value)
	var out []rune
	changed := false
	last := 0
	for p := 0; p < len(r); {
		if e, repl := match(r, p); e > p {
			if !changed {
				out = make([]rune, 0, len(r))
				changed = true
			}
			out = append(out, r[last:p]...)
			out = append(out, repl...)
			p = e
			last = e
			continue
		}
		p++
	}
	if !changed {
		return value
	}
	out = append(out, r[last:]...)
	return pyjson.FromRunes(out)
}
