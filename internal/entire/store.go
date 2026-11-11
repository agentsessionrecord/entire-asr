// Package entire reads Entire checkpoints from a repository's Git objects and
// maps them to Agent Session Record streams. It follows the ASR Entire
// importer (importers/entire in agentsessionrecord/asr) exactly: the same Git
// reads, session grouping, checkpoint merge, records and declared losses.
package entire

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

const (
	// Branch is the git-branch backend's local branch.
	Branch = "refs/heads/entire/checkpoints/v1"
	// Refs is the git-refs backend's ref prefix.
	Refs = "refs/entire/checkpoints/"
)

var (
	hexID         = regexp.MustCompile(`\A[0-9a-f]{12}\z`)
	ulidID        = regexp.MustCompile(`\A[0-7][0-9ABCDEFGHJKMNPQRSTVWXYZ]{25}\z`)
	branchSummary = regexp.MustCompile(`\A([0-9a-f]{2})/([0-9a-f]{10})/metadata\.json\z`)
)

func devNull() string {
	if runtime.GOOS == "windows" {
		return "nul"
	}
	return os.DevNull
}

// GitError is a failed or oversized Git read.
type GitError struct{ Msg string }

func (e *GitError) Error() string { return e.Msg }

// Git runs a read-only Git command in repo. Inherited Git variables, system
// and global config, replace refs, hooks and the file system monitor are
// turned off, and the output is capped at 32 MiB.
func Git(repo string, args ...string) ([]byte, error) {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if runtime.GOOS == "windows" {
			name = strings.ToUpper(name)
		}
		if !strings.HasPrefix(name, "GIT_") {
			env = append(env, entry)
		}
	}
	null := devNull()
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+null, "GIT_NO_LAZY_FETCH=1")
	limit, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	full := append([]string{"--no-optional-locks", "--no-replace-objects", "-c", "core.fsmonitor=false",
		"-c", "log.showSignature=false", "-c", "core.hooksPath=" + null, "-C", repo}, args...)
	cmd := exec.CommandContext(limit, "git", full...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stderr = nil
	out := &capped{limit: asr.MaxInputBytes}
	cmd.Stdout = out
	err := cmd.Run()
	if limit.Err() == context.DeadlineExceeded {
		return nil, &GitError{"local checkpoint Git read timed out"}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, err
	}
	if err != nil || out.over {
		return nil, &GitError{"local checkpoint Git read failed or exceeded byte limit"}
	}
	return out.buf.Bytes(), nil
}

// capped keeps up to limit bytes and records whether more arrived.
type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); len(p) > room {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.over = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// RepositoryRoot returns the top level of the repository containing start.
func RepositoryRoot(start string) (string, error) {
	out, err := Git(start, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	text, err := pyjson.DecodeStrict(out)
	if err != nil {
		return "", err
	}
	return pyjson.Strip(text), nil
}

// CheckedPath resolves a summary pointer inside its checkpoint subtree or
// rejects it.
func CheckedPath(value any, prefix, strip string) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", &pyjson.ValueError{Msg: "checkpoint pointer must be a Git path"}
	}
	p := strings.TrimLeft(s, "/")
	// Migration rewrites branch pointers to the ref root. Accept one that kept
	// its branch prefix, since it still names a path in the same subtree.
	if strip != "" && strings.HasPrefix(p, strip+"/") {
		p = p[len(strip)+1:]
	}
	inside := p != ""
	if prefix != "" {
		inside = strings.HasPrefix(p, prefix+"/")
	}
	bad := strings.ContainsAny(p, "\\\x00") || !inside
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			bad = true
		}
	}
	if bad {
		return "", &pyjson.ValueError{Msg: "checkpoint pointer escapes its checkpoint subtree"}
	}
	return p, nil
}

