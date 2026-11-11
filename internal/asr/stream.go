package asr

import (
	"errors"
	"os"

	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// META is the metadata namespace of the ASR importers.
const META = "org.agentsessionrecord.importers"

const (
	maxInputBytes  = 32 * 1024 * 1024
	maxOutputBytes = 64 * 1024 * 1024
	// MaxRecords bounds the records of one stream and the lines of one input.
	MaxRecords = 100_000
	// MaxInputBytes bounds one input document.
	MaxInputBytes = maxInputBytes
)

// Losses are the declared losses every importer stream starts with.
var Losses = []string{"reasoning", "usage", "tool_output", "exit_codes", "file_changes",
	"commits", "subagents", "copies", "model", "approvals", "agent_settings", "tools"}

// F is one record field, in the order fields are written.
type F struct {
	K string
	V any
}

// Stream collects the records of one session, as importer-core's Stream does.
type Stream struct {
	Scope    string
	Header   *pyjson.Object
	Records  []*pyjson.Object
	ids      map[string]bool
	retained int
}

// NewStream starts a stream. A nil namespace is absent. An empty namespace
// changes derived IDs but is not written, as in the reference.
func NewStream(agent, session, source string, namespace *string) (*Stream, error) {
	if session == "" {
		return nil, &pyjson.ValueError{Msg: "a native session ID is required"}
	}
	var ns any
	if namespace != nil {
		ns = *namespace
	}
	scope, err := pyjson.Dumps([]any{agent, ns, session, source}, pyjson.Options{EnsureASCII: true})
	if err != nil {
		return nil, err
	}
	missing := make([]any, len(Losses))
	for i, name := range Losses {
		missing[i] = name
	}
	header := pyjson.Obj("type", "session", "id", "header", "formatVersion", "0.1",
		"agent", pyjson.Obj("name", agent), "sessionId", session,
		"producer", pyjson.Obj("name", "asr-import-"+source, "version", "0.1.0", "method", "converter", "source", source),
		"missing", missing)
	if namespace != nil && *namespace != "" {
		header.Set("namespace", *namespace)
	}
	return &Stream{Scope: scope, Header: header, ids: map[string]bool{"header": true}}, nil
}

func missingList(header *pyjson.Object) []any {
	list, _ := header.Value("missing").([]any)
	return list
}

// HasMissing reports whether a loss is declared.
func (s *Stream) HasMissing(name string) bool {
	for _, item := range missingList(s.Header) {
		if item == name {
			return true
		}
	}
	return false
}

// Missing declares losses that are not declared yet.
func (s *Stream) Missing(names ...string) {
	list := missingList(s.Header)
	for _, name := range names {
		if !s.HasMissing(name) {
			list = append(list, name)
			s.Header.Set("missing", list)
		}
	}
}

// SetMissing replaces the declared losses.
func (s *Stream) SetMissing(names []string) {
	list := make([]any, len(names))
	for i, name := range names {
		list[i] = name
	}
	s.Header.Set("missing", list)
}

// MissingNames returns the declared losses.
func (s *Stream) MissingNames() []string {
	var out []string
	for _, item := range missingList(s.Header) {
		if name, ok := item.(string); ok {
			out = append(out, name)
		}
	}
	return out
}

func validSourceID(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	n := 0
	for i := 0; i < len(s); {
		r, w := pyjson.DecodeRune(s[i:])
		if r <= 0x1f || r >= 0x7f && r <= 0x9f {
			return "", false
		}
		i += w
		n++
	}
	return s, n <= 256
}

// Add appends a record. A native ID becomes the record ID when it is usable
// and unused. Otherwise the ID is derived from the record's position.
func (s *Stream) Add(kind string, sourceID, ts, meta any, fields ...F) (*pyjson.Object, error) {
	rid, ok := validSourceID(sourceID)
	for collision := 0; !ok || s.ids[rid]; collision++ {
		rid, ok = DeriveID(s.Scope, kind, len(s.Records), collision), true
	}
	s.ids[rid] = true
	record := pyjson.Obj("type", kind, "id", rid)
	for _, f := range fields {
		record.Set(f.K, f.V)
	}
	if Stamp(ts) {
		record.Set("ts", ts)
	} else {
		s.Missing("timestamps")
	}
	if meta != nil {
		record.Set("_meta", pyjson.Obj(META, pyjson.Native(meta)))
	}
	text, err := pyjson.Dumps(pyjson.Native(record), pyjson.Options{})
	if err != nil {
		return nil, err
	}
	if err := pyjson.CheckUTF8(text); err != nil {
		return nil, err
	}
	if len(s.Records) >= MaxRecords || s.retained+len(text) > maxOutputBytes {
		return nil, &pyjson.ValueError{Msg: "output_capacity"}
	}
	s.retained += len(text)
	s.Records = append(s.Records, record)
	return record, nil
}

// Gap appends a gap record and declares history as a loss.
func (s *Stream) Gap(reason, detail string, sourceID, ts, meta any, fields ...F) (*pyjson.Object, error) {
	s.Missing("history")
	return s.Add("gap", sourceID, ts, meta, append([]F{{"reason", reason}, {"detail", detail}}, fields...)...)
}

// Unparseable and DefaultDetail are the reference's default gap reason and detail.
const (
	Unparseable   = "unparseable"
	DefaultDetail = "A native record could not be mapped."
)

// Finish prepares the header and records for writing, as Stream.write does
// before its writer runs: reused result call IDs become unpaired, and
// credentials are removed using environment values and credential-named keys.
func (s *Stream) Finish() ([]*pyjson.Object, error) {
	all := make([]any, 0, len(s.Records)+1)
	all = append(all, s.Header)
	for _, r := range s.Records {
		all = append(all, r)
	}
	learned, err := CredentialValues(all)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, v := range EnvironmentSecrets() {
		set[v] = true
	}
	for _, v := range learned {
		set[v] = true
	}
	secrets := sortSecrets(set)

	results := map[string]int{}
	for _, r := range s.Records {
		if r.Value("type") == "tool_result" && r.Has("callId") {
			key, err := pyjson.HashKey(r.Value("callId"))
			if err != nil {
				return nil, err
			}
			results[key]++
		}
	}
	for _, r := range s.Records {
		if r.Value("type") != "tool_result" {
			continue
		}
		key, err := pyjson.HashKey(r.Value("callId"))
		if err != nil {
			return nil, err
		}
		if results[key] > 1 {
			callID := r.Value("callId")
			r.Delete("callId")
			metaObj := pyjson.AsObject(r.Value("_meta"))
			if !r.Has("_meta") {
				metaObj = pyjson.NewObject()
				r.Set("_meta", metaObj)
			}
			inner := pyjson.AsObject(metaObj.Value(META))
			if !metaObj.Has(META) {
				inner = pyjson.NewObject()
				metaObj.Set(META, inner)
			}
			inner.Set("unpairedCallId", callID)
			s.Missing("ambiguous_tool_results")
		}
	}

	out := make([]*pyjson.Object, 0, len(s.Records)+1)
	out = append(out, nil)
	for _, r := range s.Records {
		cleaned, changed, err := Scrub(pyjson.Native(r), secrets)
		if err != nil {
			return nil, err
		}
		safe := cleaned.(*pyjson.Object)
		if changed {
			safe.Set("truncated", true)
			s.Missing("secrets")
		}
		out = append(out, safe)
	}
	cleaned, changed, err := Scrub(pyjson.Native(s.Header), secrets)
	if err != nil {
		return nil, err
	}
	header := cleaned.(*pyjson.Object)
	if changed {
		header.Set("truncated", true)
		list := missingList(header)
		found := false
		for _, item := range list {
			if item == "secrets" {
				found = true
			}
		}
		if !found {
			header.Set("missing", append(list, "secrets"))
		}
	}
	out[0] = header
	return out, nil
}

// encodeRecord serializes one line as the reference writer does.
func encodeRecord(record *pyjson.Object) ([]byte, error) {
	text, err := pyjson.Dumps(record, pyjson.Options{Compact: true})
	if err != nil {
		return nil, err
	}
	if err := pyjson.CheckUTF8(text); err != nil {
		return nil, err
	}
	return []byte(text + "\n"), nil
}

// writer applies the reference writer's checks record by record.
type writer struct {
	ids     map[string]bool
	results map[string]bool
	missing map[string]bool
}

func (w *writer) prepare(record *pyjson.Object) ([]byte, error) {
	if err := CheckSchema(record); err != nil {
		return nil, err
	}
	id, _ := record.Value("id").(string)
	if w.ids[id] {
		return nil, &pyjson.ValueError{Msg: "Duplicate record ID"}
	}
	kind := record.Value("type")
	if kind == "tool_result" {
		if c, ok := record.Value("callId").(string); ok && w.results[c] {
			return nil, &pyjson.ValueError{Msg: "Duplicate result callId"}
		}
	}
	if kind == "inference" && !record.Has("model") && !w.missing["model"] {
		return nil, &pyjson.ValueError{Msg: "Declare model in missing before an inference without a model"}
	}
	return encodeRecord(record)
}

func (w *writer) remember(record *pyjson.Object) {
	id, _ := record.Value("id").(string)
	w.ids[id] = true
	kind := record.Value("type")
	if kind == "tool_result" {
		if c, ok := record.Value("callId").(string); ok {
			w.results[c] = true
		}
	}
	if kind == "session" || kind == "session_info" {
		if list, ok := record.Value("missing").([]any); ok {
			for _, item := range list {
				if name, ok := item.(string); ok {
					w.missing[name] = true
				}
			}
		}
	}
}

// Encode returns the stream's bytes, checking each record as the reference
// writer does.
func (s *Stream) Encode() ([]byte, error) {
	var out []byte
	err := s.write(func(data []byte) error {
		out = append(out, data...)
		return nil
	}, nil)
	return out, err
}

// Interrupted, when set, is polled between records so that a write can stop
// cleanly after Ctrl-C.
var Interrupted func() bool

// ErrInterrupted reports a write stopped by Interrupted.
var ErrInterrupted = errors.New("interrupted")

func (s *Stream) write(emit func([]byte) error, open func() error) error {
	lines, err := s.Finish()
	if err != nil {
		return err
	}
	w := &writer{ids: map[string]bool{}, results: map[string]bool{}, missing: map[string]bool{}}
	head, err := w.prepare(lines[0])
	if err != nil {
		return err
	}
	if Interrupted != nil && Interrupted() {
		return ErrInterrupted
	}
	if open != nil {
		if err := open(); err != nil {
			return err
		}
	}
	if err := emit(head); err != nil {
		return err
	}
	w.remember(lines[0])
	for _, record := range lines[1:] {
		if Interrupted != nil && Interrupted() {
			return ErrInterrupted
		}
		data, err := w.prepare(record)
		if err != nil {
			return err
		}
		if err := emit(data); err != nil {
			return err
		}
		w.remember(record)
	}
	return nil
}

// Write creates a new private file and writes the stream. The file is
// removed if any record fails. An existing file is never replaced.
func (s *Stream) Write(path string) error {
	var f *os.File
	created := false
	err := s.write(func(data []byte) error {
		_, err := f.Write(data)
		return err
	}, func() error {
		var err error
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			created = true
		}
		return err
	})
	if f != nil {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil && created {
		os.Remove(path)
	}
	return err
}
