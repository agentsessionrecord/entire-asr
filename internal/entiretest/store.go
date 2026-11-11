// Package entiretest builds synthetic Entire checkpoint stores with Git
// plumbing, laid out as Entire v0.13.2 writes them. It follows the store
// builder of the ASR Entire importer tests and reads the same fixtures.
package entiretest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Branch is the git-branch backend's local branch.
const Branch = "refs/heads/entire/checkpoints/v1"

// Fixtures returns the testdata directory.
func Fixtures() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

var gitEnv = []string{"GIT_AUTHOR_NAME=Synthetic Test", "GIT_AUTHOR_EMAIL=synthetic@example.invalid",
	"GIT_COMMITTER_NAME=Synthetic Test", "GIT_COMMITTER_EMAIL=synthetic@example.invalid",
	"GIT_AUTHOR_DATE=2026-10-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T12:00:00Z",
	"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}

// Git runs a Git command in repo and returns its trimmed output.
func Git(t testing.TB, repo string, input []byte, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-C", repo}, args...)...)
	cmd.Env = append(append(os.Environ(), gitEnv...), extraEnv...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errOut.String())
	}
	return strings.TrimSpace(out.String())
}

// Init creates a repository with one empty commit.
func Init(t testing.TB, repo string) string {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, repo, nil, nil, "init", "-q", "-b", "main")
	Git(t, repo, nil, nil, "commit", "-q", "--allow-empty", "-m", "Synthetic baseline")
	return repo
}

// JSONL writes rows as compact JSON lines, as Python's json.dumps with
// separators=(",", ":") does.
func JSONL(rows []any) []byte {
	var b bytes.Buffer
	for _, row := range rows {
		b.WriteString(pyjson.MustDumps(row, pyjson.Options{EnsureASCII: true, Compact: true}))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// WriteTree stores files as a Git tree and returns its ID.
func WriteTree(t testing.TB, repo string, files map[string][]byte) string {
	t.Helper()
	gitDir := Git(t, repo, nil, nil, "rev-parse", "--absolute-git-dir")
	index := filepath.Join(gitDir, "synthetic-index")
	os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		oid := Git(t, repo, files[p], nil, "hash-object", "-w", "--stdin")
		Git(t, repo, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+oid+","+p)
	}
	tree := Git(t, repo, nil, env, "write-tree")
	os.Remove(index)
	return tree
}

// Session is one session slot of a checkpoint in a store specification.
type Session struct {
	Metadata   *pyjson.Object
	Transcript string
	// Lines are indexes into the named fixture transcript, or whole lines.
	Lines   []any
	Full    []byte
	Compact *bool
}

// Checkpoint is one stored checkpoint copy.
type Checkpoint struct {
	Backend  string
	ID       string
	Summary  *pyjson.Object
	Sessions []Session
}

func indent(v any) []byte {
	return []byte(pyjson.MustDumps(v, pyjson.Options{EnsureASCII: true, Indent: 2}) + "\n")
}

// CheckpointFiles returns one checkpoint subtree. prefix is "" at a ref root,
// or the branch shard path.
func CheckpointFiles(c Checkpoint, prefix string, transcripts map[string][]any) map[string][]byte {
	base := ""
	if prefix != "" {
		base = prefix + "/"
	}
	files := map[string][]byte{}
	var pointers []any
	for index, session := range c.Sessions {
		directory := fmt.Sprintf("%s%d", base, index)
		source := transcripts[session.Transcript]
		var lines []any
		for _, item := range session.Lines {
			if n, ok := item.(int); ok {
				lines = append(lines, source[n])
			} else {
				lines = append(lines, item)
			}
		}
		files[directory+"/metadata.json"] = indent(session.Metadata)
		pointer := pyjson.Obj("metadata", "/"+directory+"/metadata.json", "prompt", "/"+directory+"/prompt.txt")
		files[directory+"/prompt.txt"] = []byte{}
		if session.Full != nil {
			files[directory+"/full.jsonl"] = session.Full
			pointer.Set("transcript", "/"+directory+"/full.jsonl")
		}
		if session.Compact == nil || *session.Compact {
			files[directory+"/transcript.jsonl"] = JSONL(lines)
			pointer.Set("compact_transcript", "/"+directory+"/transcript.jsonl")
		}
		pointers = append(pointers, pointer)
	}
	if pointers == nil {
		pointers = []any{}
	}
	summary := pyjson.Obj("cli_version", "0.13.2", "checkpoint_id", c.ID, "strategy", "manual-commit", "branch", "main",
		"checkpoints_count", int64(len(pointers)), "files_touched", []any{}, "sessions", pointers)
	if c.Summary != nil {
		summary.Update(c.Summary)
	}
	files[base+"metadata.json"] = indent(summary)
	return files
}

// Build writes each checkpoint copy in order: branch copies as v1 commits,
// ref copies as one ref each.
func Build(t testing.TB, repo string, checkpoints []Checkpoint, transcripts map[string][]any) {
	t.Helper()
	if transcripts == nil {
		transcripts = LoadTranscripts(t)
	}
	branchFiles := map[string][]byte{}
	tip := ""
	for _, c := range checkpoints {
		if c.Backend == "git-branch" {
			for k, v := range CheckpointFiles(c, c.ID[:2]+"/"+c.ID[2:], transcripts) {
				branchFiles[k] = v
			}
			tree := WriteTree(t, repo, branchFiles)
			args := []string{"commit-tree", tree}
			if tip != "" {
				args = append(args, "-p", tip)
			}
			tip = Git(t, repo, nil, nil, append(args, "-m", "Checkpoint: "+c.ID)...)
			Git(t, repo, nil, nil, "update-ref", Branch, tip)
		} else {
			tree := WriteTree(t, repo, CheckpointFiles(c, "", transcripts))
			commit := Git(t, repo, nil, nil, "commit-tree", tree, "-m", "Checkpoint: "+c.ID)
			Git(t, repo, nil, nil, "update-ref", "refs/entire/checkpoints/"+c.ID[len(c.ID)-2:]+"/"+c.ID, commit)
		}
	}
}

func readJSONLines(t testing.TB, path string) []any {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		v, err := pyjson.Parse(line, false)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, v)
	}
	return rows
}

