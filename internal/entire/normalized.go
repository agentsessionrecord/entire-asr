package entire

import (
	"bytes"
	"math/big"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// ASRNames maps Entire agent registry names, as transcript lines carry them,
// to ASR agent names.
var ASRNames = map[string]string{"claude-code": "claude-code", "codex": "codex", "copilot-cli": "copilot-cli",
	"cursor": "cursor", "factoryai-droid": "droid", "opencode": "opencode", "pi": "pi", "antigravity": "antigravity",
	"Gemini CLI": "gemini-cli"}

// RegistryNames maps agent type labels from checkpoint metadata to registry names.
var RegistryNames = map[string]string{"Claude Code": "claude-code", "Codex": "codex", "Copilot CLI": "copilot-cli",
	"Cursor": "cursor", "Factory AI Droid": "factoryai-droid", "OpenCode": "opencode", "Pi": "pi",
	"Antigravity": "antigravity", "Gemini CLI": "Gemini CLI"}

// UnknownAgent names sessions of agents ASR has no name for.
const UnknownAgent = "org.agentsessionrecord.entire-unknown"

var tokenKeys = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"cache_write_5m_tokens", "cache_write_1h_tokens", "reasoning_tokens"}

// Line is one physical transcript line and its parsed form, or nil when it
// is not a complete Entire Transcript Format v1 line.
type Line struct {
	Raw  []byte
	Line *pyjson.Object
}

// version reads the leading major.minor.patch of a CLI version.
func version(v any) ([3]*big.Int, bool) {
	var out [3]*big.Int
	s, ok := v.(string)
	if !ok {
		return out, false
	}
	r := pyjson.Runes(s)
	i := 0
	if i < len(r) && r[i] == 'v' {
		i++
	}
	for part := 0; part < 3; part++ {
		if part > 0 {
			if i >= len(r) || r[i] != '.' {
				return out, false
			}
			i++
		}
		start := i
		for i < len(r) && pyjson.IsDigit(r[i]) {
			i++
		}
		if i == start {
			return out, false
		}
		n, _, err := digitsValue(string(r[start:i]))
		if err != nil || n == nil {
			return out, false
		}
		out[part] = n
	}
	return out, true
}

// perCall reports whether a line comes from Entire 0.12.2 or later, whose
// converters write one line per model call with per-call usage.
func perCall(v any) bool {
	ver, ok := version(v)
	if !ok {
		return false
	}
	for i, want := range []int64{0, 12, 2} {
		if c := ver[i].Cmp(big.NewInt(want)); c != 0 {
			return c > 0
		}
	}
	return true
}

// Parse splits a transcript into physical lines, the unit of compact_transcript_start.
func Parse(data []byte) ([]Line, error) {
	if len(data) == 0 {
		return nil, nil
	}
	pieces := bytes.Split(data, []byte{'\n'})
	if len(pieces) > asr.MaxRecords {
		return nil, &pyjson.ValueError{Msg: "input_record_limit"}
	}
	complete, tail := pieces[:len(pieces)-1], pieces[len(pieces)-1]
	result := make([]Line, 0, len(pieces))
	for _, raw := range complete {
		result = append(result, Line{raw, parseLine(raw)})
	}
	if len(tail) > 0 {
		// An incomplete final line is never read as a complete record.
		result = append(result, Line{tail, nil})
	}
	return result, nil
}

func parseLine(raw []byte) *pyjson.Object {
	text, err := pyjson.DecodeStrict(raw)
	if err != nil || pyjson.RuneLen(text) > asr.MaxInputBytes {
		return nil
	}
	value, err := pyjson.Parse(text, true)
	if err != nil {
		return nil
	}
	line, ok := value.(*pyjson.Object)
	if !ok {
		return nil
	}
	v, present := line.Get("v")
	if !present || !pyjson.Equal(v, int64(1)) {
		return nil
	}
	if _, ok := pyjson.NonEmptyStr(line.Value("type")); !ok {
		return nil
	}
	return line
}