// CheckpointFromRef parses refs/entire/checkpoints/<shard>/<id> as Entire's ParseRef does.
func CheckpointFromRef(name string) (string, bool) {
	if !strings.HasPrefix(name, Refs) {
		return "", false
	}
	parts := strings.Split(name[len(Refs):], "/")
	if len(parts) != 2 {
		return "", false
	}
	shard, checkpoint := parts[0], parts[1]
	if !hexID.MatchString(checkpoint) && !ulidID.MatchString(checkpoint) {
		return "", false
	}
	if strings.ToLower(shard) != strings.ToLower(checkpoint[len(checkpoint)-2:]) {
		return "", false
	}
	return checkpoint, true
}

// Blob is a stored transcript: one object, or a raw transcript with its
// numbered chunks.
type Blob struct {
	Path string
	OIDs []string
	Size int64
}

// SessionEntry is one session slot in one stored copy of a checkpoint.
type SessionEntry struct {
	Checkpoint   string
	Backend      string
	Revision     string
	Index        int
	MetadataPath string
	Metadata     *pyjson.Object
	Summary      *pyjson.Object
	Full         *Blob
	Compact      *Blob
	SessionID    string
}

// CompactStart is compact_transcript_start when it is a non-negative integer.
func (e *SessionEntry) CompactStart() (*big.Int, bool) {
	v := e.Metadata.Value("compact_transcript_start")
	n, ok := pyjson.Int(v)
	if !ok || n.Sign() < 0 {
		return nil, false
	}
	return n, true
}

// TranscriptStart is Entire's position marker for ordering a session's checkpoints.
func (e *SessionEntry) TranscriptStart() *big.Int {
	for _, key := range []string{"checkpoint_transcript_start", "transcript_lines_at_start"} {
		if n, ok := pyjson.Int(e.Metadata.Value(key)); ok && n.Sign() > 0 {
			return n
		}
	}
	return big.NewInt(0)
}

func blobPath(b *Blob) any {
	if b == nil {
		return nil
	}
	return b.Path
}

// Describe is the checkpoint description kept in header metadata.
func (e *SessionEntry) Describe() *pyjson.Object {
	d := pyjson.Obj("checkpoint", e.Checkpoint, "backend", e.Backend, "revision", e.Revision,
		"session", int64(e.Index), "metadataPath", e.MetadataPath,
		"fullTranscript", blobPath(e.Full), "normalizedTranscript", blobPath(e.Compact),
		"metadata", e.Metadata)
	for _, key := range []string{"commit_sha", "linked_commits"} {
		if v, ok := e.Summary.Get(key); ok {
			d.Set(key, v)
		}
	}
	return d
}

// Reader lists checkpoint sessions and reads their blobs within one byte
// budget per session.
type Reader struct {
	Repo   string
	Budget int64
}

// ByteLimit is the error for a session over its checkpoint byte budget.
var ByteLimit = &pyjson.ValueError{Msg: "checkpoint_byte_limit"}

// ReadBlob reads a blob, or returns nil for an absent one.
func (r *Reader) ReadBlob(item *Blob) ([]byte, error) {
	if item == nil {
		return nil, nil
	}
	var data []byte
	for _, oid := range item.OIDs {
		part, err := Git(r.Repo, "cat-file", "blob", oid)
		if err != nil {
			return nil, err
		}
		data = append(data, part...)
	}
	r.Budget += int64(len(data))
	if len(data) > asr.MaxInputBytes || r.Budget > asr.MaxInputBytes*4 {
		return nil, ByteLimit
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

type treeEntry struct {
	oid  string
	size int64
}

type tree struct {
	paths []string
	items map[string]treeEntry
}

func (r *Reader) tree(revision string) (*tree, error) {
	out, err := Git(r.Repo, "ls-tree", "-rlz", "--full-tree", revision)
	if err != nil {
		return nil, err
	}
	t := &tree{items: map[string]treeEntry{}}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		info, name, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			return nil, &pyjson.ValueError{Msg: "not enough values to unpack (expected 2, got 1)"}
		}
		fields := strings.Fields(string(info))
		if len(fields) != 4 {
			return nil, &pyjson.ValueError{Msg: "unexpected Git tree entry"}
		}
		if fields[1] != "blob" || fields[0] != "100644" {
			continue
		}
		p, err := pyjson.DecodeStrict(name)
		if err != nil {
			return nil, err
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, &pyjson.ValueError{Msg: "invalid literal for int() with base 10: '" + fields[3] + "'"}
		}
		if _, seen := t.items[p]; !seen {
			t.paths = append(t.paths, p)
		}
		t.items[p] = treeEntry{fields[2], size}
	}
	return t, nil
}

