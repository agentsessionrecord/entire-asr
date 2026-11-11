# entire-asr

`entire asr` exports [Entire](https://github.com/entireio/cli) checkpoints as
[Agent Session Record (ASR)](https://agentsessionrecord.org) files. It is an
external command for the Entire CLI. When you run `entire asr`, Entire finds
the `entire-asr` executable in its plugin directory or on `PATH` and runs it
with the remaining arguments.

It is a single static binary with no runtime dependencies beyond Git. It is
tested with Entire CLI v0.13.2.

## Install

```sh
entire plugin install https://github.com/agentsessionrecord/entire-asr
entire asr --help
```

Entire picks the highest release tag, downloads the archive for your system,
checks it against the release's `checksums.txt` and links `entire-asr` into
its plugin directory. Releases have binaries for Linux, macOS and Windows on
amd64 and arm64. Entire asks you to confirm a repository that is not in its
plugin index. Add `--yes` to skip the question in scripts.
`entire plugin upgrade asr` installs a newer release, and
`entire plugin remove asr` removes the command.

To build from source with Go, run `go build ./cmd/entire-asr` and then
`entire plugin install ./entire-asr`.

## Usage

Run it inside a repository that has Entire checkpoints:

```sh
entire asr export --checkpoint 01KVBJCWYA4YW6J5M9GP655HZN > session.jsonl
entire asr export --commit HEAD --out asr
entire asr export --session 3f2a --out asr
entire asr export --all --out asr
```

| Flag | Meaning |
| --- | --- |
| `-c, --checkpoint ID` | Sessions in a checkpoint, by ID or unique prefix |
| `--commit REF` | Sessions linked to a commit by its `Entire-Checkpoint` trailer or a recorded commit link |
| `--session ID` | A session, by ID or unique prefix |
| `--all` | Every session in the repository's local checkpoints |
| `-o, --out DIR` | Write one file per session to `DIR` and print each path |
| `--transcript auto` | `normalized` reads `transcript.jsonl`. `full` reads the Claude Code or Codex `full.jsonl`, which keeps reasoning text |
| `--namespace NAME` | Installation namespace for session IDs that repeat across machines |

Selections add up, and the selection flags can repeat. Without `--out`, one
selected session is written to standard output. Existing files are never
overwritten. Each file holds the whole retained session from every local
checkpoint of that session, and each record names the checkpoint whose slice
first held it. Output files are created with owner-only permissions, and
their names are hashes of the agent and the native session ID.

`entire asr --help` and `entire asr export --help` print help.
`entire agent-help asr` and `entire agent-help asr export` describe the
command to coding agents, with `--json` for a structured form.
`entire asr version` prints the version.

The command exits with 0 on success and 1 on an error, and Entire passes the
code on. Errors are printed to standard error. Interrupting it stops it by
the same signal, and a file it was writing is removed.

## Repository and environment

Entire runs the command in your working directory and sets `ENTIRE_REPO_ROOT`
when it runs inside a Git repository. The command reads that repository. Run
directly as `entire-asr`, it uses the Git repository of the working directory.
Each file's header names `entire-asr` and its version as the producer, and
records the Entire CLI version from `ENTIRE_CLI_VERSION` under `invokedBy`.
The command needs no other variables, no account and no network access.

## What it reads

The command reads Git objects only. It reads the git-refs backend,
`refs/entire/checkpoints/<shard>/<id>`, and the git-branch backend,
`entire/checkpoints/v1`, from local refs. When one checkpoint exists in both
backends after a migration, the ref copy is read, as Entire's readers do, and
the branch copy is named in header metadata. A ref whose shard does not match
its ID is ignored, as Entire ignores it.

It reads each session's metadata and its normalized transcript,
`transcript.jsonl`, which covers every agent Entire supports. It reads
`full.jsonl` only for the `full` choice, or when a Claude Code or Codex
session has no normalized transcript. It never checks out, fetches, pushes or
changes checkpoint data, and never runs hooks or recorded commands. It writes
only the files it reports.

Each checkpoint stores the whole session up to that point, and its
`compact_transcript_start` marks where its own slice begins. The command
orders a session's checkpoints by Entire's position markers, checks that each
later transcript restates the earlier lines before its marker, and joins them
so that no line or usage is counted twice. A later view that leaves out
earlier lines keeps them after a gap at the end of the file, and
`checkpoint_order` is declared. Checkpoints from Entire before 0.8 hold only
their own lines. Those are joined in order, and `checkpoint_deltas` is
declared.

The mapping and the checkpoint merge are the
[ASR Entire importer's](https://github.com/agentsessionrecord/asr/blob/main/importers/entire/README.md),
and the command writes the same records. Its README documents the full
mapping. In short, user and assistant lines become messages, tool blocks
become tool calls and results, and model calls become inference records with
model and usage where the format marks call boundaries.

## Known losses

Each file declares what it could not keep in its header's `missing` list.

| Loss | Why |
| --- | --- |
| `reasoning` | Entire's converters drop thinking blocks. `--transcript full` keeps them for Claude Code and Codex |
| `content` | User text is trimmed and IDE context tags are removed |
| `message_origin` | Injected text and compaction summaries are not marked |
| `tool_output` | A result is one text string. Claude Code Bash results hold stdout only |
| `timestamps` | Tool results have no time of their own |
| `subagents` | Task transcripts under `tasks/` are not read. A subagent ID stays in result metadata |
| `checkpoint_history` | Only ref tips are read. Earlier versions of a ref and deleted checkpoints are not |
| `checkpoint_attribution` | Entire's line attribution metadata is not mapped |
| `exit_codes`, `file_changes`, `commits`, `approvals`, `agent_settings`, `tools` | The format does not carry them |
| `usage`, `model` | Copilot CLI, Cursor, Droid, OpenCode, Antigravity and external agents count tokens per message, so counts stay in metadata |

No file change or commit action is derived from tool input, file lists,
checkpoint metadata or `Entire-Checkpoint` trailers. Checkpoint creation time,
branch, model labels, token totals and commit links stay in header metadata.

## Safety

The command runs Git read commands with inherited Git variables, system and
global Git config, replace refs, hooks and the file system monitor turned off.
Each blob is capped at 32 MiB and each session at 128 MiB of checkpoint data.
Values of credential-named fields, recognized token forms and credential-like
environment values are replaced with `[redacted]`, and the file declares
`secrets`. A pattern filter cannot catch every secret, so read a file before
you share it.

## Checks

```sh
go test ./...
```

The tests build synthetic checkpoint stores with Git plumbing, as Entire
v0.13.2 lays them out, and use the ASR Entire importer's fixtures. The
documented mappings must match the importer's expected files byte for byte.
Continuous integration also validates every test output with the
[ASR validator](https://github.com/agentsessionrecord/asr/tree/main/tools/validator)
and runs [scripts/parity.py](scripts/parity.py), which exports synthetic
stores with this command and with the importer and compares every record.

## License

[Apache License 2.0](LICENSE)