func stripResults(content any) any {
	list, ok := content.([]any)
	if !ok {
		return content
	}
	out := make([]any, len(list))
	for i, block := range list {
		if b, ok := block.(*pyjson.Object); ok && b.Value("type") == "tool_use" {
			out[i] = b.Without("result")
		} else {
			out[i] = block
		}
	}
	return out
}

// lineKey identifies a line across checkpoint views, ignoring late tool
// results and usage.
type lineKey struct {
	n       int
	a, b, c string
}

func keyOf(l Line) lineKey {
	if l.Line == nil {
		return lineKey{n: 2, a: "raw", b: string(l.Raw)}
	}
	kind, _ := l.Line.Value("type").(string)
	if id, ok := pyjson.NonEmptyStr(l.Line.Value("id")); ok {
		return lineKey{n: 3, a: kind, b: "id", c: id}
	}
	text, err := pyjson.Dumps(stripResults(l.Line.Value("content")),
		pyjson.Options{EnsureASCII: true, SortKeys: true, DecimalAsString: true})
	if err != nil {
		text = "\x00" + err.Error()
	}
	return lineKey{n: 3, a: kind, b: "content", c: text}
}

// extends reports whether a later checkpoint's line restates an earlier view of the same line.
func extends(earlier, later Line) bool {
	a, b := earlier.Line, later.Line
	if a == nil || b == nil {
		return bytes.Equal(earlier.Raw, later.Raw)
	}
	if !pyjson.Equal(a.Value("type"), b.Value("type")) {
		return false
	}
	if pyjson.Truthy(a.Value("id")) || pyjson.Truthy(b.Value("id")) {
		return pyjson.Equal(a.Value("id"), b.Value("id"))
	}
	ca, cb := stripResults(a.Value("content")), stripResults(b.Value("content"))
	la, aok := ca.([]any)
	lb, bok := cb.([]any)
	if aok && bok {
		if len(la) > len(lb) {
			return false
		}
		return pyjson.Equal(la, lb[:len(la)])
	}
	return pyjson.Equal(ca, cb)
}

// Mapper writes ASR records for the normalized lines of one session, in file order.
type Mapper struct {
	stream           *asr.Stream
	agent            string
	calls            map[string]string
	inferences       []*pyjson.Object
	usageOutsideCall bool
	assets           bool
	userText         bool
}

// NewMapper returns a mapper for a session of the named registry agent.
func NewMapper(stream *asr.Stream, agent string) *Mapper {
	return &Mapper{stream: stream, agent: agent, calls: map[string]string{}}
}

func (m *Mapper) gap(detail string, meta any, ts any) error {
	_, err := m.stream.Gap(asr.Unparseable, detail, nil, ts, meta)
	return err
}

func withKey(meta *pyjson.Object, key string, value any) *pyjson.Object {
	c := meta.Copy()
	c.Set(key, value)
	return c
}

// Line maps one transcript line. meta holds its position and checkpoint.
func (m *Mapper) Line(raw []byte, line *pyjson.Object, meta *pyjson.Object) error {
	if line == nil {
		return m.gap("A normalized transcript line could not be read.", withKey(meta, "line", pyjson.DecodeReplace(raw)), nil)
	}
	agent := m.agent
	if a, ok := pyjson.NonEmptyStr(line.Value("agent")); ok {
		agent = a
	}
	ts := line.Value("ts")
	fields := line.Without("content")
	switch line.Value("type") {
	case "user":
		return m.content(line, "user", ts, withKey(meta, "line", fields), nil, userID(line))
	case "assistant":
		return m.assistant(line, agent, ts, withKey(meta, "line", fields))
	}
	// Readers keep unknown line types so that consumers can skip them.
	_, err := m.stream.Add("session_info", nil, ts, withKey(meta, "nativeRecord", line),
		asr.F{K: "missing", V: []any{"native_events"}})
	return err
}

