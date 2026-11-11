// Package cli is the entire-asr external command: `entire asr ...`.
//
// Entire runs entire-asr with the remaining arguments, the user's working
// directory and ENTIRE_REPO_ROOT when it runs inside a Git repository. Exit
// codes reach the caller unchanged.
package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/entire"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Producer names entire-asr in the streams it writes.
var Producer = entire.Producer{Name: "entire-asr", Version: Version}

type usageErr struct{ msg string }

func (e *usageErr) Error() string { return e.msg }

func usageError(msg string) error { return &usageErr{msg} }

// trailer is the Entire-Checkpoint commit trailer, with Python's whitespace.
var trailer = regexp.MustCompile(`(?m)^Entire-Checkpoint:[ \t]*([^` + pySpaceClass + `]+)[ \t]*(?:$)`)

const pySpaceClass = `\t\n\v\f\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

type options struct {
	all                         bool
	checkpoint, commit, session []string
	namespace, out              *string
	transcript                  string
	help                        bool
}

func parseExport(args []string) (*options, error) {
	o := &options{transcript: "auto"}
	names := map[string]string{"-c": "checkpoint", "-o": "out", "-h": "help", "--help": "help"}
	for _, f := range flags {
		names["--"+f.name] = f.name
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flagName, inline := arg, ""
		if strings.HasPrefix(arg, "--") {
			flagName, inline, _ = strings.Cut(arg, "=")
		}
		name, ok := names[flagName]
		if !ok {
			if strings.HasPrefix(arg, "-") {
				return nil, usageError("unknown flag: " + arg)
			}
			return nil, usageError(`unexpected argument "` + arg + `"`)
		}
		if name == "all" || name == "help" {
			if inline != "" {
				return nil, usageError("flag --" + name + " takes no value")
			}
			if name == "all" {
				o.all = true
			} else {
				o.help = true
			}
			continue
		}
		if inline == "" {
			i++
			if i >= len(args) {
				return nil, usageError("flag needs an argument: " + flagName)
			}
			inline = args[i]
		}
		value := inline
		switch name {
		case "checkpoint":
			o.checkpoint = append(o.checkpoint, value)
		case "commit":
			o.commit = append(o.commit, value)
		case "session":
			o.session = append(o.session, value)
		case "namespace":
			o.namespace = &value
		case "out":
			o.out = &value
		case "transcript":
			o.transcript = value
		}
	}
	if o.transcript != "auto" && o.transcript != "normalized" && o.transcript != "full" {
		return nil, usageError(`--transcript must be "auto", "normalized" or "full"`)
	}
	if !o.help && !(o.all || len(o.checkpoint) > 0 || len(o.commit) > 0 || len(o.session) > 0) {
		return nil, usageError("choose sessions with --checkpoint, --commit, --session or --all")
	}
	return o, nil
}

// match finds the value equal to prefix, or the one value it starts.
func match(values []string, prefix, label string) (string, error) {
	var found []string
	for _, v := range values {
		if v == prefix {
			return v, nil
		}
		if strings.HasPrefix(v, prefix) {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return "", usageError("no local " + label + " matches " + prefix)
	}
	if len(found) > 1 {
		return "", usageError(fmt.Sprintf("%s prefix %s matches %d %ss", label, prefix, len(found), label))
	}
	return found[0], nil
}

func commitCheckpoints(repo, ref string) (string, map[string]bool, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", nil, usageError("not a commit: " + ref)
	}
	out, err := entire.Git(repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		var gitErr *entire.GitError
		if errors.As(err, &gitErr) {
			return "", nil, usageError("not a commit: " + ref)
		}
		return "", nil, err
	}
	sha, err := pyjson.DecodeStrict(out)
	if err != nil {
		return "", nil, err
	}
	sha = pyjson.Strip(sha)
	data, err := entire.Git(repo, "cat-file", "commit", sha)
	if err != nil {
		var gitErr *entire.GitError
		if errors.As(err, &gitErr) {
			return "", nil, usageError("not a commit: " + ref)
		}
		return "", nil, err
	}
	message := pyjson.DecodeReplace(data)
	body := ""
	if _, rest, ok := strings.Cut(message, "\n\n"); ok {
		body = rest
	}
	found := map[string]bool{}
	for _, m := range trailer.FindAllStringSubmatch(body, -1) {
		found[m[1]] = true
	}
	return sha, found, nil
}

// iterable returns what Python's iteration of linked_commits yields, or a
// TypeError for a value that is truthy but not iterable.
func iterable(v any) ([]any, error) {
	if !pyjson.Truthy(v) {
		return nil, nil
	}
	switch x := v.(type) {
	case []any:
		return x, nil
	case *pyjson.Object:
		keys := make([]any, x.Len())
		for i, k := range x.Keys() {
			keys[i] = k
		}
		return keys, nil
	case string:
		var out []any
		for _, r := range pyjson.Runes(x) {
			out = append(out, string(r))
		}
		return out, nil
	}
	return nil, &pyjson.TypeError{Msg: "object is not iterable"}
}

func selectSessions(repo string, sessions []*entire.Session, o *options) ([]*entire.Session, error) {
	if o.all {
		return sessions, nil
	}
	chosen := map[*entire.Session]bool{}
	var checkpoints []string
	seen := map[string]bool{}
	for _, s := range sessions {
		for _, c := range s.Checkpoints() {
			if !seen[c] {
				seen[c] = true
				checkpoints = append(checkpoints, c)
			}
		}
	}
	for _, prefix := range o.checkpoint {
		checkpoint, err := match(checkpoints, prefix, "checkpoint")
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			for _, c := range s.Checkpoints() {
				if c == checkpoint {
					chosen[s] = true
				}
			}
		}
	}
	for _, ref := range o.commit {
		sha, trailers, err := commitCheckpoints(repo, ref)
		if err != nil {
			return nil, err
		}
		found := false
		for _, s := range sessions {
			linked := false
			for _, c := range s.Checkpoints() {
				if trailers[c] {
					linked = true
				}
			}
			if !linked {
				for _, e := range s.Entries {
					links, err := iterable(e.Summary.Value("linked_commits"))
					if err != nil {
						return nil, err
					}
					for _, link := range links {
						if l := pyjson.AsObject(link); l != nil && l.Value("sha") == sha {
							linked = true
						}
					}
					if linked {
						break
					}
				}
			}
			if linked {
				chosen[s] = true
				found = true
			}
		}
		if !found {
			return nil, usageError("no local checkpoint session is linked to commit " + sha)
		}
	}
	var ids []string
	idSeen := map[string]bool{}
	for _, s := range sessions {
		if !idSeen[s.SessionID] {
			idSeen[s.SessionID] = true
			ids = append(ids, s.SessionID)
		}
	}
	for _, prefix := range o.session {
		id, err := match(ids, prefix, "session")
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			if s.SessionID == id {
				chosen[s] = true
			}
		}
	}
	var out []*entire.Session
	for _, s := range sessions {
		if chosen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// joinOut joins an output directory and a file name as Python's pathlib does:
// empty and "." parts drop out, ".." stays, and a leading "//" is kept.
func joinOut(dir, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, name)
	}
	lead := ""
	switch {
	case strings.HasPrefix(dir, "//") && !strings.HasPrefix(dir, "///"):
		lead = "//"
	case strings.HasPrefix(dir, "/"):
		lead = "/"
	}
	var parts []string
	for _, p := range strings.Split(dir, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return lead + strings.Join(append(parts, name), "/")
}

// mkdirOut creates the output directory with mode 0700, and missing parents
// with the default mode, as Path.mkdir(mode=0o700, parents=True) does.
func mkdirOut(dir string) error {
	if dir == "" {
		dir = "."
	}
	info, err := os.Stat(dir)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return &fs.PathError{Op: "mkdir", Path: dir, Err: fs.ErrExist}
	}
	parent := filepath.Dir(filepath.Clean(dir))
	if parent != dir && parent != "." && parent != string(filepath.Separator) {
		if err := os.MkdirAll(parent, 0o777); err != nil {
			return err
		}
	}
	err = os.Mkdir(dir, 0o700)
	if err != nil && errors.Is(err, fs.ErrExist) {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return nil
		}
	}
	return err
}

func export(args []string, stdout io.Writer) error {
	o, err := parseExport(args)
	if err != nil {
		return err
	}
	if o.help {
		_, err := io.WriteString(stdout, exportHelp())
		return err
	}
	repo := os.Getenv("ENTIRE_REPO_ROOT")
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		root, err := entire.RepositoryRoot(cwd)
		if err != nil {
			var gitErr *entire.GitError
			var decodeErr *pyjson.DecodeError
			if errors.As(err, &gitErr) || errors.As(err, &decodeErr) {
				return usageError("not inside a Git repository")
			}
			return err
		}
		repo = root
	}
	reader := &entire.Reader{Repo: repo}
	discovered, err := entire.Discover(reader)
	if err != nil {
		return err
	}
	sessions, err := selectSessions(repo, discovered, o)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return usageError("this repository has no local Entire checkpoints")
	}
	if o.out == nil && len(sessions) > 1 {
		return usageError(fmt.Sprintf("%d sessions selected: pass --out DIR to write one file each", len(sessions)))
	}
	invoked := pyjson.Obj("command", "entire-asr", "version", Version)
	if v := os.Getenv("ENTIRE_CLI_VERSION"); v != "" {
		invoked.Set("entireCliVersion", v)
	}
	streams := make([]*asr.Stream, len(sessions))
	for i, s := range sessions {
		streams[i], err = entire.Convert(s, reader, entire.Options{Namespace: o.namespace, Transcript: o.transcript,
			Invoked: invoked, Producer: Producer})
		if err != nil {
			return err
		}
	}
	if o.out == nil {
		data, err := streams[0].Encode()
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	}
	paths := make([]string, len(sessions))
	for i, s := range sessions {
		paths[i] = joinOut(*o.out, entire.OutputName(s))
	}
	if err := mkdirOut(*o.out); err != nil {
		return err
	}
	written, err := entire.WriteAll(streams, paths)
	if err != nil {
		return err
	}
	for _, p := range written {
		if _, err := fmt.Fprintln(stdout, p); err != nil {
			return err
		}
	}
	return nil
}

func run(argv []string, stdout io.Writer) error {
	command, rest := "", []string(nil)
	if len(argv) > 0 {
		command, rest = argv[0], argv[1:]
	}
	switch {
	case (command == "" || command == "-h" || command == "--help" || command == "help") && len(rest) == 0:
		_, err := io.WriteString(stdout, rootHelp())
		return err
	case command == "help" && len(rest) == 1 && rest[0] == "export":
		_, err := io.WriteString(stdout, exportHelp())
		return err
	case command == "version" || command == "-v" || command == "--version":
		_, err := io.WriteString(stdout, "entire-asr "+Version+"\n")
		return err
	case command == "agent-help":
		asJSON := false
		var path []string
		for _, arg := range rest {
			if arg == "--json" {
				asJSON = true
			} else {
				path = append(path, arg)
			}
		}
		for _, arg := range path {
			if strings.HasPrefix(arg, "-") {
				return usageError("agent-help takes a command path, not flags")
			}
		}
		text, err := agentHelp(path, asJSON)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, text)
		return err
	case command == "export":
		return export(rest, stdout)
	}
	return usageError(`unknown command "` + command + `" for "entire asr"`)
}

// errorName returns the Python exception name the reference prints for an
// operating system error.
func errorName(err error) string {
	var pathErr *fs.PathError
	var execErr *exec.Error
	switch {
	case errors.As(err, &execErr):
		return "FileNotFoundError"
	case errors.Is(err, fs.ErrExist):
		return "FileExistsError"
	case errors.Is(err, fs.ErrNotExist):
		return "FileNotFoundError"
	case errors.Is(err, fs.ErrPermission):
		return "PermissionError"
	case errors.Is(err, syscall.ENOTDIR):
		return "NotADirectoryError"
	case errors.Is(err, syscall.EISDIR):
		return "IsADirectoryError"
	case errors.Is(err, syscall.EPIPE):
		return "BrokenPipeError"
	case errors.As(err, &pathErr):
		return "OSError"
	}
	return "OSError"
}

// Main runs the command and returns its exit code.
func Main(argv []string, stdout, stderr io.Writer) int {
	err := run(argv, stdout)
	if err == nil {
		return 0
	}
	if asr.Interrupted != nil && asr.Interrupted() {
		// An interrupt is reported by the signal, not by a message.
		return 130
	}
	var u *usageErr
	var valueErr *pyjson.ValueError
	var syntaxErr *pyjson.SyntaxError
	var decodeErr *pyjson.DecodeError
	var gitErr *entire.GitError
	var typeErr *pyjson.TypeError
	switch {
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "Error: Invalid usage: %s\n", u.msg)
		fmt.Fprintln(stderr, `Run "entire asr --help" for usage.`)
	case errors.As(err, &valueErr), errors.As(err, &syntaxErr), errors.As(err, &decodeErr), errors.As(err, &gitErr):
		fmt.Fprintf(stderr, "Error: export failed: %s\n", err.Error())
	case errors.As(err, &typeErr):
		fmt.Fprintln(stderr, "Error: export failed: TypeError")
	default:
		fmt.Fprintf(stderr, "Error: export failed: %s\n", errorName(err))
	}
	return 1
}
