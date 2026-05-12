package tui

import (
	"encoding/json"
	"strings"
)

// parseSnapshot turns the SDK-native JSONL conversation into our transcript
// items. The wire shape mirrors what the SPA's normalizeConversation handles:
//
//	{type:"user",      message:{role,content:string|parts}, uuid, ...}
//	{type:"assistant", message:{role,content:[parts]}, uuid, ...}
//
// Tool calls live inside assistant parts (type:"tool_use") and their results
// inside a follow-up user message (type:"tool_result"). We pair them by
// tool_use_id.
func parseSnapshot(raw json.RawMessage) []item {
	if len(raw) == 0 {
		return nil
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil
	}
	out := make([]item, 0, len(records))
	toolNames := map[string]string{}
	toolItems := map[string]*toolItem{}
	for _, rec := range records {
		recType, _ := rec["type"].(string)
		if recType != "user" && recType != "assistant" {
			continue
		}
		msg, _ := rec["message"].(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = recType
		}
		content := msg["content"]

		if s, ok := content.(string); ok {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if role == "user" {
				out = append(out, &userItem{content: s})
			} else {
				out = append(out, &assistantItem{content: s, final: true})
			}
			continue
		}
		parts, _ := content.([]any)
		if parts == nil {
			continue
		}

		if role == "assistant" {
			textBuf := ""
			flushText := func() {
				if textBuf == "" {
					return
				}
				out = append(out, &assistantItem{content: textBuf, final: true})
				textBuf = ""
			}
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				if pm == nil {
					continue
				}
				t, _ := pm["type"].(string)
				switch t {
				case "text":
					if txt, _ := pm["text"].(string); txt != "" {
						if textBuf != "" {
							textBuf += "\n"
						}
						textBuf += txt
					}
				case "tool_use":
					flushText()
					id, _ := pm["id"].(string)
					name, _ := pm["name"].(string)
					if id != "" {
						toolNames[id] = name
					}
					var inputJSON json.RawMessage
					if v, ok := pm["input"]; ok {
						inputJSON, _ = json.Marshal(v)
					}
					it := &toolItem{
						useID: id,
						tool:  name,
						input: inputJSON,
					}
					if id != "" {
						toolItems[id] = it
					}
					out = append(out, it)
				}
			}
			flushText()
			continue
		}

		// role == "user"
		textBuf := ""
		flushText := func() {
			if textBuf == "" {
				return
			}
			out = append(out, &userItem{content: textBuf})
			textBuf = ""
		}
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			t, _ := pm["type"].(string)
			switch t {
			case "text":
				if txt, _ := pm["text"].(string); txt != "" {
					if textBuf != "" {
						textBuf += "\n"
					}
					textBuf += txt
				}
			case "tool_result":
				flushText()
				useID, _ := pm["tool_use_id"].(string)
				isErr := false
				if b, ok := pm["is_error"].(bool); ok {
					isErr = b
				}
				var outJSON json.RawMessage
				if v, ok := pm["content"]; ok {
					outJSON, _ = json.Marshal(v)
				}
				if it, ok := toolItems[useID]; ok {
					it.output = outJSON
					it.isError = isErr
					it.done = true
				} else {
					name := toolNames[useID]
					out = append(out, &toolItem{
						useID:   useID,
						tool:    name,
						output:  outJSON,
						isError: isErr,
						done:    true,
					})
				}
			}
		}
		flushText()
	}
	return out
}
