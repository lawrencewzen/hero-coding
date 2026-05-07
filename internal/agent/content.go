package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// extractTextContent flattens a ChatMessage.Content (which may be a string,
// a typed []ContentPart, or a JSON-decoded []any of part objects) into plain
// text. Non-text parts (images, tool blocks) are dropped. Used by the agent
// loop and by streaming/non-streaming chat to expose the assistant's reply
// to onDelta callbacks.
func extractTextContent(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []ContentPart:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			if part.Type == "text" || part.Type == "" {
				if text := strings.TrimSpace(part.Text); text != "" {
					parts = append(parts, part.Text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if anySlice := toAnySlice(v); anySlice != nil {
			return extractTextFromAnySlice(anySlice)
		}
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

func extractTextFromAnySlice(parts []any) string {
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		partMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		text, _ := partMap["text"].(string)
		if (partType == "text" || partType == "") && strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n")
}

func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		out := make([]any, len(s))
		for i, m := range s {
			out[i] = m
		}
		return out
	default:
		return nil
	}
}
