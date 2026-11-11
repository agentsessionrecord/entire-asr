package entire

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"sort"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Profile names the pinned Entire storage and transcript profile.
const Profile = "entire-v0.13.2"

// Producer names the program that writes a stream.
type Producer struct {
	Name, Version string
}

// ImporterProducer is the ASR Python importer's producer, for comparisons.
var ImporterProducer = Producer{"asr-import-entire", "0.2.0"}

// Session is one agent session, grouped across its checkpoints.
type Session struct {
	Agent       string
	Registry    string
	NativeAgent *string
	SessionID   string
	Entries     []*SessionEntry
	Shadowed    []*SessionEntry
}

// Checkpoints lists the session's checkpoint IDs in merge order.
func (s *Session) Checkpoints() []string {
	out := make([]string, len(s.Entries))
	for i, e := range s.Entries {
		out[i] = e.Checkpoint
	}
	return out
}

func entryLess(a, b *SessionEntry) bool {
	// Entire's own position markers grow through a session. Creation time and
	// the ID only break ties. Event timestamps never reorder records.
	if c := a.TranscriptStart().Cmp(b.TranscriptStart()); c != 0 {
		return c < 0
	}
	start := func(e *SessionEntry) *big.Int {
		if n, ok := e.CompactStart(); ok {
			return n
		}
		return big.NewInt(-1)
	}
	if c := start(a).Cmp(start(b)); c != 0 {
		return c < 0
	}
	created := func(e *SessionEntry) string {
		s, _ := e.Metadata.Value("created_at").(string)
		return s
	}
	if ca, cb := created(a), created(b); ca != cb {
		return ca < cb
	}
	if a.Checkpoint != b.Checkpoint {
		return a.Checkpoint < b.Checkpoint
	}
	return a.Index < b.Index
}

type slotKey struct {
	hasLabel                bool
	label, session, checkpt string
}

type groupKey struct {
	hasLabel       bool
	label, session string
}

// Discover groups every retained checkpoint copy by agent and native session ID.
func Discover(reader *Reader) ([]*Session, error) {
	entries, err := reader.Sessions()
	if err != nil {
		return nil, err
	}
	var order []slotKey
	copies := map[slotKey]*SessionEntry{}
	shadowed := map[slotKey][]*SessionEntry{}
	for _, entry := range entries {
		label, hasLabel := entry.Metadata.Value("agent").(string)
		slot := slotKey{hasLabel, label, entry.SessionID, entry.Checkpoint}
		previous, seen := copies[slot]
		// Entire's readers try a checkpoint's own ref before the v1 branch,
		// including hex checkpoints migrated from the branch.
		if !seen || (previous.Backend == "git-branch" && entry.Backend == "git-refs") {
			if seen {
				shadowed[slot] = append(shadowed[slot], previous)
			} else {
				order = append(order, slot)
			}
			copies[slot] = entry
		} else {
			shadowed[slot] = append(shadowed[slot], entry)
		}
	}
	var groups []*Session
	byKey := map[groupKey]*Session{}
	for _, slot := range order {
		entry := copies[slot]
		key := groupKey{slot.hasLabel, slot.label, slot.session}
		group, ok := byKey[key]
		if !ok {
			agent, registry := UnknownAgent, ""
			if slot.hasLabel && slot.label != "" {
				if r, known := RegistryNames[slot.label]; known {
					registry = r
					if name, named := ASRNames[r]; named {
						agent = name
					}
				}
			}
			var native *string
			if slot.hasLabel {
				l := slot.label
				native = &l
			}
			group = &Session{Agent: agent, Registry: registry, NativeAgent: native, SessionID: slot.session}
			byKey[key] = group
			groups = append(groups, group)
		}
		group.Entries = append(group.Entries, entry)
		group.Shadowed = append(group.Shadowed, shadowed[slot]...)
	}
	for _, g := range groups {
		sort.SliceStable(g.Entries, func(i, j int) bool { return entryLess(g.Entries[i], g.Entries[j]) })
	}
	nativeName := func(s *Session) string {
		if s.NativeAgent == nil {
			return ""
		}
		return *s.NativeAgent
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		return nativeName(a) < nativeName(b)
	})
	return groups, nil
}

// OutputName is the session's file name. The hash is a file name, not a
// replacement session identity.
func OutputName(s *Session) string {
	sum := sha256.Sum256([]byte(s.Agent + "\x00" + s.SessionID))
	return hex.EncodeToString(sum[:]) + ".jsonl"
}

// Options configure a conversion.
type Options struct {
	Namespace  *string
	Transcript string
	Invoked    *pyjson.Object
	Producer   Producer
}

