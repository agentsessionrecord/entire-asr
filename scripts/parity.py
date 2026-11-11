"""Check that entire-asr writes the same records as the ASR Entire importer.

Run it from a checkout of agentsessionrecord/asr, in the importer's
environment:

    uv run --directory asr --package asr-entire python ../scripts/parity.py \
        --asr . --plugin /path/to/entire-asr --out /tmp/parity

It builds synthetic checkpoint stores with the importer's test store builder,
imports each store with the importer, exports it with entire-asr, and
compares every record. Only the producer name and version and the
invokedBy metadata may differ.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--asr", type=Path, required=True, help="checkout of agentsessionrecord/asr")
parser.add_argument("--plugin", type=Path, required=True, help="entire-asr executable")
parser.add_argument("--out", type=Path, required=True, help="new directory for stores and outputs")
args = parser.parse_args()
asr = args.asr.resolve()
plugin = args.plugin.resolve()
out = args.out.resolve()
sys.path.insert(0, str(asr / "importers/entire/tests"))

from asr_entire import import_repository  # noqa: E402
from entire_store import FIXTURES, build, fixture_store, git, init, load_transcripts  # noqa: E402

CLAUDE = "01KA2SYNTHETC0000000000CA1"
TRANSCRIPTS = ("auto", "normalized", "full")


def metadata(cid, session, agent="Claude Code", start=0, compact=0):
    value = {"cli_version": "0.13.2", "checkpoint_id": cid, "session_id": session, "strategy": "manual-commit",
             "created_at": "2026-10-01T10:00:00Z", "files_touched": [], "agent": agent, "model": ""}
    if start:
        value["checkpoint_transcript_start"] = start
    if compact is not None:
        value["compact_transcript_start"] = compact
    return value


def stores(root):
    repo = root / "fixture"
    fixture_store(repo)
    yield repo
    repo = init(root / "legacy")
    lines = load_transcripts()["claude-code"]
    build(repo, {"checkpoints": [
        {"backend": "git-branch", "id": "aa0000000001", "sessions": [
            {"metadata": metadata("aa0000000001", "legacy", compact=None), "lines": lines[:4]}]},
        {"backend": "git-branch", "id": "aa0000000002", "sessions": [
            {"metadata": metadata("aa0000000002", "legacy", start=5, compact=None), "lines": lines[4:]}]}]})
    yield repo
    full = (FIXTURES / "full.jsonl").read_bytes()
    first = full.split(b"\n", 1)[0] + b"\n"
    repo = init(root / "raw")
    build(repo, {"checkpoints": [
        {"backend": "git-refs", "id": CLAUDE, "sessions": [
            {"metadata": metadata(CLAUDE, "synthetic-auto-full"), "full": full, "compact": False}]},
        {"backend": "git-branch", "id": "ab1234567890", "sessions": [
            {"metadata": metadata("ab1234567890", "synthetic-entire-session"), "full": first, "lines": []}]},
        {"backend": "git-branch", "id": "cd1234567890", "sessions": [
            {"metadata": metadata("cd1234567890", "synthetic-entire-session", start=1, compact=1), "full": full,
             "lines": []}]}]})
    yield repo
    # The ASR repository's native samples, stored as raw transcripts.
    repo = init(root / "samples")
    checkpoints = []
    for index, (agent, label) in enumerate((("claude-code", "Claude Code"), ("codex", "Codex"))):
        data = (asr / "samples" / agent / "native/session.jsonl").read_bytes()
        cid = f"5a{index:010x}"
        checkpoints.append({"backend": "git-branch", "id": cid, "sessions": [
            {"metadata": metadata(cid, f"sample-{agent}", agent=label), "full": data, "compact": False}]})
    build(repo, {"checkpoints": checkpoints})
    yield repo


def normalized(path):
    rows = []
    for line in path.read_bytes().split(b"\n")[:-1]:
        row = json.loads(line)
        if row.get("type") == "session":
            row["producer"]["name"] = row["producer"]["version"] = None
            row.get("_meta", {}).get("org.agentsessionrecord.importers", {}).pop("invokedBy", None)
        rows.append(row)
    return rows


def main():
    out.mkdir(parents=True)
    env = {key: value for key, value in os.environ.items() if not key.startswith("ENTIRE_")}
    failures = sessions = records = 0
    for repo in stores(out / "stores"):
        for transcript in TRANSCRIPTS:
            name = f"{repo.name}-{transcript}"
            expected = import_repository(repo, out / "importer" / name, transcript=transcript)
            target = out / "entire-asr" / name
            result = subprocess.run([str(plugin), "export", "--all", "--transcript", transcript, "--out", str(target)],
                                    env={**env, "ENTIRE_REPO_ROOT": str(repo)}, capture_output=True, text=True)
            if result.returncode:
                print(f"{name}: entire-asr failed: {result.stderr}")
                failures += 1
                continue
            written = [Path(line) for line in result.stdout.splitlines()]
            if sorted(p.name for p in written) != sorted(p.name for p in expected):
                print(f"{name}: different files")
                failures += 1
                continue
            for path in expected:
                a, b = normalized(path), normalized(target / path.name)
                sessions += 1
                records += len(a)
                if a != b:
                    failures += 1
                    print(f"{name}/{path.name}: records differ")
                    for x, y in zip(a, b):
                        if x != y:
                            print("  importer  ", json.dumps(x)[:400])
                            print("  entire-asr", json.dumps(y)[:400])
                            break
    print(f"{sessions} sessions, {records} records compared, {failures} differences")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
