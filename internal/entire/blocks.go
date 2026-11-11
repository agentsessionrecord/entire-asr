package entire

import (
	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/pyjson"
)

// Blocks maps an Anthropic message's content blocks, as the ASR Anthropic
// Compliance importer's blocks function does. inference is empty when the
// message belongs to no call. With nativeInput, tool input is kept as given.
func Blocks(stream *asr.Stream, message *pyjson.Object, sourceID, ts any, meta *pyjson.Object,
	verified bool, inference string, nativeInput bool) error {
	role := message.Value("role")
	value := message.Value("content")
	var content []any
	isList := false
	switch x := value.(type) {
	case string:
		content, isList = []any{pyjson.Obj("type", "text", "text", x)}, true
	case []any:
		content, isList = x, true
	}
	if !inStrings(role, "user", "assistant", "system", "developer") || !isList {
		_, err := stream.Gap(asr.Unparseable, asr.DefaultDetail, nil, ts, metaValue(meta))
		return err
	}
	if len(content) == 0 {
		if _, err := stream.Add("message", sourceID, ts, metaValue(meta), asr.F{K: "role", V: role},
			asr.F{K: "content", V: []any{}}); err != nil {
			return err
		}
	}
	usedMessageID := false
	nextSource := func() any {
		if usedMessageID {
			return nil
		}
		return sourceID
	}
	for _, item := range content {
		part, ok := item.(*pyjson.Object)
		if !ok {
			if _, err := stream.Gap(asr.Unparseable, asr.DefaultDetail, nil, ts, nil); err != nil {
				return err
			}
			continue
		}
		base := pyjson.NewObject()
		if meta != nil {
			base = meta.Copy()
		}
		base.Set("block", part)
		var common []asr.F
		if inference != "" {
			common = append(common, asr.F{K: "inferenceId", V: inference})
		}
		shortened := part.Value("truncated") == true
		if shortened {
			common = append(common, asr.F{K: "truncated", V: true})
			stream.Missing("content")
		}
		kind := part.Value("type")
		text, textOK := part.Value("text").(string)
		callID, callOK := pyjson.NonEmptyStr(part.Value("id"))
		switch {
		case kind == "text" && textOK:
			fields := []asr.F{{K: "role", V: role}, {K: "content", V: []any{pyjson.Obj("type", "text", "text", text)}}}
			if verified && role == "assistant" {
				fields = append(fields, asr.F{K: "author", V: "model"})
			}
			if inStrings(role, "system", "developer") {
				fields = append(fields, asr.F{K: "origin", V: "instructions"})
			}
			var provenance any
			if meta != nil {
				provenance = meta.Value("provenance")
			}
			prov := pyjson.AsObject(provenance)
			if pyjson.Truthy(provenance) && prov == nil {
				stream.Missing("provenance")
			}
			if prov != nil && prov.Value("type") == "synthetic_marker" {
				fields = setField(fields, "author", "harness")
				fields = setField(fields, "origin", "notice")
			}
			if _, err := stream.Add("message", nextSource(), ts, base, append(common, fields...)...); err != nil {
				return err
			}
			usedMessageID = true
		case kind == "tool_use" && callOK:
			fields := []asr.F{{K: "callId", V: callID}}
			if name, ok := part.Value("name").(string); ok {
				fields = append(fields, asr.F{K: "name", V: name})
			}
			if verified && role == "assistant" {
				fields = append(fields, asr.F{K: "requestedBy", V: "model"})
			}
			if input, ok := part.Get("input"); ok {
				if nativeInput {
					fields = append(fields, asr.F{K: "input", V: input})
				} else if s, isStr := input.(string); isStr {
					if shortened {
						fields = append(fields, asr.F{K: "inputText", V: s})
					} else if parsed, err := pyjson.Parse(s, true); err == nil {
						fields = append(fields, asr.F{K: "input", V: parsed})
					} else {
						fields = append(fields, asr.F{K: "inputText", V: s})
						if _, err := stream.Gap(asr.Unparseable, "An untruncated tool input was not valid JSON.", nil, ts, nil); err != nil {
							return err
						}
					}
				} else if _, err := stream.Gap(asr.Unparseable, "The documented tool input was not a JSON string.", nil, ts, nil); err != nil {
					return err
				}
			}
			if _, err := stream.Add("tool_call", callID, ts, base, append(common, fields...)...); err != nil {
				return err
			}
		case kind == "tool_result":
			status := "unknown"
			switch part.Value("is_error") {
			case true:
				status = "failed"
			case false:
				status = "completed"
			}
			fields := []asr.F{{K: "status", V: status}}
			if c, ok := pyjson.NonEmptyStr(part.Value("tool_use_id")); ok {
				fields = append(fields, asr.F{K: "callId", V: c})
			}
			if native, ok := part.Get("content"); ok {
				parts := textParts(native)
				fields = append(fields, asr.F{K: "content", V: parts})
				nativeList, isList := native.([]any)
				_, isStr := native.(string)
				if (!isStr && !isList) || (isList && len(parts) != len(nativeList)) {
					common = setField(common, "truncated", true)
					stream.Missing("tool_output")
				}
			}
			if _, err := stream.Add("tool_result", nextSource(), ts, base, append(common, fields...)...); err != nil {
				return err
			}
			usedMessageID = true
		case kind == "thinking" || kind == "redacted_thinking":
			// Signatures and encrypted provider state must never reach metadata.
			visibility := "full"
			if kind == "redacted_thinking" {
				visibility = "encrypted"
			}
			reason := pyjson.Obj("type", "reasoning", "visibility", visibility)
			if thinking, ok := part.Value("thinking").(string); ok && kind == "thinking" {
				reason.Set("text", thinking)
			}
			fields := []asr.F{{K: "role", V: role}, {K: "content", V: []any{reason}}}
			if inference != "" {
				fields = append(fields, asr.F{K: "inferenceId", V: inference})
			}
			if _, err := stream.Add("message", nextSource(), ts, nil, fields...); err != nil {
				return err
			}
			usedMessageID = true
		default:
			if _, err := stream.Gap("unknown", "An unsupported content block is retained as metadata.", nextSource(), ts, base,
				common...); err != nil {
				return err
			}
			usedMessageID = true
		}
	}
	return nil
}

func metaValue(meta *pyjson.Object) any {
	if meta == nil {
		return nil
	}
	return meta
}

// setField sets a field in place if present, or appends it, as dict assignment does.
func setField(fields []asr.F, key string, value any) []asr.F {
	for i := range fields {
		if fields[i].K == key {
			fields[i].V = value
			return fields
		}
	}
	return append(fields, asr.F{K: key, V: value})
}
