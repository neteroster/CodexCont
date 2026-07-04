package policyplus

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(NormalizeSubmittedKey(value)))
	return hex.EncodeToString(sum[:])
}

var bearerPrefixPattern = regexp.MustCompile(`(?i)^\s*(authorization\s*:\s*)?(bearer\s+)+`)

// NormalizeSubmittedKey accepts the common clipboard shapes users paste into
// the self-service portal, while keeping hashing deterministic.
func NormalizeSubmittedKey(value string) string {
	text := strings.TrimSpace(value)
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(strings.Trim(text, `"'`+"`"+`“”‘’「」『』<>`))
	text = bearerPrefixPattern.ReplaceAllString(text, "")
	return strings.TrimSpace(strings.Trim(text, `"'`+"`"+`“”‘’「」『』<>`))
}

type SubmittedKeyHint struct {
	Error   string
	Message string
}

func ExplainUnmatchedSubmittedKey(value string) SubmittedKeyHint {
	key := NormalizeSubmittedKey(value)
	lower := strings.ToLower(key)
	switch {
	case key == "":
		return SubmittedKeyHint{Error: "missing_api_key", Message: "请粘贴完整的 CPA 原生 sk- Key。"}
	case strings.Contains(key, "...") || strings.Contains(key, "…"):
		return SubmittedKeyHint{Error: "key_preview_not_usable", Message: "你粘贴的是缩略预览，不是完整 Key。请使用 CPAMP 中完整的 CPA 原生 sk- Key。"}
	case strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_"):
		return SubmittedKeyHint{Error: "invalid_api_key", Message: "这个 sk- Key 没有匹配到已同步并启用的 CPA Key Policy+ 策略。请先在 CPAMP 确认原生 Key 存在，并在 Plus 管理页启用对应策略。"}
	case strings.HasPrefix(lower, "cpa_"):
		return SubmittedKeyHint{Error: "legacy_cpa_key_retired", Message: "旧的 cpa_ Key 已迁移下线。现在请使用 CPA/CPAMP 管理的原生 sk- Key 登录用量页。"}
	case !strings.HasPrefix(lower, "sk-") && !strings.HasPrefix(lower, "sk_"):
		return SubmittedKeyHint{Error: "unsupported_key_format", Message: "用量自助页现在只接受 CPA 原生 sk- Key。"}
	default:
		return SubmittedKeyHint{Error: "invalid_api_key", Message: "这个 Key 没有匹配到当前 CPA Key Policy+ 策略。请确认原生 Key 仍存在并已同步。"}
	}
}

func NormalizeHash(value string) (string, error) {
	text := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(text), "sha256:") {
		text = text[len("sha256:"):]
	}
	if len(text) != 64 {
		return "", errors.New("invalid hash length")
	}
	_, err := hex.DecodeString(text)
	if err != nil {
		return "", err
	}
	return strings.ToLower(text), nil
}

func HashPreview(value string) string {
	normalized, err := NormalizeHash(value)
	if err != nil {
		normalized = SHA256Hex(value)
	}
	if len(normalized) <= 16 {
		return normalized
	}
	return normalized[:8] + "..." + normalized[len(normalized)-6:]
}

type SessionPayload struct {
	KeyID     string `json:"key_id"`
	KeyHash   string `json:"key_hash"`
	ExpiresAt int64  `json:"expires_at"`
}

func SignSession(payload SessionPayload, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("session secret is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

func VerifySession(token, secret string, now time.Time) (SessionPayload, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || strings.TrimSpace(secret) == "" {
		return SessionPayload{}, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return SessionPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return SessionPayload{}, false
	}
	var payload SessionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return SessionPayload{}, false
	}
	if payload.ExpiresAt > 0 && now.Unix() > payload.ExpiresAt {
		return SessionPayload{}, false
	}
	return payload, true
}
