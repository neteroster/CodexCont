package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type CodexSummary struct {
	RequestID  string         `json:"request_id"`
	KeyID      string         `json:"key_id,omitempty"`
	Model      string         `json:"model,omitempty"`
	Protection string         `json:"protection,omitempty"`
	Summary    map[string]any `json:"summary"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfig().StateDBPath
	}
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`pragma journal_mode=wal`,
		`create table if not exists codexcont_summaries (
			request_id text primary key,
			key_id text,
			model text,
			protection text,
			summary_json text,
			updated_at integer not null
		)`,
		`create table if not exists audit_log (
			id integer primary key autoincrement,
			timestamp integer not null,
			actor text,
			action text not null,
			target text,
			detail_json text
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveCodexSummary(ctx context.Context, requestID, keyID, model, protection string, summary any) error {
	if s == nil || s.db == nil {
		return nil
	}
	raw, _ := json.Marshal(summary)
	_, err := s.db.ExecContext(ctx, `insert into codexcont_summaries(request_id, key_id, model, protection, summary_json, updated_at)
		values(?, ?, ?, ?, ?, ?)
		on conflict(request_id) do update set key_id=excluded.key_id, model=excluded.model,
		protection=excluded.protection, summary_json=excluded.summary_json, updated_at=excluded.updated_at`,
		requestID, keyID, model, protection, string(raw), time.Now().Unix())
	return err
}

func (s *Store) RecentCodexSummaries(ctx context.Context, keyID string, limit int) ([]CodexSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, model, protection, summary_json, updated_at from codexcont_summaries`
	args := []any{}
	if strings.TrimSpace(keyID) != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by updated_at desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CodexSummary
	for rows.Next() {
		var item CodexSummary
		var raw string
		var ts int64
		if err := rows.Scan(&item.RequestID, &item.KeyID, &item.Model, &item.Protection, &raw, &ts); err != nil {
			return nil, err
		}
		item.UpdatedAt = time.Unix(ts, 0)
		_ = json.Unmarshal([]byte(raw), &item.Summary)
		if item.Summary == nil {
			item.Summary = map[string]any{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
