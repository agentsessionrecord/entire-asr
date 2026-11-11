package entire

import (
	"bytes"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// jsonRow is one raw transcript line, or nil when it is malformed or incomplete.
type jsonRow struct {
	position int
	row      *pyjson.Object
}

// splitKeepEnds is bytes.splitlines(keepends=True): lines end at \n, \r\n or \r.
func splitKeepEnds(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\n':
			out = append(out, data[start:i+1])
			start = i + 1
		case '\r':
			if i+1 < len(data) && data[i+1] == '\n' {
				i++
			}
			out = append(out, data[start:i+1])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// jsonLines is importer-core's json_lines: a nil row for each malformed or
// incomplete native line.
func jsonLines(data []byte) ([]jsonRow, error) {
	if len(data) > asr.MaxInputBytes {
		return nil, &pyjson.ValueError{Msg: "input_byte_limit"}
	}
	var rows []jsonRow
	for position, raw := range splitKeepEnds(data) {
		if position >= asr.MaxRecords {
			return nil, &pyjson.ValueError{Msg: "input_record_limit"}
		}
		var row *pyjson.Object
		if bytes.HasSuffix(raw, []byte{'\n'}) {
			if text, err := pyjson.DecodeStrict(raw); err == nil && pyjson.RuneLen(text) <= asr.MaxInputBytes {
				if value, err := pyjson.Parse(text, true); err == nil {
					row, _ = value.(*pyjson.Object)
				}
			}
		}
		rows = append(rows, jsonRow{position, row})
	}
	return rows, nil
}

var usageMapping = []struct{ key, target string }{
	{"input_tokens", "inputTokens"}, {"output_tokens", "outputTokens"},
	{"cache_read_input_tokens", "cacheReadTokens"}, {"cached_input_tokens", "cacheReadTokens"},
	{"cache_creation_input_tokens", "cacheWriteTokens"}, {"cache_creation_tokens", "cacheWriteTokens"},
	{"cache_read_tokens", "cacheReadTokens"}, {"cache_write_input_tokens", "cacheWriteTokens"},
	{"reasoning_output_tokens", "reasoningTokens"},
}

// usage is importer-core's usage mapping. Anthropic input totals include
// cache reads and writes.
func usage(value *pyjson.Object, anthropic bool) *pyjson.Object {
	result := pyjson.NewObject()
	for _, m := range usageMapping {
		if v, ok := value.Get(m.key); ok {
			if n, valid := pyjson.Count(v); valid {
				result.Set(m.target, n)
			}
		}
	}
	if anthropic && result.Has("inputTokens") {
		// Anthropic base input excludes cache reads and writes. An invalid optional
		// component cannot be treated as zero in the inclusive total.
		invalid := false
		for _, m := range usageMapping {
			if m.target != "cacheReadTokens" && m.target != "cacheWriteTokens" {
				continue
			}
			if v, ok := value.Get(m.key); ok {
				if _, valid := pyjson.Count(v); !valid {
					invalid = true
				}
			}
		}
		if invalid {
			result.Delete("inputTokens")
		} else {
			total := result.Value("inputTokens").(int64)
			if n, ok := result.Value("cacheReadTokens").(int64); ok {
				total += n
			}
			if n, ok := result.Value("cacheWriteTokens").(int64); ok {
				total += n
			}
			if total <= pyjson.MaxInt {
				result.Set("inputTokens", total)
			} else {
				result.Delete("inputTokens")
			}
		}
	}
	return result
}

// textParts is importer-core's text_parts.
func textParts(value any) []any {
	switch x := value.(type) {
	case string:
		return []any{pyjson.Obj("type", "text", "text", x)}
	case []any:
		result := []any{}
		for _, part := range x {
			p := pyjson.AsObject(part)
			if p == nil {
				continue
			}
			text, ok := p.Value("text").(string)
			kind := p.Value("type")
			if ok && (kind == "text" || kind == "input_text" || kind == "output_text") {
				result = append(result, pyjson.Obj("type", "text", "text", text))
			}
		}
		return result
	}
	return []any{}
}

func inStrings(v any, options ...string) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}

func nativeRecord(row *pyjson.Object) *pyjson.Object { return pyjson.Obj("nativeRecord", row) }

func rowValue(row *pyjson.Object) any {
	if row == nil {
		return nil
	}
	return row
}

// NativeTranscript maps a Claude Code or Codex raw full.jsonl transcript.
func NativeTranscript(stream *asr.Stream, data []byte, agent string) error {
	rows, err := jsonLines(data)
	if err != nil {
		return err
	}
	// A retained UUID can have several snapshots. Keep its completion point.
	lastUUID := map[string]int{}
	lastCall := map[string]int{}
	for _, r := range rows {
		row := r.row
		if row == nil || row.Len() == 0 || agent != "claude-code" {
			continue
		}
		if pyjson.Truthy(row.Value("uuid")) {
			lastUUID[pyjson.PyStr(row.Value("uuid"))] = r.position
		}
		message := pyjson.AsObject(row.Value("message"))
		if row.Value("type") == "assistant" && message != nil && pyjson.Truthy(message.Value("id")) {
			lastCall[pyjson.PyStr(message.Value("id"))] = r.position
		}
	}
	var requestedModel any
	settings := pyjson.NewObject()
	inferenceIDs := map[string]bool{}
	for _, r := range rows {
		row := r.row
		if row == nil {
			if _, err := stream.Gap(asr.Unparseable, asr.DefaultDetail, nil, nil,
				pyjson.Obj("sourceLine", int64(r.position+1))); err != nil {
				return err
			}
			continue
		}
		ts := row.Value("timestamp")
		switch agent {
		case "claude-code":
			if err := claudeRow(stream, row, ts, r.position, lastUUID, lastCall); err != nil {
				return err
			}
		case "codex":
			if err := codexRow(stream, row, ts, &requestedModel, &settings, inferenceIDs); err != nil {
				return err
			}
		default:
			if _, err := stream.Gap("unknown", "This checkpoint's native agent format is unsupported.", nil, ts,
				nativeRecord(row)); err != nil {
				return err
			}
		}
	}
	return nil
}

func claudeRow(stream *asr.Stream, row *pyjson.Object, ts any, position int, lastUUID, lastCall map[string]int) error {
	if pyjson.Truthy(row.Value("uuid")) && lastUUID[pyjson.PyStr(row.Value("uuid"))] != position {
		return nil
	}
	message := pyjson.AsObject(row.Value("message"))
	kind := row.Value("type")
	if !inStrings(kind, "user", "assistant") || message == nil {
		_, err := stream.Add("session_info", nil, ts, nativeRecord(row), asr.F{K: "missing", V: []any{"native_events"}})
		return err
	}
	var inference string
	if kind == "assistant" && pyjson.Truthy(message.Value("id")) {
		inference = pyjson.PyStr(message.Value("id"))
	}
	// Reserve a stable provider ID for the later inference. Native messages
	// use UUIDs and calls use their own tool IDs.
	if err := Blocks(stream, message, row.Value("uuid"), ts, row.Without("message"), kind == "assistant", inference, true); err != nil {
		return err
	}
	if inference != "" && lastCall[inference] == position {
		var fields []asr.F
		if model, ok := pyjson.NonEmptyStr(message.Value("model")); ok {
			fields = append(fields, asr.F{K: "model", V: pyjson.Obj("provider", "anthropic", "id", model)})
		}
		if u := pyjson.AsObject(message.Value("usage")); u != nil {
			fields = append(fields, asr.F{K: "usage", V: usage(u, true)})
		}
		if stop, ok := message.Value("stop_reason").(string); ok {
			fields = append(fields, asr.F{K: "stopReason", V: stop})
		}
		record, err := stream.Add("inference", inference, ts, pyjson.Obj("messageUsage", message.Value("usage")), fields...)
		if err != nil {
			return err
		}
		if actual := record.Value("id").(string); actual != inference {
			// A native ID collision must not break links.
			for _, r := range stream.Records {
				if v, ok := r.Get("inferenceId"); ok && v == inference {
					r.Set("inferenceId", actual)
				}
			}
		}
	}
	if row.Value("isCompactSummary") == true {
		uuid := row.Value("uuid")
		for _, r := range stream.Records {
			if r.Value("type") != "message" {
				continue
			}
			var recorded any
			if m := pyjson.AsObject(r.Value("_meta")); m != nil {
				if inner := pyjson.AsObject(m.Value(asr.META)); inner != nil {
					recorded = inner.Value("uuid")
				}
			}
			if pyjson.Equal(recorded, uuid) {
				r.Set("origin", "compaction")
				r.Set("author", "harness")
			}
		}
		if _, err := stream.Gap("not_retained", "The source retains a compaction summary.", nil, ts, nil); err != nil {
			return err
		}
	}
	return nil
}

func codexRow(stream *asr.Stream, row *pyjson.Object, ts any, requestedModel *any, settings **pyjson.Object, inferenceIDs map[string]bool) error {
	payload := pyjson.AsObject(row.Value("payload"))
	if payload == nil {
		_, err := stream.Gap(asr.Unparseable, asr.DefaultDetail, nil, ts, nativeRecord(row))
		return err
	}
	nativeEvent := func(missing string) error {
		_, err := stream.Add("session_info", nil, ts, nativeRecord(row), asr.F{K: "missing", V: []any{missing}})
		return err
	}
	switch kind := row.Value("type"); {
	case kind == "turn_context":
		*requestedModel = payload.Value("model")
		next := pyjson.NewObject()
		for _, m := range [][2]string{{"effort", "reasoningEffort"}, {"service_tier", "serviceTier"}} {
			if s, ok := payload.Value(m[0]).(string); ok {
				next.Set(m[1], s)
			}
		}
		*settings = next
		policies := pyjson.NewObject()
		for _, m := range [][2]string{{"approval_policy", "approval"}, {"sandbox_policy", "sandbox"}} {
			if v, ok := payload.Get(m[0]); ok {
				policies.Set(m[1], v)
			}
		}
		if policies.Len() > 0 {
			_, err := stream.Add("session_info", nil, ts, nativeRecord(row), asr.F{K: "agentSettings", V: policies})
			return err
		}
	case kind == "response_item":
		itemType := payload.Value("type")
		switch {
		case itemType == "message" && inStrings(payload.Value("role"), "user", "assistant", "system", "developer"):
			content := textParts(payload.Value("content"))
			native, isList := payload.Value("content").([]any)
			shortened := isList && len(content) != len(native)
			if shortened {
				stream.Missing("content")
			}
			fields := []asr.F{{K: "role", V: payload.Value("role")}, {K: "content", V: content}}
			if shortened {
				fields = append(fields, asr.F{K: "truncated", V: true})
			}
			_, err := stream.Add("message", payload.Value("id"), ts, nativeRecord(row), fields...)
			return err
		case inStrings(itemType, "function_call", "custom_tool_call"):
			callID, ok := payload.Value("call_id").(string)
			if !ok {
				return nativeEvent("native_events")
			}
			value, ok := payload.Get("arguments")
			if !ok {
				value = payload.Value("input")
			}
			fields := []asr.F{{K: "callId", V: callID}}
			if name, ok := payload.Value("name").(string); ok {
				fields = append(fields, asr.F{K: "name", V: name})
			}
			fields = append(fields, asr.F{K: "requestedBy", V: "model"})
			if s, isStr := value.(string); isStr {
				fields = append(fields, asr.F{K: "inputText", V: s})
			} else if value != nil {
				fields = append(fields, asr.F{K: "input", V: value})
			}
			_, err := stream.Add("tool_call", payload.Value("id"), ts, nativeRecord(row), fields...)
			return err
		case inStrings(itemType, "function_call_output", "custom_tool_call_output"):
			content := textParts(payload.Value("output"))
			native, isList := payload.Value("output").([]any)
			shortened := isList && len(content) != len(native)
			if shortened {
				stream.Missing("tool_output")
			}
			fields := []asr.F{{K: "status", V: "unknown"}}
			if callID, ok := payload.Value("call_id").(string); ok {
				fields = append(fields, asr.F{K: "callId", V: callID})
			}
			fields = append(fields, asr.F{K: "content", V: content})
			if shortened {
				fields = append(fields, asr.F{K: "truncated", V: true})
			}
			_, err := stream.Add("tool_result", payload.Value("id"), ts, nativeRecord(row), fields...)
			return err
		case itemType == "reasoning":
			reasoning := pyjson.Obj("type", "reasoning", "visibility", "encrypted")
			if summary, ok := payload.Value("summary").([]any); ok {
				texts := []any{}
				for _, part := range summary {
					if p := pyjson.AsObject(part); p != nil {
						if text, ok := p.Value("text").(string); ok {
							texts = append(texts, text)
						}
					}
				}
				reasoning.Set("visibility", "summary")
				reasoning.Set("summary", texts)
			}
			_, err := stream.Add("message", payload.Value("id"), ts, nil,
				asr.F{K: "role", V: "assistant"}, asr.F{K: "content", V: []any{reasoning}})
			return err
		default:
			return nativeEvent("native_events")
		}
	case kind == "token_usage_record":
		responseID, ok := payload.Value("response_id").(string)
		if !ok {
			return nativeEvent("native_events")
		}
		if inferenceIDs[responseID] {
			return nativeEvent("duplicate_usage_observations")
		}
		inferenceIDs[responseID] = true
		fields := []asr.F{{K: "settings", V: *settings}}
		if model, ok := pyjson.NonEmptyStr(*requestedModel); ok {
			fields = append(fields, asr.F{K: "model", V: pyjson.Obj("requested", model)})
		}
		if u := pyjson.AsObject(payload.Value("usage")); u != nil {
			fields = append(fields, asr.F{K: "usage", V: usage(u, false)})
		}
		_, err := stream.Add("inference", responseID, ts, nativeRecord(row), fields...)
		return err
	default:
		return nativeEvent("native_events")
	}
	return nil
}

type snapshot struct {
	metadata *pyjson.Object
	data     []byte
}

func identityOf(row *pyjson.Object) (any, bool) {
	if row == nil || row.Len() == 0 {
		return nil, false
	}
	payload, hasPayload := row.Get("payload")
	payloadObj := pyjson.AsObject(payload)
	if hasPayload && payloadObj == nil {
		return nil, false
	}
	if uuid := row.Value("uuid"); pyjson.Truthy(uuid) {
		return uuid, true
	}
	if payloadObj == nil {
		return nil, true
	}
	return payloadObj.Value("id"), true
}

// mergeSnapshots merges whole-session snapshots in checkpoint order without repeating work.
func mergeSnapshots(stream *asr.Stream, snapshots []snapshot) ([]byte, error) {
	// full.jsonl is a whole-session snapshot. Merge only provable prefix
	// extensions. Divergent retained branches remain visible with a gap.
	merged := []byte{}
	for _, s := range snapshots {
		if s.data == nil {
			if _, err := stream.Gap("not_retained", "The checkpoint has no raw transcript.", nil, nil,
				pyjson.Obj("metadata", s.metadata)); err != nil {
				return nil, err
			}
			continue
		}
		if bytes.HasPrefix(s.data, merged) {
			merged = s.data
			continue
		}
		if bytes.HasPrefix(merged, s.data) {
			continue
		}
		stream.Missing("checkpoint_order")
		// Export both snapshots below. Matching native IDs, rather than
		// matching text, are the only deduplication key for the merge.
		if _, err := stream.Gap("unknown", "Retained checkpoint snapshots diverge. Their cross-checkpoint event order is unresolved.",
			nil, nil, nil); err != nil {
			return nil, err
		}
		oldRows, err := jsonLines(merged)
		if err != nil {
			return nil, err
		}
		identities := map[string]*pyjson.Object{}
		for _, r := range oldRows {
			identity, ok := identityOf(r.row)
			if !ok {
				continue
			}
			key, err := pyjson.HashKey(identity)
			if err != nil {
				return nil, err
			}
			identities[key] = r.row
		}
		delete(identities, "N")
		newRows, err := jsonLines(s.data)
		if err != nil {
			return nil, err
		}
		var additional []*pyjson.Object
		for _, r := range newRows {
			identity, ok := identityOf(r.row)
			if !ok || identity == nil {
				additional = append(additional, r.row)
				continue
			}
			key, err := pyjson.HashKey(identity)
			if err != nil {
				return nil, err
			}
			previous, seen := identities[key]
			if !seen {
				additional = append(additional, r.row)
			} else if !pyjson.Equal(previous, r.row) {
				if _, err := stream.Gap("unknown", "Conflicting retained native revisions are preserved here without merging their claims.",
					nil, nil, nativeRecord(r.row)); err != nil {
					return nil, err
				}
			}
		}
		next := append([]byte(nil), merged...)
		for _, row := range additional {
			text, err := pyjson.Dumps(pyjson.Native(rowValue(row)), pyjson.Options{EnsureASCII: true})
			if err != nil {
				return nil, err
			}
			next = append(next, text...)
			next = append(next, '\n')
		}
		merged = next
	}
	return merged, nil
}