type ref struct {
	backend, name, revision string
}

func (r *Reader) refs() ([]ref, error) {
	out, err := Git(r.Repo, "for-each-ref", "--format=%(objectname) %(objecttype) %(refname)", Branch, Refs)
	if err != nil {
		return nil, err
	}
	text, err := pyjson.DecodeStrict(out)
	if err != nil {
		return nil, err
	}
	var found []ref
	for _, line := range splitLines(text) {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return nil, &pyjson.ValueError{Msg: "unexpected Git ref listing"}
		}
		oid, kind, name := parts[0], parts[1], parts[2]
		if kind != "commit" {
			continue
		}
		if name == Branch {
			found = append(found, ref{"git-branch", name, oid})
		} else if _, ok := CheckpointFromRef(name); ok {
			found = append(found, ref{"git-refs", name, oid})
		}
	}
	return found, nil
}

// splitLines is Python's str.splitlines.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, w := pyjson.DecodeRune(s[i:])
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			if r == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				w = 2
			}
			start = i + w
		}
		i += w
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Sessions returns every session slot of every local checkpoint copy.
func (r *Reader) Sessions() ([]*SessionEntry, error) {
	refs, err := r.refs()
	if err != nil {
		return nil, err
	}
	var result []*SessionEntry
	for _, rf := range refs {
		t, err := r.tree(rf.revision)
		if err != nil {
			return nil, err
		}
		if rf.backend == "git-branch" {
			for _, p := range t.paths {
				m := branchSummary.FindStringSubmatch(p)
				if m == nil {
					continue
				}
				entries, err := r.checkpoint(t, m[1]+m[2], rf.backend, rf.revision, m[1]+"/"+m[2], "")
				if err != nil {
					return nil, err
				}
				result = append(result, entries...)
			}
		} else if _, ok := t.items["metadata.json"]; ok {
			checkpoint, _ := CheckpointFromRef(rf.name)
			entries, err := r.checkpoint(t, checkpoint, rf.backend, rf.revision, "", checkpoint[:2]+"/"+checkpoint[2:])
			if err != nil {
				return nil, err
			}
			result = append(result, entries...)
		}
	}
	return result, nil
}

func (r *Reader) jsonBlob(t *tree, p string) (*pyjson.Object, bool, error) {
	item, ok := t.items[p]
	if !ok {
		return nil, false, nil
	}
	data, err := Git(r.Repo, "cat-file", "blob", item.oid)
	if err != nil {
		return nil, false, err
	}
	text, err := pyjson.DecodeStrict(data)
	if err != nil {
		return nil, false, err
	}
	if pyjson.RuneLen(text) > asr.MaxInputBytes {
		return nil, false, &pyjson.ValueError{Msg: "input_byte_limit"}
	}
	value, err := pyjson.Parse(text, true)
	if err != nil {
		return nil, false, err
	}
	o, isObj := value.(*pyjson.Object)
	if !isObj {
		return nil, false, &pyjson.ValueError{Msg: "invalid checkpoint metadata"}
	}
	return o, true, nil
}

// digitsValue reads a run of Unicode decimal digits as Python's int() does.
func digitsValue(s string) (*big.Int, bool, error) {
	var b strings.Builder
	for _, c := range s {
		if !pyjson.IsDigit(c) {
			return nil, false, nil
		}
		b.WriteRune('0' + digitOf(c))
	}
	if b.Len() > 4300 {
		return nil, false, &pyjson.ValueError{Msg: "Exceeds the limit (4300 digits) for integer string conversion"}
	}
	n, _ := new(big.Int).SetString(b.String(), 10)
	return n, true, nil
}

