package asr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

const redacted = "[redacted]"

// Scrub removes credentials from a value as importer-core's scrub does and
// reports whether anything changed. JSON documents held in strings are
// scrubbed as documents.
func Scrub(value any, secrets []string) (any, bool, error) {
	switch x := value.(type) {
	case string:
		if t := pyjson.LStrip(x); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			if pyjson.RuneLen(x) <= maxInputBytes {
				// Any ValueError on this path falls back to scrubbing the text.
				if parsed, err := pyjson.Parse(x, true); err == nil {
					cleaned, changed, err := Scrub(pyjson.Native(parsed), secrets)
					if err == nil && changed {
						if text, err := pyjson.Dumps(cleaned, pyjson.Options{Compact: true}); err == nil {
							return text, true, nil
						}
					}
				}
			}
		}
		result := substitute(x, matchAssignment)
		result = substitute(RedactText(result, secrets, redacted), matchURLCredentials)
		return result, result != x, nil
	case *pyjson.Object:
		result := pyjson.NewObject()
		changed := false
		for _, key := range x.Keys() {
			item := x.Value(key)
			safe, keyChanged, err := Scrub(key, secrets)
			if err != nil {
				return nil, false, err
			}
			safeKey := safe.(string)
			var itemChanged bool
			if CredentialKey(key) || vendorKey.MatchString(key) || opaqueKey.MatchString(key) {
				s, isStr := item.(string)
				item, itemChanged = redacted, !(isStr && s == redacted)
			} else if item, itemChanged, err = Scrub(item, secrets); err != nil {
				return nil, false, err
			}
			// Redacting two keys must not silently overwrite either value.
			if result.Has(safeKey) {
				if err := pyjson.CheckUTF8(key); err != nil {
					return nil, false, err
				}
				sum := sha256.Sum256([]byte(key))
				safeKey = "redacted-key-" + hex.EncodeToString(sum[:])
			}
			result.Set(safeKey, item)
			changed = changed || keyChanged || itemChanged
		}
		return result, changed, nil
	case []any:
		result := make([]any, len(x))
		changed := false
		for i, item := range x {
			cleaned, itemChanged, err := Scrub(item, secrets)
			if err != nil {
				return nil, false, err
			}
			result[i] = cleaned
			changed = changed || itemChanged
		}
		return result, changed, nil
	}
	return value, false, nil
}