// Convert maps one session to a stream.
func Convert(s *Session, reader *Reader, o Options) (*asr.Stream, error) {
	if o.Transcript != "auto" && o.Transcript != "normalized" && o.Transcript != "full" {
		return nil, &pyjson.ValueError{Msg: "unknown transcript source"}
	}
	reader.Budget = 0
	last := s.Entries[len(s.Entries)-1]
	anyFull := false
	for _, e := range s.Entries {
		if e.Full != nil {
			anyFull = true
		}
	}
	useRaw := (s.Agent == "claude-code" || s.Agent == "codex") &&
		((o.Transcript == "full" && anyFull) || (o.Transcript == "auto" && last.Compact == nil && last.Full != nil))
	source := "entire-transcript-v1"
	if useRaw {
		source = "entire-full-jsonl"
	}
	stream, err := NewStream(s.Agent, s.SessionID, source, o.Namespace, o.Producer)
	if err != nil {
		return nil, err
	}
	transcript := "normalized"
	if useRaw {
		transcript = "full"
	}
	checkpoints := make([]any, len(s.Entries))
	for i, e := range s.Entries {
		checkpoints[i] = e.Describe()
	}
	meta := pyjson.Obj("profile", Profile, "transcript", transcript, "checkpoints", checkpoints)
	if s.NativeAgent != nil && *s.NativeAgent != "" && s.Agent == UnknownAgent {
		meta.Set("agent", *s.NativeAgent)
	}
	if len(s.Shadowed) > 0 {
		copies := make([]any, len(s.Shadowed))
		for i, e := range s.Shadowed {
			copies[i] = pyjson.Obj("checkpoint", e.Checkpoint, "backend", e.Backend, "revision", e.Revision)
		}
		meta.Set("shadowedCopies", copies)
	}
	if o.Invoked != nil && o.Invoked.Len() > 0 {
		meta.Set("invokedBy", o.Invoked)
	}
	stream.Header.Set("_meta", pyjson.Obj(asr.META, meta))
	if useRaw {
		stream.Missing("assets")
		var snapshots []snapshot
		for _, e := range s.Entries {
			data, err := reader.ReadBlob(e.Full)
			if err != nil {
				return nil, err
			}
			snapshots = append(snapshots, snapshot{e.Metadata, data})
		}
		merged, err := mergeSnapshots(stream, snapshots)
		if err != nil {
			return nil, err
		}
		if len(merged) > 0 {
			if err := NativeTranscript(stream, merged, s.Agent); err != nil {
				return nil, err
			}
		}
		return stream, nil
	}
	if err := normalizedSession(stream, s, reader); err != nil {
		return nil, err
	}
	return stream, nil
}

// NewStream starts a stream with the Entire importer's producer fields and
// checkpoint losses.
func NewStream(agent, session, source string, namespace *string, producer Producer) (*asr.Stream, error) {
	stream, err := asr.NewStream(agent, session, source, namespace)
	if err != nil {
		return nil, err
	}
	stream.Header.Set("producer", pyjson.Obj("name", producer.Name, "version", producer.Version,
		"method", "converter", "source", source))
	stream.Missing("checkpoint_history", "checkpoint_attribution")
	return stream, nil
}

// item is a line assembled from checkpoint views, or a gap marker.
type item struct {
	gap        *SessionEntry
	line       Line
	checkpoint *string
}

func strPtr(s string) *string { return &s }

func normalizedSession(stream *asr.Stream, s *Session, reader *Reader) error {
	var current, orphans []item
	for _, entry := range s.Entries {
		data, err := reader.ReadBlob(entry.Compact)
		if err != nil {
			return err
		}
		if data == nil {
			// A later self-contained view covers this checkpoint. Without one,
			// its content is missing here.
			current = append(current, item{gap: entry})
			continue
		}
		lines, err := Parse(data)
		if err != nil {
			return err
		}
		var real []item
		for _, it := range current {
			if it.gap == nil {
				real = append(real, it)
			}
		}
		startBig, hasStart := entry.CompactStart()
		if !hasStart {
			// Checkpoints from CLIs before 0.8 store only their own delta.
			for _, l := range lines {
				current = append(current, item{line: l, checkpoint: strPtr(entry.Checkpoint)})
			}
			stream.Missing("checkpoint_deltas")
			continue
		}
		var start int
		if startBig.Cmp(big.NewInt(int64(len(lines)))) > 0 {
			if _, err := stream.Gap("unknown", "A checkpoint's transcript start marker is past the end of its transcript.",
				nil, nil, pyjson.Obj("checkpoint", entry.Checkpoint, "compactTranscriptStart", bigValue(startBig))); err != nil {
				return err
			}
			start = len(lines)
		} else {
			start = int(startBig.Int64())
		}
		// The slice can repeat one merged line from the previous checkpoint.
		restates := func(n int) bool {
			for i := 0; i < n; i++ {
				if !extends(real[i].line, lines[i]) {
					return false
				}
			}
			return true
		}
		consistent := (start == len(real) && restates(start)) ||
			(start == len(real)-1 && start < len(lines) && restates(start+1))
		if !consistent {
			// Keep earlier content that this view no longer holds.
			held := map[lineKey]bool{}
			for _, l := range lines {
				held[keyOf(l)] = true
			}
			earlier := map[lineKey]*string{}
			for _, it := range real {
				k := keyOf(it.line)
				if !held[k] {
					orphans = append(orphans, it)
				}
				earlier[k] = it.checkpoint
			}
			next := make([]item, len(lines))
			for i, l := range lines {
				if i < start {
					next[i] = item{line: l, checkpoint: earlier[keyOf(l)]}
				} else {
					next[i] = item{line: l, checkpoint: strPtr(entry.Checkpoint)}
				}
			}
			current = next
			continue
		}
		next := make([]item, len(lines))
		for i, l := range lines {
			if i < len(real) {
				next[i] = item{line: l, checkpoint: real[i].checkpoint}
			} else {
				next[i] = item{line: l, checkpoint: strPtr(entry.Checkpoint)}
			}
		}
		current = next
	}
	return MapLines(stream, s.Registry, current, orphans)
}

