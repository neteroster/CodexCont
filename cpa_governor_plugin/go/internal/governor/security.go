package governor

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
		return SubmittedKeyHint{Error: "missing_api_key", Message: "请粘贴完整的 cpa_ 开头用户 Key。"}
	case strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_"):
		return SubmittedKeyHint{Error: "native_cpa_key_not_supported", Message: "这是 CPA 原生 sk Key，不能登录用量自助页。请使用 Key Policy 创建时弹窗里的完整 cpa_ 用户 Key。"}
	case strings.Contains(key, "...") || strings.Contains(key, "…"):
		return SubmittedKeyHint{Error: "key_preview_not_usable", Message: "你粘贴的是缩略预览，不是完整 Key。Key Policy 创建或轮换时弹窗里的完整 cpa_ Key 才能登录。"}
	case !strings.HasPrefix(lower, "cpa_"):
		return SubmittedKeyHint{Error: "unsupported_key_format", Message: "用量自助页只接受 Key Policy 的完整 cpa_ 用户 Key。"}
	case len(key) < 40:
		return SubmittedKeyHint{Error: "key_preview_not_usable", Message: "这个 cpa_ Key 太短，像是列表里的预览，不是完整 Key。请在 Key Policy 里点击“轮换”，复制弹窗中新生成的完整 Key。"}
	default:
		return SubmittedKeyHint{Error: "invalid_api_key", Message: "这个 cpa_ Key 没有匹配到当前 Key Policy 记录。请确认粘贴的是创建或轮换弹窗里的完整 Key；列表里的 cpa_xxx...xxx 只是预览，旧 Key 关闭弹窗后无法找回，需要在 Key Policy 里轮换生成新的完整 Key。"}
	}
}

func NormalizeHash(value string) (string, error) {
	text := strings.TrimSpace(value)
	text = strings.TrimPrefix(text, "sha256:")
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