// LoadTranscripts reads the normalized fixture transcripts by agent.
func LoadTranscripts(t testing.TB) map[string][]any {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(Fixtures(), "normalized", "*.transcript.jsonl"))
	out := map[string][]any{}
	for _, path := range matches {
		out[strings.SplitN(filepath.Base(path), ".", 2)[0]] = readJSONLines(t, path)
	}
	return out
}

// FixtureSpec reads checkpoints.json.
func FixtureSpec(t testing.TB) []Checkpoint {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Fixtures(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pyjson.Parse(string(data), true)
	if err != nil {
		t.Fatal(err)
	}
	var out []Checkpoint
	for _, item := range pyjson.AsObject(doc).Value("checkpoints").([]any) {
		c := pyjson.AsObject(item)
		cp := Checkpoint{Backend: c.Value("backend").(string), ID: c.Value("id").(string), Summary: pyjson.AsObject(c.Value("summary"))}
		for _, s := range c.Value("sessions").([]any) {
			so := pyjson.AsObject(s)
			session := Session{Metadata: pyjson.AsObject(so.Value("metadata"))}
			session.Transcript, _ = so.Value("transcript").(string)
			for _, line := range asList(so.Value("lines")) {
				if n, ok := line.(int64); ok {
					session.Lines = append(session.Lines, int(n))
				} else {
					session.Lines = append(session.Lines, line)
				}
			}
			cp.Sessions = append(cp.Sessions, session)
		}
		out = append(out, cp)
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// FixtureStore builds the shared synthetic store in repo.
func FixtureStore(t testing.TB, repo string) {
	t.Helper()
	Init(t, repo)
	Build(t, repo, FixtureSpec(t), nil)
}

// Metadata returns session metadata for a checkpoint. start sets
// checkpoint_transcript_start when positive. A nil compact leaves out
// compact_transcript_start.
func Metadata(cid, session string, start int, compact *int) *pyjson.Object {
	m := pyjson.Obj("cli_version", "0.13.2", "checkpoint_id", cid, "session_id", session, "strategy", "manual-commit",
		"created_at", "2026-10-01T10:00:00Z", "files_touched", []any{}, "agent", "Claude Code", "model", "")
	if start != 0 {
		m.Set("checkpoint_transcript_start", int64(start))
	}
	if compact != nil {
		m.Set("compact_transcript_start", int64(*compact))
	}
	return m
}

// Int returns a pointer to n.
func Int(n int) *int { return &n }

// Bool returns a pointer to b.
func Bool(b bool) *bool { return &b }

// ReadRecords parses an ASR file.
func ReadRecords(t testing.TB, path string) []*pyjson.Object {
	t.Helper()
	var out []*pyjson.Object
	for _, row := range readJSONLines(t, path) {
		out = append(out, pyjson.AsObject(row))
	}
	return out
}

// Meta returns a record's importer metadata.
func Meta(record *pyjson.Object) *pyjson.Object {
	m := pyjson.AsObject(record.Value("_meta"))
	if m == nil {
		return pyjson.NewObject()
	}
	inner := pyjson.AsObject(m.Value("org.agentsessionrecord.importers"))
	if inner == nil {
		return pyjson.NewObject()
	}
	return inner
}

// Keep copies a written ASR file into ENTIRE_ASR_TEST_OUTPUT, when set, so
// that CI can validate every test output with the ASR validator.
func Keep(t testing.TB, path string) {
	t.Helper()
	dir := os.Getenv("ENTIRE_ASR_TEST_OUTPUT")
	if dir == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "-" + filepath.Base(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// KeepBytes writes ASR bytes into ENTIRE_ASR_TEST_OUTPUT, when set.
func KeepBytes(t testing.TB, name string, data []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	Keep(t, tmp)
}