func bigValue(n *big.Int) any {
	if n.IsInt64() {
		return n.Int64()
	}
	return n
}

func lineMeta(position *int, checkpoint *string) *pyjson.Object {
	meta := pyjson.NewObject()
	if position != nil {
		meta.Set("transcriptLine", int64(*position))
	}
	if checkpoint != nil && *checkpoint != "" {
		meta.Set("checkpoint", *checkpoint)
	}
	return meta
}

// MapLines writes records for assembled lines, then settles the session's declared losses.
func MapLines(stream *asr.Stream, agent string, lines []item, orphans []item) error {
	mapper := NewMapper(stream, agent)
	position := 0
	for _, it := range lines {
		if it.gap != nil {
			if _, err := stream.Gap("not_retained", "A checkpoint of this session has no normalized transcript.",
				nil, nil, pyjson.Obj("checkpoint", it.gap.Checkpoint)); err != nil {
				return err
			}
			continue
		}
		p := position
		if err := mapper.Line(it.line.Raw, it.line.Line, lineMeta(&p, it.checkpoint)); err != nil {
			return err
		}
		position++
	}
	if len(orphans) > 0 {
		stream.Missing("checkpoint_order")
		if _, err := stream.Gap("unknown", "Content that earlier checkpoints held and later checkpoints do not follows. "+
			"Its order relative to the records above is unresolved.", nil, nil, nil); err != nil {
			return err
		}
		for _, it := range orphans {
			if err := mapper.Line(it.line.Raw, it.line.Line, lineMeta(nil, it.checkpoint)); err != nil {
				return err
			}
		}
	}
	if mapper.assets {
		stream.Missing("assets")
	}
	if mapper.userText {
		// Entire trims user text and removes IDE context tags.
		stream.Missing("content")
	}
	// Injected text and compaction summaries are not marked in this format.
	stream.Missing("message_origin")
	captured := map[string]bool{}
	for _, name := range mapper.Losses() {
		captured[name] = true
	}
	var kept []string
	for _, name := range stream.MissingNames() {
		if !captured[name] {
			kept = append(kept, name)
		}
	}
	stream.SetMissing(kept)
	return nil
}

// Lines wraps parsed transcript lines for MapLines.
func Lines(lines []Line) []item {
	out := make([]item, len(lines))
	for i, l := range lines {
		out[i] = item{line: l}
	}
	return out
}

// WriteAll writes each stream to its path. Nothing is written when any path
// exists, and files already written are removed when a later one fails.
func WriteAll(streams []*asr.Stream, paths []string) ([]string, error) {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return nil, &pyjson.ValueError{Msg: "an output session already exists"}
		}
	}
	var written []string
	for i, s := range streams {
		if err := s.Write(paths[i]); err != nil {
			for _, p := range written {
				os.Remove(p)
			}
			return nil, err
		}
		written = append(written, paths[i])
	}
	return written, nil
}

// ImportRepository converts every session in a repository's local
// checkpoints and writes one file per session to outputDir.
func ImportRepository(repo, outputDir string, o Options) ([]string, error) {
	reader := &Reader{Repo: repo}
	sessions, err := Discover(reader)
	if err != nil {
		return nil, err
	}
	streams := make([]*asr.Stream, len(sessions))
	paths := make([]string, len(sessions))
	for i, s := range sessions {
		if streams[i], err = Convert(s, reader, o); err != nil {
			return nil, err
		}
		paths[i] = filepath.Join(outputDir, OutputName(s))
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, err
	}
	return WriteAll(streams, paths)
}
