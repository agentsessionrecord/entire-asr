package entire

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	et "github.com/agentsessionrecord/entire-asr/internal/entiretest"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

const claude = "01KA2SYNTHETC0000000000CA1"

var sessionsByAgent = map[string]string{"claude-code": "synthetic-claude-session", "codex": "synthetic-codex-session",
	"pi": "synthetic-pi-session", "opencode": "synthetic-opencode-session",
	"org.agentsessionrecord.entire-unknown": "synthetic-external-session"}

var testProducer = Producer{"entire-asr", "0.1.0"}

func store(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	et.FixtureStore(t, repo)
	return repo
}

func importAll(t *testing.T, repo string, o Options) []string {
	t.Helper()
	if o.Transcript == "" {
		o.Transcript = "auto"
	}
	if o.Producer.Name == "" {
		o.Producer = testProducer
	}
	outputs, err := ImportRepository(repo, filepath.Join(t.TempDir(), "out"), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range outputs {
		et.Keep(t, p)
	}
	return outputs
}

func byAgent(t *testing.T, outputs []string) map[string][]*pyjson.Object {
	out := map[string][]*pyjson.Object{}
	for _, p := range outputs {
		rows := et.ReadRecords(t, p)
		out[pyjson.AsObject(rows[0].Value("agent")).Value("name").(string)] = rows
	}
	return out
}

func missing(row *pyjson.Object) map[string]bool {
	set := map[string]bool{}
	for _, m := range row.Value("missing").([]any) {
		set[m.(string)] = true
	}
	return set
}

func ofType(rows []*pyjson.Object, kind string) []*pyjson.Object {
	var out []*pyjson.Object
	for _, r := range rows {
		if r.Value("type") == kind {
			out = append(out, r)
		}
	}
	return out
}

func checkpointsOf(row *pyjson.Object) []*pyjson.Object {
	var out []*pyjson.Object
	for _, c := range et.Meta(row).Value("checkpoints").([]any) {
		out = append(out, pyjson.AsObject(c))
	}
	return out
}

func TestBothBackendsFourAgentsAndOneSessionPerFile(t *testing.T) {
	repo := store(t)
	refsBefore := et.Git(t, repo, nil, nil, "for-each-ref")
	sessions := byAgent(t, importAll(t, repo, Options{}))
	got := map[string]string{}
	for name, rows := range sessions {
		got[name] = rows[0].Value("sessionId").(string)
	}
	if !reflect.DeepEqual(got, sessionsByAgent) {
		t.Fatalf("sessions %v", got)
	}
	backends := func(name string) []string {
		var out []string
		for _, c := range checkpointsOf(sessions[name][0]) {
			out = append(out, c.Value("backend").(string))
		}
		return out
	}
	if b := backends("claude-code"); b[0] != "git-refs" || b[len(b)-1] != "git-refs" {
		t.Fatalf("claude backends %v", b)
	}
	if b := backends("codex"); b[0] != "git-branch" {
		t.Fatalf("codex backends %v", b)
	}
	for _, rows := range sessions {
		producer := pyjson.AsObject(rows[0].Value("producer"))
		want := pyjson.Obj("name", "entire-asr", "version", "0.1.0", "method", "converter", "source", "entire-transcript-v1")
		if !pyjson.Equal(producer, want) {
			t.Fatalf("producer %s", pyjson.PyRepr(producer))
		}
		// Every checkpoint repeats earlier lines. Each prompt appears once.
		seen := map[string]bool{}
		for _, row := range ofType(rows, "message") {
			if row.Value("role") != "user" {
				continue
			}
			for _, part := range row.Value("content").([]any) {
				p := pyjson.AsObject(part)
				if p.Value("type") == "text" {
					text := p.Value("text").(string)
					if seen[text] {
						t.Fatalf("prompt repeated: %q", text)
					}
					seen[text] = true
				}
			}
		}
	}
	if et.Git(t, repo, nil, nil, "for-each-ref") != refsBefore || et.Git(t, repo, nil, nil, "status", "--porcelain") != "" {
		t.Fatal("the import changed the repository")
	}
}

func TestCompactTranscriptStartMergesWithoutLossOrDoubleCounting(t *testing.T) {
	rows := byAgent(t, importAll(t, store(t), Options{}))["claude-code"]
	var ids []string
	for _, c := range checkpointsOf(rows[0]) {
		ids = append(ids, c.Value("checkpoint").(string))
	}
	if want := []string{claude, "01KA2SYNTHETC0000000000CB2", "01KA2SYNTHETC0000000000CC3"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("checkpoints %v", ids)
	}
	inferences := ofType(rows, "inference")
	var inferenceIDs []string
	var outputTokens int64
	for _, r := range inferences {
		inferenceIDs = append(inferenceIDs, r.Value("id").(string))
		outputTokens += pyjson.AsObject(r.Value("usage")).Value("outputTokens").(int64)
	}
	if want := []string{"msg_synthetic_a", "msg_synthetic_b", "msg_synthetic_c", "msg_synthetic_d"}; !reflect.DeepEqual(inferenceIDs, want) {
		t.Fatalf("inferences %v", inferenceIDs)
	}
	// The straddling message keeps its final usage, not the earlier partial one.
	want := pyjson.Obj("inputTokens", int64(16), "cacheReadTokens", int64(4), "cacheWriteTokens", int64(2), "outputTokens", int64(9))
	if !pyjson.Equal(inferences[0].Value("usage"), want) {
		t.Fatalf("usage %s", pyjson.PyRepr(inferences[0].Value("usage")))
	}
	if outputTokens != 9+4+6+7 {
		t.Fatalf("output tokens %d", outputTokens)
	}
	// The Edit result arrived after its checkpoint. A later view holds it.
	results := map[string]*pyjson.Object{}
	for _, r := range ofType(rows, "tool_result") {
		results[r.Value("callId").(string)] = r
	}
	edit := pyjson.MustDumps(results["toolu_synthetic_edit"].Value("content"), pyjson.Options{})
	if edit != `[{"type": "text", "text": "The file was updated."}]` {
		t.Fatalf("edit result %s", edit)
	}
	if results["toolu_synthetic_check"].Value("status") != "failed" {
		t.Fatal("check status")
	}
	first := map[string]string{}
	var calls []string
	for _, r := range ofType(rows, "tool_call") {
		calls = append(calls, r.Value("callId").(string))
		if !pyjson.Truthy(r.Value("inferenceId")) {
			t.Fatal("tool call without inference")
		}
		// Each record names the checkpoint whose slice first held it.
		first[r.Value("callId").(string)] = et.Meta(r).Value("checkpoint").(string)
	}
	if want := []string{"toolu_synthetic_read", "toolu_synthetic_edit", "toolu_synthetic_check"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v", calls)
	}
	if want := map[string]string{"toolu_synthetic_read": claude, "toolu_synthetic_edit": "01KA2SYNTHETC0000000000CB2",
		"toolu_synthetic_check": "01KA2SYNTHETC0000000000CC3"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first checkpoints %v", first)
	}
	m := missing(rows[0])
	if m["usage"] || m["model"] {
		t.Fatal("usage and model should be captured")
	}
	for _, name := range []string{"reasoning", "assets", "content", "message_origin", "checkpoint_history"} {
		if !m[name] {
			t.Fatalf("%s not declared", name)
		}
	}
}

func TestDivergentViewsKeepEarlierContentAfterAGap(t *testing.T) {
	rows := byAgent(t, importAll(t, store(t), Options{}))["pi"]
	gap := -1
	for i, r := range rows {
		if r.Value("type") == "gap" {
			gap = i
			break
		}
	}
	if gap < 0 || !missing(rows[0])["checkpoint_order"] {
		t.Fatal("expected a gap and checkpoint_order")
	}
	kept := ofType(rows[gap:], "message")
	text := pyjson.AsObject(kept[0].Value("content").([]any)[0]).Value("text")
	if text != "An abandoned branch reply." {
		t.Fatalf("kept %v", text)
	}
	if n := len(ofType(rows, "inference")); n != 4 {
		t.Fatalf("inferences %d", n)
	}
}

func TestMigratedCopyPrefersTheCheckpointRef(t *testing.T) {
	rows := byAgent(t, importAll(t, store(t), Options{}))["opencode"]
	header := et.Meta(rows[0])
	if c := checkpointsOf(rows[0]); len(c) != 1 || c[0].Value("backend") != "git-refs" {
		t.Fatal("expected the ref copy")
	}
	shadowed := pyjson.AsObject(header.Value("shadowedCopies").([]any)[0])
	if shadowed.Value("backend") != "git-branch" {
		t.Fatal("expected the branch copy to be shadowed")
	}
	last := pyjson.AsObject(rows[len(rows)-1].Value("content").([]any)[0])
	if last.Value("text") != "There are two files." {
		t.Fatalf("last %v", last.Value("text"))
	}
	// OpenCode lines count tokens per message, not per model call.
	if len(ofType(rows, "inference")) != 0 || !missing(rows[0])["usage"] || !missing(rows[0])["model"] {
		t.Fatal("OpenCode counts must stay in metadata")
	}
}

func TestExternalAgentInAMultiSessionCheckpoint(t *testing.T) {
	rows := byAgent(t, importAll(t, store(t), Options{}))[UnknownAgent]
	if et.Meta(rows[0]).Value("agent") != "Acme Agent" {
		t.Fatal("native agent name")
	}
	if checkpointsOf(rows[0])[0].Value("session") != int64(1) {
		t.Fatal("session index")
	}
	if len(rows) != 3 || rows[1].Value("type") != "message" || rows[2].Value("type") != "message" {
		t.Fatalf("records %d", len(rows))
	}
}

func encodeNormalized(t *testing.T, agent string, data []byte) []byte {
	t.Helper()
	stream, err := NewStream(agent, "synthetic-"+agent+"-session", "entire-transcript-v1", nil, ImporterProducer)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := MapLines(stream, agent, Lines(lines), nil); err != nil {
		t.Fatal(err)
	}
	out, err := stream.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The ASR importer's documented mappings, byte for byte.
func TestDocumentedNormalizedMapping(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex", "pi", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			source, _ := os.ReadFile(filepath.Join(et.Fixtures(), "normalized", agent+".transcript.jsonl"))
			want, _ := os.ReadFile(filepath.Join(et.Fixtures(), "normalized", agent+".expected.asr.jsonl"))
			got := encodeNormalized(t, agent, source)
			et.KeepBytes(t, agent+".jsonl", got)
			if !bytes.Equal(got, want) {
				t.Fatalf("mapping differs\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestDocumentedRawMappingSample(t *testing.T) {
	stream, err := asr.NewStream("claude-code", "synthetic-entire-session", "entire-checkpoints-v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(et.Fixtures(), "full.jsonl"))
	if err := NativeTranscript(stream, data, "claude-code"); err != nil {
		t.Fatal(err)
	}
	got, err := stream.Encode()
	if err != nil {
		t.Fatal(err)
	}
	et.KeepBytes(t, "raw.jsonl", got)
	want, _ := os.ReadFile(filepath.Join(et.Fixtures(), "full.expected.asr.jsonl"))
	if !bytes.Equal(got, want) {
		t.Fatalf("raw mapping differs\n got %s\nwant %s", got, want)
	}
}

func TestCodexAndPiUsageRules(t *testing.T) {
	transcripts := et.LoadTranscripts(t)
	stream, _ := asr.NewStream("codex", "synthetic", "test", nil)
	mapper := NewMapper(stream, "codex")
	for _, line := range transcripts["codex"] {
		if err := mapper.Line(nil, pyjson.AsObject(line), pyjson.NewObject()); err != nil {
			t.Fatal(err)
		}
	}
	var inputs []int64
	inferences := ofType(stream.Records, "inference")
	for _, r := range inferences {
		inputs = append(inputs, pyjson.AsObject(r.Value("usage")).Value("inputTokens").(int64))
	}
	// Codex fresh input excludes cached input. A line without usage is no call.
	if !reflect.DeepEqual(inputs, []int64{500, 800, 820}) {
		t.Fatalf("inputs %v", inputs)
	}
	if !pyjson.Equal(inferences[0].Value("model"), pyjson.Obj("requested", "synthetic-codex-model")) {
		t.Fatal("codex model")
	}
	old := pyjson.AsObject(transcripts["claude-code"][1]).Copy()
	old.Set("cli_version", "0.11.6")
	stream, _ = asr.NewStream("claude-code", "synthetic", "test", nil)
	if err := NewMapper(stream, "claude-code").Line(nil, old, pyjson.NewObject()); err != nil {
		t.Fatal(err)
	}
	// Older Claude Code lines hold fresh input only, so the total is unknown.
	if got := ofType(stream.Records, "inference")[0].Value("usage"); !pyjson.Equal(got, pyjson.Obj("outputTokens", int64(9))) {
		t.Fatalf("usage %s", pyjson.PyRepr(got))
	}
}

func TestCorruptAndIncompleteNormalizedLinesBecomeGaps(t *testing.T) {
	stream, _ := asr.NewStream("pi", "synthetic", "test", nil)
	mapper := NewMapper(stream, "pi")
	lines, _ := Parse([]byte("{\"bad\":\n{\"v\":2,\"type\":\"user\",\"content\":[]}\n{\"v\":1,\"type\":\"user\",\"content\":[{\"text\":\"cut"))
	for _, l := range lines {
		if err := mapper.Line(l.Raw, l.Line, pyjson.NewObject()); err != nil {
			t.Fatal(err)
		}
	}
	out, err := stream.Encode()
	if err != nil {
		t.Fatal(err)
	}
	et.KeepBytes(t, "corrupt.jsonl", out)
	for _, r := range stream.Records {
		if r.Value("type") != "gap" {
			t.Fatal("expected only gaps")
		}
	}
	if len(stream.Records) != 3 {
		t.Fatalf("records %d", len(stream.Records))
	}
}

func TestLegacyDeltaCheckpointsConcatenate(t *testing.T) {
	repo := et.Init(t, filepath.Join(t.TempDir(), "repo"))
	lines := et.LoadTranscripts(t)["claude-code"]
	et.Build(t, repo, []et.Checkpoint{
		{Backend: "git-branch", ID: "aa0000000001", Sessions: []et.Session{{Metadata: et.Metadata("aa0000000001", "legacy", 0, nil), Lines: lines[:4]}}},
		{Backend: "git-branch", ID: "aa0000000002", Sessions: []et.Session{{Metadata: et.Metadata("aa0000000002", "legacy", 5, nil), Lines: lines[4:]}}},
	}, nil)
	rows := et.ReadRecords(t, importAll(t, repo, Options{})[0])
	if !missing(rows[0])["checkpoint_deltas"] || len(ofType(rows, "inference")) != 4 {
		t.Fatal("legacy deltas")
	}
}

func fullFixture(t *testing.T) []byte {
	data, err := os.ReadFile(filepath.Join(et.Fixtures(), "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAutoReadsFullJSONLWithoutANormalizedTranscript(t *testing.T) {
	repo := et.Init(t, filepath.Join(t.TempDir(), "repo"))
	et.Build(t, repo, []et.Checkpoint{{Backend: "git-refs", ID: claude, Sessions: []et.Session{
		{Metadata: et.Metadata(claude, "synthetic-entire-session", 0, et.Int(0)), Full: fullFixture(t), Compact: et.Bool(false)}}}}, nil)
	rows := et.ReadRecords(t, importAll(t, repo, Options{})[0])
	if pyjson.AsObject(rows[0].Value("producer")).Value("source") != "entire-full-jsonl" {
		t.Fatal("expected the raw transcript")
	}
	if pyjson.AsObject(ofType(rows, "inference")[0].Value("usage")).Value("inputTokens") != int64(15) {
		t.Fatal("raw usage")
	}
	rows = et.ReadRecords(t, importAll(t, repo, Options{Transcript: "normalized"})[0])
	if len(rows) != 2 || rows[1].Value("type") != "gap" {
		t.Fatal("normalized choice should leave a gap")
	}
}

func TestFullTranscriptChoiceReadsRawSnapshots(t *testing.T) {
	repo := et.Init(t, filepath.Join(t.TempDir(), "raw"))
	full := fullFixture(t)
	first := full[:bytes.IndexByte(full, '\n')+1]
	et.Build(t, repo, []et.Checkpoint{
		{Backend: "git-branch", ID: "ab1234567890", Sessions: []et.Session{{Metadata: et.Metadata("ab1234567890", "synthetic-entire-session", 0, et.Int(0)), Full: first, Lines: []any{}}}},
		{Backend: "git-branch", ID: "cd1234567890", Sessions: []et.Session{{Metadata: et.Metadata("cd1234567890", "synthetic-entire-session", 1, et.Int(1)), Full: full, Lines: []any{}}}},
	}, nil)
	rows := et.ReadRecords(t, importAll(t, repo, Options{Transcript: "full"})[0])
	if pyjson.AsObject(rows[0].Value("producer")).Value("source") != "entire-full-jsonl" || len(ofType(rows, "inference")) != 1 {
		t.Fatal("full choice")
	}
}

func TestRawTranscriptChunks(t *testing.T) {
	repo := et.Init(t, filepath.Join(t.TempDir(), "repo"))
	full := fullFixture(t)
	cut := bytes.IndexByte(full, '\n') + 1
	et.Build(t, repo, []et.Checkpoint{{Backend: "git-branch", ID: "ab1234567890", Sessions: []et.Session{
		{Metadata: et.Metadata("ab1234567890", "synthetic-entire-session", 0, et.Int(0)), Full: full[:cut], Compact: et.Bool(false)}}}}, nil)
	// Rebuild the tree from object IDs and add a numbered chunk.
	gitDir := et.Git(t, repo, nil, nil, "rev-parse", "--absolute-git-dir")
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(gitDir, "chunk-index")}
	for _, p := range strings.Fields(et.Git(t, repo, nil, nil, "ls-tree", "-r", "--name-only", et.Branch)) {
		oid := et.Git(t, repo, nil, nil, "rev-parse", et.Branch+":"+p)
		et.Git(t, repo, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+oid+","+p)
	}
	oid := et.Git(t, repo, full[cut:], nil, "hash-object", "-w", "--stdin")
	et.Git(t, repo, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+oid+",ab/1234567890/0/full.jsonl.001")
	commit := et.Git(t, repo, nil, nil, "commit-tree", et.Git(t, repo, nil, env, "write-tree"), "-p", et.Branch, "-m", "Synthetic chunk")
	et.Git(t, repo, nil, nil, "update-ref", et.Branch, commit)
	rows := et.ReadRecords(t, importAll(t, repo, Options{})[0])
	if len(ofType(rows, "inference")) == 0 {
		t.Fatal("chunks were not joined")
	}
}

func TestPointerEscapeAndRefNames(t *testing.T) {
	if _, err := CheckedPath("/ab/1234567890/../outside", "ab/1234567890", ""); err == nil {
		t.Fatal("traversal accepted")
	}
	if p, _ := CheckedPath("/0/metadata.json", "", ""); p != "0/metadata.json" {
		t.Fatal(p)
	}
	if p, _ := CheckedPath("/ab/1234567890/0/full.jsonl", "", "ab/1234567890"); p != "0/full.jsonl" {
		t.Fatal(p)
	}
	if _, err := CheckedPath("/../metadata.json", "", ""); err == nil {
		t.Fatal("parent accepted")
	}
	if c, ok := CheckpointFromRef("refs/entire/checkpoints/A1/" + claude); !ok || c != claude {
		t.Fatal("ref parse")
	}
	if _, ok := CheckpointFromRef("refs/entire/checkpoints/ZZ/" + claude); ok {
		t.Fatal("wrong shard accepted")
	}
	if _, ok := CheckpointFromRef("refs/entire/checkpoints/90/ab1234567890/extra"); ok {
		t.Fatal("extra component accepted")
	}
	repo := et.Init(t, filepath.Join(t.TempDir(), "repo"))
	et.Build(t, repo, []et.Checkpoint{{Backend: "git-refs", ID: claude,
		Summary:  pyjson.Obj("sessions", []any{pyjson.Obj("metadata", "/../outside.json")}),
		Sessions: []et.Session{{Metadata: et.Metadata(claude, "escape", 0, et.Int(0)), Lines: []any{}}}}}, nil)
	rejected := filepath.Join(t.TempDir(), "rejected")
	if _, err := ImportRepository(repo, rejected, Options{Transcript: "auto", Producer: testProducer}); err == nil {
		t.Fatal("escaping pointer accepted")
	}
	if _, err := os.Stat(rejected); err == nil {
		t.Fatal("output written for a rejected store")
	}
}

func TestExistingOutputsAreNeverOverwritten(t *testing.T) {
	repo := store(t)
	out := filepath.Join(t.TempDir(), "out")
	o := Options{Transcript: "auto", Producer: testProducer}
	if _, err := ImportRepository(repo, out, o); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportRepository(repo, out, o); err == nil || err.Error() != "an output session already exists" {
		t.Fatalf("second import: %v", err)
	}
}

func TestCorruptAndIncompleteNativeLinesBecomeGaps(t *testing.T) {
	stream, _ := asr.NewStream("claude-code", "synthetic", "test", nil)
	if err := NativeTranscript(stream, []byte("{\"bad\":\n{}"), "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Encode(); err != nil {
		t.Fatal(err)
	}
	for _, r := range stream.Records {
		if r.Value("type") != "gap" {
			t.Fatal("expected gaps")
		}
	}
}

func TestNativeSnapshotsCompleteAfterInterveningRecords(t *testing.T) {
	var rows []any
	for _, line := range strings.Split(strings.TrimSpace(string(fullFixture(t))), "\n") {
		v, _ := pyjson.Parse(line, true)
		rows = append(rows, v)
	}
	clone := func(v any) *pyjson.Object {
		o, _ := pyjson.Parse(pyjson.MustDumps(v, pyjson.Options{}), true)
		return o.(*pyjson.Object)
	}
	initial, final := clone(rows[1]), clone(rows[1])
	pyjson.AsObject(pyjson.AsObject(initial.Value("message")).Value("usage")).Set("output_tokens", int64(1))
	pyjson.AsObject(pyjson.AsObject(final.Value("message")).Value("usage")).Set("output_tokens", int64(4))
	notice := pyjson.Obj("type", "user", "uuid", "synthetic-notice", "message", pyjson.Obj("role", "user", "content", "Intervening notice."))
	stream, _ := asr.NewStream("claude-code", "synthetic", "test", nil)
	if err := NativeTranscript(stream, et.JSONL([]any{initial, notice, final}), "claude-code"); err != nil {
		t.Fatal(err)
	}
	out, err := stream.Encode()
	if err != nil {
		t.Fatal(err)
	}
	et.KeepBytes(t, "completed.jsonl", out)
	if stream.Records[0].Value("id") != "synthetic-notice" || len(ofType(stream.Records, "inference")) != 1 {
		t.Fatal("snapshot completion")
	}
	if pyjson.AsObject(stream.Records[len(stream.Records)-1].Value("usage")).Value("outputTokens") != int64(4) {
		t.Fatal("final usage")
	}
}

func TestDiscoverGroupsCopiesBySession(t *testing.T) {
	sessions, err := Discover(&Reader{Repo: store(t)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, s := range sessions {
		got[s.SessionID] = s.Checkpoints()
	}
	if want := []string{claude, "01KA2SYNTHETC0000000000CB2", "01KA2SYNTHETC0000000000CC3"}; !reflect.DeepEqual(got["synthetic-claude-session"], want) {
		t.Fatalf("claude %v", got["synthetic-claude-session"])
	}
	if want := []string{"c0de0000a001", "c0de0000a002"}; !reflect.DeepEqual(got["synthetic-codex-session"], want) {
		t.Fatalf("codex %v", got["synthetic-codex-session"])
	}
}

func TestNamespaceChangesIDsAndIsRecorded(t *testing.T) {
	ns := "machine-a"
	rows := byAgent(t, importAll(t, store(t), Options{Namespace: &ns}))["codex"]
	if rows[0].Value("namespace") != ns {
		t.Fatal("namespace not recorded")
	}
	plain := byAgent(t, importAll(t, store(t), Options{}))["codex"]
	if rows[1].Value("id") == plain[1].Value("id") && strings.HasPrefix(plain[1].Value("id").(string), "asr-") {
		t.Fatal("derived IDs should depend on the namespace")
	}
}