func (m *Mapper) assistant(line *pyjson.Object, agent string, ts any, meta *pyjson.Object) error {
	hasUsage := false
	for _, name := range tokenKeys {
		if line.Has(name) {
			hasUsage = true
		}
	}
	current := perCall(line.Value("cli_version"))
	identity, hasIdentity := pyjson.NonEmptyStr(line.Value("id"))
	var provisional string
	switch {
	case agent == "claude-code" && hasIdentity:
		// Fragments of one message ID form one call. A continuation line
		// after tool results carries no usage. A reused ID with usage starts
		// a separate call, as Entire's fold keys it.
		if call, seen := m.calls[identity]; seen && !hasUsage {
			return m.content(line, "assistant", ts, meta, call, nil)
		}
		provisional = identity
	case agent == "pi" || (agent == "codex" && current && hasUsage):
		if hasIdentity {
			provisional = identity
		} else {
			provisional = asr.DeriveID(m.stream.Scope, "inference", len(m.stream.Records), 0)
		}
	case hasUsage:
		// Other converters write per-message counts without call boundaries.
		m.usageOutsideCall = true
	}
	start := len(m.stream.Records)
	var inference any
	if provisional != "" {
		inference = provisional
	}
	if err := m.content(line, "assistant", ts, meta, inference, nil); err != nil {
		return err
	}
	if provisional == "" {
		return nil
	}
	var fields []asr.F
	if model, ok := pyjson.NonEmptyStr(line.Value("model")); ok {
		// Claude Code reports the model that served the call. Codex and Pi
		// lines carry the model the agent configured for the request.
		if agent == "claude-code" {
			fields = append(fields, asr.F{K: "model", V: pyjson.Obj("id", model)})
		} else {
			fields = append(fields, asr.F{K: "model", V: pyjson.Obj("requested", model)})
		}
	}
	if usage := callUsage(line, agent, current); usage.Len() > 0 {
		fields = append(fields, asr.F{K: "usage", V: usage})
	}
	counts := pyjson.NewObject()
	for _, name := range tokenKeys {
		if v, ok := line.Get(name); ok {
			counts.Set(name, v)
		}
	}
	inferMeta := meta.Without("line")
	inferMeta.Set("usage", counts)
	if hasIdentity {
		inferMeta.Set("messageId", identity)
	}
	actual, err := m.stream.Add("inference", provisional, ts, inferMeta, fields...)
	if err != nil {
		return err
	}
	m.inferences = append(m.inferences, actual)
	id := actual.Value("id").(string)
	if id != provisional {
		for _, record := range m.stream.Records[start:] {
			if v, ok := record.Get("inferenceId"); ok && v == provisional {
				record.Set("inferenceId", id)
			}
		}
	}
	if agent == "claude-code" {
		m.calls[identity] = id
	}
	return nil
}

