package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	et "github.com/agentsessionrecord/entire-asr/internal/entiretest"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

const claude = "01KA2SYNTHETC0000000000CA1"

func store(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	et.FixtureStore(t, repo)
	return repo
}

type result struct {
	code           int
	stdout, stderr []byte
}

// plugin runs the command as Entire does. An empty repo leaves
// ENTIRE_REPO_ROOT unset.
func plugin(t *testing.T, repo string, args ...string) result {
	t.Helper()
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, "ENTIRE_") && name != "ENTIRE_ASR_TEST_OUTPUT" {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	if repo != "" {
		t.Setenv("ENTIRE_REPO_ROOT", repo)
	}
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return result{code, stdout.Bytes(), stderr.Bytes()}
}

func header(t *testing.T, stream []byte) *pyjson.Object {
	t.Helper()
	line, _, _ := bytes.Cut(stream, []byte{'\n'})
	v, err := pyjson.Parse(string(line), true)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}

func TestHelpAndAgentHelp(t *testing.T) {
	repo := store(t)
	if r := plugin(t, repo, "--help"); r.code != 0 || !bytes.Contains(r.stdout, []byte("entire asr [command]")) {
		t.Fatalf("root help: %s", r.stdout)
	}
	if r := plugin(t, repo, "export", "--help"); !bytes.HasPrefix(r.stdout, []byte("Selects sessions")) {
		t.Fatalf("export help: %s", r.stdout)
	}
	described, err := pyjson.Parse(string(plugin(t, repo, "agent-help", "--json").stdout), true)
	if err != nil {
		t.Fatal(err)
	}
	d := pyjson.AsObject(described)
	if d.Value("command") != "entire asr" || pyjson.AsObject(d.Value("subcommands").([]any)[0]).Value("name") != "export" {
		t.Fatal("agent-help --json")
	}
	exported, err := pyjson.Parse(string(plugin(t, repo, "agent-help", "export", "--json").stdout), true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range pyjson.AsObject(exported).Value("flags").([]any) {
		names = append(names, pyjson.AsObject(f).Value("name").(string))
	}
	if strings.Join(names, ",") != "all,checkpoint,commit,namespace,out,session,transcript" {
		t.Fatalf("flags %v", names)
	}
	if r := plugin(t, repo, "agent-help", "export"); !bytes.HasPrefix(r.stdout, []byte("entire asr export - ")) {
		t.Fatal("agent-help export")
	}
	if r := plugin(t, repo, "version"); string(r.stdout) != "entire-asr "+Version+"\n" {
		t.Fatalf("version %q", r.stdout)
	}
}

// The help texts are the Python plugin's, byte for byte.
func TestHelpText(t *testing.T) {
	want := `      --all                 Export every session in this repository's local checkpoints
  -c, --checkpoint string   Export the sessions in a checkpoint (ID or prefix). Repeatable
      --commit string       Export the sessions linked to a commit (SHA or ref). Repeatable
      --namespace string    Installation namespace for session IDs that repeat across machines
  -o, --out string          Directory for one ASR file per session (default: stdout for one session)
      --session string      Export a session (ID or prefix). Repeatable
      --transcript string   Transcript to read: auto, normalized or full (default "auto")
  -h, --help                help for this command`
	if got := flagTable(); got != want {
		t.Fatalf("flag table:\n%s", got)
	}
}

func TestExportsSelectedSessions(t *testing.T) {
	repo := store(t)
	t.Setenv("ENTIRE_CLI_VERSION", "")
	single := plugin(t, repo, "export", "--checkpoint", claude[:len(claude)-1]+"1")
	if single.code != 0 {
		t.Fatalf("export: %s", single.stderr)
	}
	et.KeepBytes(t, "single.jsonl", single.stdout)
	if header(t, single.stdout).Value("sessionId") != "synthetic-claude-session" {
		t.Fatal("selected session")
	}
	if r := plugin(t, repo, "export", "--session", "synthetic-claude"); !bytes.Equal(r.stdout, single.stdout) {
		t.Fatal("session prefix selects the same session")
	}
	// A commit's Entire-Checkpoint trailer selects its sessions.
	et.Git(t, repo, nil, nil, "commit", "-q", "--allow-empty", "-m", "Synthetic change\n\nEntire-Checkpoint: c0de0000a002")
	linked := plugin(t, repo, "export", "--commit", "HEAD")
	if header(t, linked.stdout).Value("sessionId") != "synthetic-codex-session" {
		t.Fatalf("commit selection: %s", linked.stderr)
	}
	out := filepath.Join(t.TempDir(), "all")
	all := plugin(t, repo, "export", "--all", "--out", out)
	paths := strings.Fields(string(all.stdout))
	if all.code != 0 || len(paths) != 5 {
		t.Fatalf("export --all: %s %s", all.stdout, all.stderr)
	}
	for _, p := range paths {
		et.Keep(t, p)
	}
	if r := plugin(t, repo, "export", "--all", "--out", out); r.code != 1 || !bytes.Contains(r.stderr, []byte("an output session already exists")) {
		t.Fatalf("overwrite: %s", r.stderr)
	}
}

func TestRecordsTheEntireVersion(t *testing.T) {
	repo := store(t)
	r := plugin(t, repo, "export", "--session", "synthetic-pi")
	invoked := et.Meta(header(t, r.stdout)).Value("invokedBy")
	if pyjson.MustDumps(invoked, pyjson.Options{}) != `{"command": "entire-asr", "version": "`+Version+`"}` {
		t.Fatalf("invokedBy %s", pyjson.PyRepr(invoked))
	}
	t.Setenv("ENTIRE_CLI_VERSION", "0.13.2")
	var stdout, stderr bytes.Buffer
	if Main([]string{"export", "--session", "synthetic-pi"}, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	invoked = et.Meta(header(t, stdout.Bytes())).Value("invokedBy")
	if pyjson.AsObject(invoked).Value("entireCliVersion") != "0.13.2" {
		t.Fatalf("invokedBy %s", pyjson.PyRepr(invoked))
	}
	producer := pyjson.AsObject(header(t, stdout.Bytes()).Value("producer"))
	if producer.Value("name") != "entire-asr" || producer.Value("version") != Version {
		t.Fatal("producer")
	}
}

func TestErrorsExitOne(t *testing.T) {
	repo := store(t)
	cases := []struct {
		args    []string
		message string
	}{
		{[]string{"export"}, "choose sessions"},
		{[]string{"export", "--bogus"}, "unknown flag"},
		{[]string{"export", "--all"}, "pass --out"},
		{[]string{"export", "--session", "synthetic-"}, "matches 5"},
		{[]string{"export", "--checkpoint", "ffff"}, "no local checkpoint"},
		{[]string{"export", "--commit", "--output"}, "not a commit"},
		{[]string{"export", "--commit", "no-such-ref"}, "not a commit"},
		{[]string{"export", "--all", "--transcript", "raw"}, "--transcript"},
		{[]string{"export", "--all=yes"}, "takes no value"},
		{[]string{"export", "--out"}, "flag needs an argument"},
		{[]string{"export", "stray"}, "unexpected argument"},
		{[]string{"agent-help", "--bad"}, "not flags"},
		{[]string{"agent-help", "nope"}, "unknown command"},
		{[]string{"frobnicate"}, "unknown command"},
	}
	for _, c := range cases {
		r := plugin(t, repo, c.args...)
		if r.code != 1 || !bytes.Contains(r.stderr, []byte(c.message)) || len(r.stdout) != 0 {
			t.Errorf("%v: code %d stderr %q stdout %q", c.args, r.code, r.stderr, r.stdout)
		}
		if !bytes.HasPrefix(r.stderr, []byte("Error: Invalid usage: ")) || !bytes.HasSuffix(r.stderr, []byte("Run \"entire asr --help\" for usage.\n")) {
			t.Errorf("%v: stderr shape %q", c.args, r.stderr)
		}
	}
}

func TestFindsTheRepositoryWithoutEntire(t *testing.T) {
	repo := store(t)
	t.Chdir(repo)
	r := plugin(t, "", "export", "--session", "synthetic-pi-session")
	if r.code != 0 || header(t, r.stdout).Value("sessionId") != "synthetic-pi-session" {
		t.Fatalf("repository discovery: %s", r.stderr)
	}
	t.Chdir(t.TempDir())
	if r := plugin(t, "", "export", "--all"); !bytes.Contains(r.stderr, []byte("not inside a Git repository")) {
		t.Fatalf("outside a repository: %s", r.stderr)
	}
}

func TestEmptyRepositoryHasNoCheckpoints(t *testing.T) {
	repo := et.Init(t, filepath.Join(t.TempDir(), "empty"))
	if r := plugin(t, repo, "export", "--all"); r.code != 1 || !bytes.Contains(r.stderr, []byte("no local Entire checkpoints")) {
		t.Fatalf("empty repository: %s", r.stderr)
	}
}

func TestOutputDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits")
	}
	repo := store(t)
	out := filepath.Join(t.TempDir(), "nested", "asr")
	if r := plugin(t, repo, "export", "--session", "synthetic-pi", "--out", out); r.code != 0 {
		t.Fatal(string(r.stderr))
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("directory mode %v", info.Mode())
	}
	files, _ := os.ReadDir(out)
	for _, f := range files {
		fi, _ := f.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode())
		}
	}
}

func TestJoinOutMatchesPathlib(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	for dir, want := range map[string]string{"asr": "asr/x", "./asr/": "asr/x", "": "x", "/tmp//a/./b": "/tmp/a/b/x",
		"//net/a": "//net/a/x", "///a": "/a/x", "../up": "../up/x"} {
		if got := joinOut(dir, "x"); got != want {
			t.Errorf("joinOut(%q) = %q, want %q", dir, got, want)
		}
	}
}
