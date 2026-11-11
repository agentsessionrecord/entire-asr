package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	et "github.com/agentsessionrecord/entire-asr/internal/entiretest"
)

// TestMain runs the command itself when a test starts this binary again.
func TestMain(m *testing.M) {
	if os.Getenv("ENTIRE_ASR_RUN_MAIN") == "1" {
		os.Args = append([]string{"entire-asr"}, strings.Split(os.Getenv("ENTIRE_ASR_ARGS"), "\n")...)
		main()
		return
	}
	os.Exit(m.Run())
}

func run(t *testing.T, repo string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "ENTIRE_ASR_RUN_MAIN=1", "ENTIRE_ASR_ARGS="+strings.Join(args, "\n"),
		"ENTIRE_REPO_ROOT="+repo, "ENTIRE_CLI_VERSION=0.13.2")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func TestBinaryExitCodesAndOutput(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	et.FixtureStore(t, repo)
	code, stdout, _ := run(t, repo, "version")
	if code != 0 || stdout != "entire-asr 0.1.0\n" {
		t.Fatalf("version: %d %q", code, stdout)
	}
	code, stdout, stderr := run(t, repo, "export", "--session", "synthetic-codex")
	if code != 0 || !strings.HasPrefix(stdout, `{"type":"session"`) || stderr != "" {
		t.Fatalf("export: %d %q", code, stderr)
	}
	et.KeepBytes(t, "binary.jsonl", []byte(stdout))
	code, stdout, stderr = run(t, repo, "export", "--all")
	if code != 1 || stdout != "" || stderr != "Error: Invalid usage: 5 sessions selected: pass --out DIR to write one file each\nRun \"entire asr --help\" for usage.\n" {
		t.Fatalf("usage error: %d %q", code, stderr)
	}
}