// content writes the message, tool and gap records of one line's content.
// inference is nil or the call the line belongs to.
func (m *Mapper) content(line *pyjson.Object, role string, ts any, meta *pyjson.Object, inference any, sourceID any) error {
	value := line.Value("content")
	var content []any
	switch x := value.(type) {
	case string:
		content = []any{pyjson.Obj("type", "text", "text", x)}
	case nil:
		content = []any{}
	case []any:
		content = x
	default:
		return m.gap("A normalized line has content in an unknown shape.", withKey(meta, "content", value), ts)
	}
	if role == "user" {
		m.userText = true
	}
	var parts []any
	pendingMeta, pendingSource := meta, sourceID
	stripped := meta.Without("line")
	// Line fields go on the first record made from the line.
	first := func() (*pyjson.Object, any) {
		v, s := pendingMeta, pendingSource
		pendingMeta, pendingSource = stripped, nil
		return v, s
	}
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		v, s := first()
		fields := []asr.F{{K: "role", V: role}, {K: "content", V: append([]any(nil), parts...)}}
		if inference != nil {
			fields = append(fields, asr.F{K: "inferenceId", V: inference})
		}
		parts = parts[:0]
		_, err := m.stream.Add("message", s, ts, v, fields...)
		return err
	}
	for _, item := range content {
		block, ok := item.(*pyjson.Object)
		if !ok {
			if err := flush(); err != nil {
				return err
			}
			v, _ := first()
			if err := m.gap("A normalized content block is not an object.", withKey(v, "block", item), ts); err != nil {
				return err
			}
			continue
		}
		kind, present := block.Get("type")
		if !present {
			if _, isStr := block.Value("text").(string); isStr {
				kind = "text"
			} else {
				kind = nil
			}
		}
		text, textOK := block.Value("text").(string)
		var part *pyjson.Object
		if kind == "image" {
			part = m.image(block)
		}
		switch {
		case kind == "text" && textOK:
			parts = append(parts, pyjson.Obj("type", "text", "text", text))
		case kind == "image" && part != nil:
			parts = append(parts, part)
		case kind == "tool_use":
			if err := flush(); err != nil {
				return err
			}
			v, _ := first()
			if err := m.tool(block, ts, v, inference); err != nil {
				return err
			}
		default:
			if err := flush(); err != nil {
				return err
			}
			v, _ := first()
			if _, err := m.stream.Gap("unknown", "An unsupported content block is retained as metadata.", nil, ts,
				withKey(v, "block", block)); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if len(content) == 0 && role == "user" {
		v, s := first()
		_, err := m.stream.Add("message", s, ts, v, asr.F{K: "role", V: role}, asr.F{K: "content", V: []any{}})
		return err
	}
	return nil
}

func (m *Mapper) image(block *pyjson.Object) *pyjson.Object {
	source := pyjson.AsObject(block.Value("source"))
	if source == nil {
		source = pyjson.NewObject()
	}
	mime, ok := source.Get("media_type")
	if !ok {
		mime = block.Value("mimeType")
	}
	data, ok := source.Get("data")
	if !ok {
		data = block.Value("data")
	}
	mimeType, ok := pyjson.NonEmptyStr(mime)
	if !ok {
		return nil
	}
	part := pyjson.Obj("type", "image", "mimeType", mimeType)
	if s, isStr := data.(string); isStr && len(s) >= len("entire-asset:") && s[:len("entire-asset:")] == "entire-asset:" {
		// Image externalization moved the bytes to checkpoint assets.
		m.assets = true
	} else if isStr && s != "" {
		part.Set("data", s)
	}
	return part
}

var statusNames = map[string]string{"success": "completed", "error": "failed"}

func (m *Mapper) tool(block *pyjson.Object, ts any, meta *pyjson.Object, inference any) error {
	callID, ok := pyjson.NonEmptyStr(block.Value("id"))
	if !ok {
		callID = asr.DeriveID(m.stream.Scope, "call", len(m.stream.Records), 0)
	}
	result := pyjson.AsObject(block.Value("result"))
	fields := []asr.F{{K: "callId", V: callID}}
	if name, ok := block.Value("name").(string); ok {
		fields = append(fields, asr.F{K: "name", V: name})
	}
	if input, ok := block.Get("input"); ok {
		fields = append(fields, asr.F{K: "input", V: input})
	}
	if m.agent == "claude-code" {
		fields = append(fields, asr.F{K: "requestedBy", V: "model"})
	}
	var target *pyjson.Object
	if result != nil && result.Len() > 0 {
		target = pyjson.AsObject(result.Value("file"))
	}
	if target != nil {
		if p, ok := pyjson.NonEmptyStr(target.Value("filePath")); ok {
			fields = append(fields, asr.F{K: "locations", V: []any{pyjson.Obj("path", p)}})
		}
	}
	if inference != nil {
		fields = append(fields, asr.F{K: "inferenceId", V: inference})
	}
	if _, err := m.stream.Add("tool_call", block.Value("id"), ts, withKey(meta, "block", block.Without("result")), fields...); err != nil {
		return err
	}
	if result == nil {
		if block.Has("result") {
			return m.gap("A tool result has an unknown shape.", pyjson.Obj("block", block), nil)
		}
		return nil
	}
	status := "unknown"
	switch s := result.Value("status").(type) {
	case []any, *pyjson.Object:
		return &pyjson.TypeError{Msg: "unhashable type"}
	case string:
		if name, ok := statusNames[s]; ok {
			status = name
		}
	}
	out := []asr.F{{K: "callId", V: callID}, {K: "status", V: status}}
	// Entire writes an empty string when no text output was captured.
	if output, ok := pyjson.NonEmptyStr(result.Value("output")); ok {
		out = append(out, asr.F{K: "content", V: []any{pyjson.Obj("type", "text", "text", output)}})
	}
	resultMeta := meta.Without("line")
	resultMeta.Set("result", result.Without("output"))
	_, err := m.stream.Add("tool_result", nil, nil, resultMeta, out...)
	return err
}

// Losses returns default losses this session's records show were captured.
func (m *Mapper) Losses() []string {
	var captured []string
	if len(m.inferences) > 0 && !m.usageOutsideCall {
		all := true
		for _, r := range m.inferences {
			usage := pyjson.AsObject(r.Value("usage"))
			if usage == nil || !usage.Has("inputTokens") || !usage.Has("outputTokens") {
				all = false
			}
		}
		if all {
			captured = append(captured, "usage")
		}
	}
	if len(m.inferences) > 0 {
		all := true
		for _, r := range m.inferences {
			if !r.Has("model") {
				all = false
			}
		}
		if all {
			captured = append(captured, "model")
		}
	}
	return captured
}

// callUsage normalizes one call's usage to ASR inclusive totals, leaving unknowns absent.
func callUsage(line *pyjson.Object, agent string, current bool) *pyjson.Object {
	valid := map[string]int64{}
	for _, name := range tokenKeys {
		if n, ok := pyjson.Count(line.Value(name)); ok {
			valid[name] = n
		}
	}
	usage := pyjson.NewObject()
	if n, ok := valid["output_tokens"]; ok {
		usage.Set("outputTokens", n)
	}
	if n, ok := valid["reasoning_tokens"]; ok {
		usage.Set("reasoningTokens", n)
	}
	if current && (agent == "claude-code" || agent == "codex" || agent == "pi") {
		// On these lines input_tokens excludes the line's cache reads and writes.
		if n, ok := valid["cache_read_tokens"]; ok {
			usage.Set("cacheReadTokens", n)
		}
		if n, ok := valid["cache_write_tokens"]; ok {
			usage.Set("cacheWriteTokens", n)
		}
		parts := []string{"input_tokens", "cache_read_tokens", "cache_write_tokens"}
		if _, ok := valid["input_tokens"]; ok {
			complete := true
			var total int64
			for _, name := range parts {
				n, isValid := valid[name]
				if !isValid && line.Has(name) {
					complete = false
				}
				total += n
			}
			if complete && total <= pyjson.MaxInt {
				usage.Set("inputTokens", total)
			}
		}
	}
	// Older lines hold fresh input only, or input whose cache share is unknown.
	return usage
}

func userID(line *pyjson.Object) any {
	if id, ok := pyjson.NonEmptyStr(line.Value("id")); ok {
		return id
	}
	if content, ok := line.Value("content").([]any); ok && len(content) > 0 {
		if first := pyjson.AsObject(content[0]); first != nil {
			if id, ok := pyjson.NonEmptyStr(first.Value("id")); ok {
				return id
			}
		}
	}
	return nil
}
