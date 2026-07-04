package policyplus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const DefaultSessionIdle = 30 * time.Minute

type SessionIdentity struct {
	Source string `json:"source"`
	Value  string `json:"-"`
	Hash   string `json:"hash"`
}

type SessionDecision struct {
	Allowed bool `json:"allowed"`
	Active  int  `json:"active"`
	Limit   int  `json:"limit"`
	Missing bool `json:"missing"`
}

func ExtractSessionIdentity(headers http.Header, body []byte) SessionIdentity {
	if headers != nil {
		if value := strings.TrimSpace(headers.Get("X-Codex-Window-Id")); value != "" {
			return newSessionIdentity("x-codex-window-id", value)
		}
	}
	if value := nestedString(body, "client_metadata", "x-codex-window-id"); value != "" {
		return newSessionIdentity("client_metadata.x-codex-window-id", value)
	}
	if headers != nil {
		if raw := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); raw != "" {
			if value := stringFromJSON([]byte(raw), "window_id"); value != "" {
				return newSessionIdentity("x-codex-turn-metadata.window_id", value)
			}
			if value := stringFromJSON([]byte(raw), "prompt_cache_key"); value != "" {
				return newSessionIdentity("x-codex-turn-metadata.prompt_cache_key", value)
			}
		}
	}
	if raw := nestedString(body, "client_metadata", "x-codex-turn-metadata"); raw != "" {
		if value := stringFromJSON([]byte(raw), "window_id"); value != "" {
			return newSessionIdentity("client_metadata.x-codex-turn-metadata.window_id", value)
		}
		if value := stringFromJSON([]byte(raw), "prompt_cache_key"); value != "" {
			return newSessionIdentity("client_metadata.x-codex-turn-metadata.prompt_cache_key", value)
		}
	}
	if value := nestedString(body, "prompt_cache_key"); value != "" {
		return newSessionIdentity("prompt_cache_key", value)
	}
	if headers != nil {
		for _, name := range []string{"Session_id", "session_id", "Session-Id", "X-Session-ID"} {
			if value := strings.TrimSpace(headers.Get(name)); value != "" {
				return newSessionIdentity(strings.ToLower(name), value)
			}
		}
	}
	if value := nestedString(body, "conversation_id"); value != "" {
		return newSessionIdentity("conversation_id", value)
	}
	return SessionIdentity{}
}

func newSessionIdentity(source, value string) SessionIdentity {
	value = strings.TrimSpace(value)
	if value == "" {
		return SessionIdentity{}
	}
	sum := sha256.Sum256([]byte(source + "\x00" + value))
	return SessionIdentity{
		Source: source,
		Value:  value,
		Hash:   "sha256:" + hex.EncodeToString(sum[:]),
	}
}

func nestedString(body []byte, path ...string) string {
	if len(body) == 0 {
		return ""
	}
	var cur any
	if err := json.Unmarshal(body, &cur); err != nil {
		return ""
	}
	for _, part := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	if text, ok := cur.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func stringFromJSON(body []byte, key string) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	if text, ok := raw[key].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func (s *Store) RegisterActiveSession(ctx context.Context, keyID string, identity SessionIdentity, limit int, idle time.Duration, now time.Time) (SessionDecision, error) {
	if limit <= 0 {
		return SessionDecision{Allowed: true, Limit: limit, Missing: identity.Hash == ""}, nil
	}
	if identity.Hash == "" {
		_ = s.Audit(ctx, "frontend_auth", "missing_session_identity", keyID, map[string]any{"limit": limit})
		active, _ := s.ActiveSessionCount(ctx, keyID, idle, now)
		return SessionDecision{Allowed: true, Active: active, Limit: limit, Missing: true}, nil
	}
	if idle <= 0 {
		idle = DefaultSessionIdle
	}
	cutoff := now.Add(-idle).Unix()
	_, _ = s.db.ExecContext(ctx, `delete from active_sessions where key_id=? and last_seen < ?`, keyID, cutoff)
	var existing int
	if err := s.db.QueryRowContext(ctx, `select count(1) from active_sessions where key_id=? and session_id=?`, keyID, identity.Hash).Scan(&existing); err != nil {
		return SessionDecision{}, err
	}
	if existing > 0 {
		_, err := s.db.ExecContext(ctx, `update active_sessions set last_seen=?, source=? where key_id=? and session_id=?`, now.Unix(), identity.Source, keyID, identity.Hash)
		active, _ := s.ActiveSessionCount(ctx, keyID, idle, now)
		return SessionDecision{Allowed: err == nil, Active: active, Limit: limit}, err
	}
	active, err := s.ActiveSessionCount(ctx, keyID, idle, now)
	if err != nil {
		return SessionDecision{}, err
	}
	if active >= limit {
		_ = s.Audit(ctx, "frontend_auth", "session_limit_exceeded", keyID, map[string]any{"active": active, "limit": limit, "source": identity.Source})
		return SessionDecision{Allowed: false, Active: active, Limit: limit}, nil
	}
	_, err = s.db.ExecContext(ctx, `insert into active_sessions(key_id, session_id, source, first_seen, last_seen) values(?, ?, ?, ?, ?)`, keyID, identity.Hash, identity.Source, now.Unix(), now.Unix())
	if err != nil {
		return SessionDecision{}, err
	}
	return SessionDecision{Allowed: true, Active: active + 1, Limit: limit}, nil
}

func (s *Store) ActiveSessionCount(ctx context.Context, keyID string, idle time.Duration, now time.Time) (int, error) {
	if idle <= 0 {
		idle = DefaultSessionIdle
	}
	cutoff := now.Add(-idle).Unix()
	var count int
	err := s.db.QueryRowContext(ctx, `select count(1) from active_sessions where key_id=? and last_seen >= ?`, keyID, cutoff).Scan(&count)
	return count, err
}
