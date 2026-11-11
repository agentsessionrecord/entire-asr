package asr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Expected values in this file come from the ASR Python packages.

func TestDeriveID(t *testing.T) {
	scope := pyjson.MustDumps([]any{"claude-code", nil, "session-1", "entire-transcript-v1"}, pyjson.Options{EnsureASCII: true})
	if got := DeriveID(scope, "message", 3, 0); got != "asr-17898eeebf5523e9e8b46fe9d9648403acd05d0d66aee82fb06c63ed203efcad" {
		t.Fatal(got)
	}
	scope = pyjson.MustDumps([]any{"pi", "ns", "sé", "x"}, pyjson.Options{EnsureASCII: true})
	if got := DeriveID(scope, "gap", 0, 1); got != "asr-b95198156a9369936963279f6f742adcf487fcff91169c77739dc5ca5996a9f1" {
		t.Fatal(got)
	}
}

func TestValidateRFC3339(t *testing.T) {
	for value, want := range map[string]bool{"2026-10-01T10:00:00Z": true, "2026-10-01T10:00:00.5+05:30": true,
		"2026-02-29T10:00:00Z": false, "2024-02-29T10:00:00Z": true, "2026-10-01t10:00:00z": false,
		"2026-10-01T24:00:00Z": false, "2026-10-01T10:00:00Z\n": true, "0000-01-01T00:00:00Z": false, "": false} {
		if got := ValidateRFC3339(value); got != want {
			t.Errorf("%q: %v", value, got)
		}
	}
}

func TestRedactText(t *testing.T) {
	cases := [][2]string{
		{"key sk-abcdefghijklmnop here", "key [redacted] here"},
		{"Authorization: Bearer abc.def", "[redacted] abc.def"},
		{"url https://user:pw@example.com/x", "url https://[redacted]example.com/x"},
		{"x?api_key=123&y=2", "x[redacted]&y=2"},
		{`password = "hunter2" next`, "[redacted] next"},
		{"ghp_" + strings.Repeat("a", 20), "[redacted]"},
		{"token: x", "[redacted]"},
		{"no secret here", "no secret here"},
		{"sess-" + strings.Repeat("b", 19), "sess-" + strings.Repeat("b", 19)},
		{"AKIA" + strings.Repeat("A", 16) + "B", "AKIA" + strings.Repeat("A", 16) + "B"},
		{"eyJhbGc.eyJzdWI.sig", "[redacted]"},
		{"-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----", "[redacted]"},
		{"basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==", "[redacted]"},
	}
	for _, c := range cases {
		if got := RedactText(c[0], []string{"hunter"}, "[redacted]"); got != c[1] {
			t.Errorf("%q: got %q, want %q", c[0], got, c[1])
		}
	}
}

func TestScrub(t *testing.T) {
	value := pyjson.Obj("api_key", "k", "nested", `{"password": "p1", "n": 1}`,
		"text", "auth_token=abc123 and https://a:b@h", "signature", "opaque", "list", []any{"secret: s3"})
	got, changed, err := Scrub(value, []string{"abc123"})
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	want := `{"api_key": "[redacted]", "nested": "{\"password\":\"[redacted]\",\"n\":1}", "text": "[redacted] and https://[redacted]h", "signature": "[redacted]", "list": ["[redacted]"]}`
	if s := pyjson.MustDumps(got, pyjson.Options{}); s != want {
		t.Fatalf("got %s", s)
	}
}

func TestCredentialKey(t *testing.T) {
	for key, want := range map[string]bool{"apiKey": true, "api_key": true, "token": true, "Token": true, "mytoken": false,
		"set-cookie": true, "privateKey": true, "client.secret": true, "AWS_SECRET_ACCESS_KEY": true, "name": false,
		"tokens": false, "x_secret": true, "secretName": false} {
		if got := CredentialKey(key); got != want {
			t.Errorf("%q: %v", key, got)
		}
	}
}

func TestStreamDeclaresLossesAndWritesPrivately(t *testing.T) {
	stream, err := NewStream("pi", "s1", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Add("message", "m1", "bad time", pyjson.Obj("token", "abcdefgh"),
		F{"role", "user"}, F{"content", []any{pyjson.Obj("type", "text", "text", "the key is abcdefgh")}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := stream.Write(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "abcdefgh") || !strings.Contains(text, `"truncated":true`) {
		t.Fatalf("credential kept: %s", text)
	}
	for _, loss := range []string{`"timestamps"`, `"secrets"`} {
		if !strings.Contains(strings.SplitN(text, "\n", 2)[0], loss) {
			t.Fatalf("%s not declared: %s", loss, text)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := stream.Write(path); err == nil {
		t.Fatal("an existing file was replaced")
	}
}

func TestSchemaRejectsRecords(t *testing.T) {
	stream, _ := NewStream("pi", "s1", "test", nil)
	stream.Missing("model")
	if _, err := stream.Add("message", nil, nil, nil, F{"role", int64(3)}, F{"content", []any{}}); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Encode()
	if err == nil || err.Error() != "Record fails schema at /role" {
		t.Fatalf("got %v", err)
	}
}

func TestReusedResultCallIDsAreUnpaired(t *testing.T) {
	stream, _ := NewStream("pi", "s1", "test", nil)
	for i := 0; i < 2; i++ {
		if _, err := stream.Add("tool_result", nil, nil, nil, F{"callId", "c1"}, F{"status", "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := stream.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"unpairedCallId":"c1"`) != 2 || !strings.Contains(string(data), `"ambiguous_tool_results"`) {
		t.Fatalf("%s", data)
	}
}
