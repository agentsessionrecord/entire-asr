package cli

import (
	"strings"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Version is the entire-asr version.
const Version = "0.1.0"

const (
	short = "Export Entire checkpoints as Agent Session Record (ASR) files"
	long  = `Reads Entire checkpoints from this repository's Git objects and writes one
Agent Session Record (ASR) file per agent session. It reads the git-refs backend
(refs/entire/checkpoints/<shard>/<id>) and the git-branch backend
(entire/checkpoints/v1). It never checks out, fetches, pushes or changes
checkpoint data, and never runs recorded commands.`
	exportShort = "Write ASR files for selected checkpoint sessions"
	exportLong  = `Selects sessions by checkpoint, commit or session ID and writes each session
as one ASR file. A file holds the whole retained session from every local
checkpoint of that session, and each record names the checkpoint whose slice
first held it. Selections add up.

Without --out, a single selected session is written to stdout. With --out,
one file per session is written to that directory and each path is printed.
Existing files are never overwritten.

The normalized transcript (transcript.jsonl) is the main source and covers
every agent Entire supports. --transcript full reads the raw Claude Code or
Codex transcript (full.jsonl) instead, which keeps reasoning text.`
	exportExample = `  entire asr export --checkpoint 01KVBJCWYA4YW6J5M9GP655HZN > session.jsonl
  entire asr export --commit HEAD --out asr
  entire asr export --session 3f2a --out asr
  entire asr export --all --out asr`
	guidance = `Use it when a task needs the agent sessions behind checkpoints or commits in a
portable format, for review, audit or another tool. It only reads checkpoint
data. It writes only the files it reports. Run it from inside the repository.`
)

type flag struct {
	name, short, kind, def, usage string
}

var flags = []flag{
	{"all", "", "bool", "", "Export every session in this repository's local checkpoints"},
	{"checkpoint", "c", "string", "", "Export the sessions in a checkpoint (ID or prefix). Repeatable"},
	{"commit", "", "string", "", "Export the sessions linked to a commit (SHA or ref). Repeatable"},
	{"namespace", "", "string", "", "Installation namespace for session IDs that repeat across machines"},
	{"out", "o", "string", "", "Directory for one ASR file per session (default: stdout for one session)"},
	{"session", "", "string", "", "Export a session (ID or prefix). Repeatable"},
	{"transcript", "", "string", "auto", "Transcript to read: auto, normalized or full"},
}

func flagTable() string {
	rows := append(append([]flag(nil), flags...), flag{"help", "h", "bool", "", "help for this command"})
	heads := make([]string, len(rows))
	width := 0
	for i, f := range rows {
		head := "      --" + f.name
		if f.short != "" {
			head = "  -" + f.short + ", --" + f.name
		}
		if f.kind != "bool" {
			head += " " + f.kind
		}
		heads[i] = head
		if len(head) > width {
			width = len(head)
		}
	}
	width += 3
	lines := make([]string, len(rows))
	for i, f := range rows {
		tail := f.usage
		if f.def != "" {
			tail += ` (default "` + f.def + `")`
		}
		lines[i] = heads[i] + strings.Repeat(" ", width-len(heads[i])) + tail
	}
	return strings.Join(lines, "\n")
}

func rootHelp() string {
	return long + `

Usage:
  entire asr [command]

Available Commands:
  export      ` + exportShort + `
  agent-help  Machine-readable usage for coding agents
  version     Show the entire-asr version

Flags:
  -h, --help      help for asr
  -v, --version   version for asr

Use "entire asr [command] --help" for more information about a command.
`
}

func exportHelp() string {
	return exportLong + `

Usage:
  entire asr export [flags]

Examples:
` + exportExample + `

Flags:
` + flagTable() + "\n"
}

func agentHelp(path []string, asJSON bool) (string, error) {
	isExport := len(path) == 1 && path[0] == "export"
	if len(path) != 0 && !isExport {
		return "", usageError(`unknown command "` + strings.Join(path, " ") + `" for "entire asr"`)
	}
	var doc *pyjson.Object
	if isExport {
		list := make([]any, len(flags))
		for i, f := range flags {
			o := pyjson.Obj("name", f.name)
			if f.short != "" {
				o.Set("shorthand", f.short)
			}
			o.Set("type", f.kind)
			if f.def != "" {
				o.Set("default", f.def)
			}
			o.Set("usage", f.usage)
			list[i] = o
		}
		doc = pyjson.Obj("command", "entire asr export", "short", exportShort, "long", exportLong,
			"guidance", guidance, "example", exportExample, "flags", list)
	} else {
		doc = pyjson.Obj("command", "entire asr", "short", short, "long", long, "guidance", guidance,
			"subcommands", []any{pyjson.Obj("name", "export", "short", exportShort, "audience", "task-driven")})
	}
	if asJSON {
		return pyjson.MustDumps(doc, pyjson.Options{EnsureASCII: true, Indent: 2}) + "\n", nil
	}
	text := doc.Value("command").(string) + " - " + doc.Value("short").(string) + "\n" + doc.Value("long").(string) +
		"\n\nWhen to use this:\n" + guidance + "\n"
	if isExport {
		text += "\nExamples:\n" + exportExample + "\n\nFlags:\n" + flagTable() + "\n"
	} else {
		text += "\nSubcommands: export\n             export writes files: run it when the task calls for it\n" +
			"Next:  entire agent-help asr <subcommand>\n"
	}
	return text, nil
}