// digitOf returns the value of a Unicode decimal digit. Decimal digits come
// in runs of ten that start at zero, and some runs are adjacent.
func digitOf(c rune) rune {
	start := c
	for pyjson.IsDigit(start - 1) {
		start--
	}
	return (c - start) % 10
}

func (r *Reader) blob(t *tree, p string, chunked bool) (*Blob, error) {
	item, ok := t.items[p]
	if !ok {
		return nil, nil
	}
	b := &Blob{Path: p, OIDs: []string{item.oid}, Size: item.size}
	if !chunked {
		return b, nil
	}
	// Entire numbers additional raw chunks as full.jsonl.001, .002.
	type chunk struct {
		name string
		n    *big.Int
	}
	var chunks []chunk
	for _, name := range t.paths {
		if !strings.HasPrefix(name, p+".") {
			continue
		}
		suffix := name[len(p)+1:]
		if pyjson.RuneLen(suffix) < 3 {
			continue
		}
		n, isDigits, err := digitsValue(suffix)
		if err != nil {
			return nil, err
		}
		if isDigits {
			chunks = append(chunks, chunk{name, n})
		}
	}
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].n.Cmp(chunks[j].n) < 0 })
	for i, c := range chunks {
		if c.name != p+"."+pad3(i+1) {
			return nil, &pyjson.ValueError{Msg: "checkpoint transcript chunks are incomplete"}
		}
		b.OIDs = append(b.OIDs, t.items[c.name].oid)
		b.Size += t.items[c.name].size
	}
	return b, nil
}

func pad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func (r *Reader) checkpoint(t *tree, checkpoint, backend, revision, prefix, strip string) ([]*SessionEntry, error) {
	base := ""
	if prefix != "" {
		base = prefix + "/"
	}
	root := base + "metadata.json"
	summary, _, err := r.jsonBlob(t, root)
	if err != nil {
		return nil, err
	}
	sessions, isList := summary.Value("sessions").([]any)
	if !isList {
		// Legacy single-session checkpoints keep metadata at the root.
		sessions = []any{pyjson.Obj("metadata", root, "transcript", base+"full.jsonl")}
	}
	var result []*SessionEntry
	for index, item := range sessions {
		pointers, ok := item.(*pyjson.Object)
		if !ok {
			return nil, &pyjson.ValueError{Msg: "invalid checkpoint session pointer"}
		}
		metadataPath, err := CheckedPath(pointers.Value("metadata"), prefix, strip)
		if err != nil {
			return nil, err
		}
		metadata, found, err := r.jsonBlob(t, metadataPath)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &pyjson.ValueError{Msg: "checkpoint session metadata is absent"}
		}
		directory := path.Dir(metadataPath)
		fullValue, ok := pointers.Get("transcript")
		if !ok {
			fullValue = directory + "/full.jsonl"
		}
		fullPath, err := CheckedPath(fullValue, prefix, strip)
		if err != nil {
			return nil, err
		}
		// Older writers left out the pointer. The file name is Entire's own.
		compactValue, ok := pointers.Get("compact_transcript")
		if !ok {
			compactValue = directory + "/transcript.jsonl"
		}
		compactPath, err := CheckedPath(compactValue, prefix, strip)
		if err != nil {
			return nil, err
		}
		full, err := r.blob(t, fullPath, true)
		if err != nil {
			return nil, err
		}
		compact, err := r.blob(t, compactPath, false)
		if err != nil {
			return nil, err
		}
		session, ok := pyjson.NonEmptyStr(metadata.Value("session_id"))
		if !ok {
			return nil, &pyjson.ValueError{Msg: "checkpoint metadata lacks its native session ID"}
		}
		result = append(result, &SessionEntry{Checkpoint: checkpoint, Backend: backend, Revision: revision,
			Index: index, MetadataPath: metadataPath, Metadata: metadata, Summary: summary,
			Full: full, Compact: compact, SessionID: session})
	}
	return result, nil
}
