package executor

import (
	"encoding/json"
	"math"
)

func IsTruncationPattern(tokens int64, ok bool, step int) bool {
	return ok && tokens >= int64(step-2) && (tokens+2)%int64(step) == 0
}

func TierN(tokens int64, ok bool, step int) (int64, bool) {
	if !IsTruncationPattern(tokens, ok, step) {
		return 0, false
	}
	return (tokens + 2) / int64(step), true
}

func ShouldContinue(tokens int64, ok bool, minN, maxN, step int) bool {
	n, match := TierN(tokens, ok, step)
	if !match {
		return false
	}
	if n < int64(minN) {
		return false
	}
	return maxN <= 0 || n <= int64(maxN)
}

func ReasoningTokens(usage map[string]any) (int64, bool) {
	details := mapValue(usage, "output_tokens_details")
	return intValue(details["reasoning_tokens"])
}

func CommentaryMessage(text string) map[string]any {
	return map[string]any{
		"type":  "message",
		"role":  "assistant",
		"phase": "commentary",
		"content": []any{
			map[string]any{"type": "output_text", "text": text},
		},
	}
}

func BuildRoundPayload(base map[string]any, input []any, cfg Config, dropPreviousResponseID bool) map[string]any {
	out := cloneMap(base)
	out["stream"] = true
	out["input"] = input
	if cfg.ForceIncludeEncrypted || base["include"] != nil {
		out["include"] = mergeInclude(base["include"], cfg.ForceIncludeEncrypted)
	}
	if dropPreviousResponseID {
		delete(out, "previous_response_id")
	}
	return out
}

func BuildFirstPayload(base map[string]any) map[string]any {
	out := cloneMap(base)
	out["stream"] = true
	return out
}

func mergeInclude(raw any, forceEncrypted bool) []any {
	seen := map[string]bool{}
	var out []any
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			text := toString(item)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			out = append(out, text)
		}
	}
	if forceEncrypted && !seen[EncryptedInclude] {
		out = append(out, EncryptedInclude)
	}
	return out
}

func mapValue(raw any, key string) map[string]any {
	m, _ := raw.(map[string]any)
	if key == "" {
		return m
	}
	nested, _ := m[key].(map[string]any)
	return nested
}

func anyList(raw any) []any {
	if raw == nil {
		return nil
	}
	if list, ok := raw.([]any); ok {
		return append([]any(nil), list...)
	}
	return nil
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneEvent(in map[string]any) map[string]any {
	raw, err := json.Marshal(in)
	if err != nil {
		return cloneMap(in)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneMap(in)
	}
	return out
}

func intValue(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if math.Trunc(v) == v {
			return int64(v), true
		}
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

func toString(raw any) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return ""
}
