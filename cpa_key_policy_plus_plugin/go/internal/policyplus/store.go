package policyplus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type UsageEvent struct {
	RequestID       string        `json:"request_id"`
	KeyID           string        `json:"key_id"`
	KeyPreview      string        `json:"key_preview"`
	Model           string        `json:"model"`
	RequestedModel  string        `json:"requested_model,omitempty"`
	ActualModel     string        `json:"actual_model,omitempty"`
	Provider        string        `json:"provider,omitempty"`
	ExecutorType    string        `json:"executor_type,omitempty"`
	Endpoint        string        `json:"endpoint,omitempty"`
	RequestedAt     time.Time     `json:"requested_at"`
	LatencyMS       int64         `json:"latency_ms"`
	TTFTMS          int64         `json:"ttft_ms,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	ServiceTier     string        `json:"service_tier,omitempty"`
	StatusCode      int           `json:"status_code,omitempty"`
	Failed          bool          `json:"failed"`
	Failure         string        `json:"failure,omitempty"`
	Usage           TokenUsage    `json:"usage"`
	Cost            float64       `json:"cost"`
	CostBreakdown   CostBreakdown `json:"cost_breakdown"`
}

type UsageSummary struct {
	Calls     int64      `json:"calls"`
	Failed    int64      `json:"failed"`
	TotalCost float64    `json:"total_cost"`
	Usage     TokenUsage `json:"usage"`
}

type CodexSummary struct {
	RequestID  string         `json:"request_id"`
	KeyID      string         `json:"key_id,omitempty"`
	Model      string         `json:"model,omitempty"`
	Protection string         `json:"protection,omitempty"`
	Summary    map[string]any `json:"summary"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type LegacyImportResult struct {
	Limits int
	Resets int
}

const sqliteBusyTimeoutMS = 5000

func OpenStore(path string) (*Store, error) {
	if path == "" {
		path = "cpa-policyplus.sqlite"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	db, err := openSQLite(path, false)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	dsn := sqliteDSN(path, readOnly)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if readOnly {
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(1)
	}
	return db, nil
}

func sqliteDSN(path string, readOnly bool) string {
	path = strings.TrimSpace(path)
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
	}
	if strings.HasPrefix(path, "file:") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + query.Encode()
	}
	if readOnly {
		return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
	}
	return path + "?" + query.Encode()
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
		`create table if not exists keys (
			id text primary key,
			name text not null,
			key_hash text not null unique,
			enabled integer not null,
			preview text,
			rpm integer,
			concurrency integer,
			max_active_sessions integer,
			models_json text,
			prices_json text,
			five_hour_limit_usd real,
			daily_limit_usd real,
			weekly_limit_usd real,
			monthly_limit_usd real,
			archived integer not null default 0,
			archived_at integer not null default 0,
			source text default '',
			source_present integer not null default 1,
			alias text default '',
			inherited_from text default '',
			inherit_conflict integer not null default 0,
			hidden integer not null default 0,
			last_enabled integer not null default 0,
			updated_at integer not null
		)`,
		`create table if not exists reset_watermarks (
			key_id text not null,
			window text not null,
			reset_at integer not null,
			primary key(key_id, window)
		)`,
		`create table if not exists active_sessions (
			key_id text not null,
			session_id text not null,
			source text,
			first_seen integer not null,
			last_seen integer not null,
			primary key(key_id, session_id)
		)`,
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			key_id text,
			key_preview text,
			model text,
			requested_model text,
			actual_model text,
			provider text,
			executor_type text,
			endpoint text,
			requested_at integer not null,
			latency_ms integer,
			ttft_ms integer,
			reasoning_effort text,
			service_tier text,
			status_code integer,
			failed integer not null,
			failure text,
			input_tokens integer,
			output_tokens integer,
			cached_tokens integer,
			cache_read_tokens integer,
			cache_creation_tokens integer,
			reasoning_tokens integer,
			total_tokens integer,
			cost real,
			cost_breakdown_json text
		)`,
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
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at integer not null
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumns(ctx, "usage_events", map[string]string{
		"requested_model":  "text default ''",
		"actual_model":     "text default ''",
		"provider":         "text default ''",
		"executor_type":    "text default ''",
		"ttft_ms":          "integer default 0",
		"reasoning_effort": "text default ''",
		"service_tier":     "text default ''",
		"status_code":      "integer default 0",
	}); err != nil {
		return err
	}
	if err := s.ensureColumns(ctx, "keys", map[string]string{
		"max_active_sessions": "integer default 0",
		"archived":            "integer not null default 0",
		"archived_at":         "integer not null default 0",
		"source":              "text default ''",
		"source_present":      "integer not null default 1",
		"alias":               "text default ''",
		"inherited_from":      "text default ''",
		"inherit_conflict":    "integer not null default 0",
		"hidden":              "integer not null default 0",
		"last_enabled":        "integer not null default 0",
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumns(ctx context.Context, table string, columns map[string]string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`pragma table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, typ := range columns {
		if existing[name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`alter table %s add column %s %s`, table, name, typ)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertKey(ctx context.Context, key KeyRecord) error {
	if err := ValidateKeyRecord(key); err != nil {
		return err
	}
	if key.Source == "" {
		key.SourcePresent = true
	}
	models, _ := json.Marshal(key.Models)
	prices, _ := json.Marshal(key.Prices)
	_, err := s.db.ExecContext(
		ctx,
		`insert into keys(
			id, name, key_hash, enabled, preview, rpm, concurrency, max_active_sessions, models_json, prices_json,
			five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd,
			archived, archived_at, source, source_present, alias, inherited_from, inherit_conflict, hidden, last_enabled, updated_at
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			name=excluded.name,
			key_hash=excluded.key_hash,
			enabled=excluded.enabled,
			preview=excluded.preview,
			rpm=excluded.rpm,
			concurrency=excluded.concurrency,
			max_active_sessions=excluded.max_active_sessions,
			models_json=excluded.models_json,
			prices_json=excluded.prices_json,
			five_hour_limit_usd=coalesce(keys.five_hour_limit_usd, excluded.five_hour_limit_usd),
			daily_limit_usd=excluded.daily_limit_usd,
			weekly_limit_usd=excluded.weekly_limit_usd,
			monthly_limit_usd=coalesce(keys.monthly_limit_usd, excluded.monthly_limit_usd),
			archived=case when excluded.archived != 0 then excluded.archived else keys.archived end,
			archived_at=case when excluded.archived != 0 then excluded.archived_at else keys.archived_at end,
			source=case when excluded.source != '' then excluded.source else keys.source end,
			source_present=excluded.source_present,
			alias=excluded.alias,
			inherited_from=case when excluded.inherited_from != '' then excluded.inherited_from else keys.inherited_from end,
			inherit_conflict=excluded.inherit_conflict,
			hidden=excluded.hidden,
			last_enabled=case when excluded.last_enabled != 0 then excluded.last_enabled else keys.last_enabled end,
			updated_at=excluded.updated_at`,
		key.ID,
		key.Name,
		key.KeyHash,
		boolInt(key.Enabled),
		key.Preview,
		key.RPM,
		key.Concurrency,
		key.MaxActiveSessions,
		string(models),
		string(prices),
		key.FiveHourUSD,
		key.DailyLimitUSD,
		key.WeeklyLimitUSD,
		key.MonthlyLimitUSD,
		boolInt(key.Archived),
		key.ArchivedAt,
		key.Source,
		boolInt(key.SourcePresent),
		key.Alias,
		key.InheritedFrom,
		boolInt(key.InheritConflict),
		boolInt(key.Hidden),
		boolInt(key.LastEnabled),
		time.Now().Unix(),
	)
	return err
}

func (s *Store) ImportKeys(ctx context.Context, state KeyPolicyState) error {
	for _, key := range state.Keys {
		if err := s.UpsertKey(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ImportLegacyQuotaSQLite(ctx context.Context, path, source string) (LegacyImportResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return LegacyImportResult{}, nil
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return LegacyImportResult{}, err
	}
	defer db.Close()
	result := LegacyImportResult{}
	if ok, _ := legacyTableExists(ctx, db, "key_limits"); ok {
		n, err := s.importLegacyPortalLimits(ctx, db)
		if err != nil {
			return result, err
		}
		result.Limits += n
	}
	if ok, _ := legacyTableExists(ctx, db, "keys"); ok {
		n, err := s.importLegacyGovernorLimits(ctx, db)
		if err != nil {
			return result, err
		}
		result.Limits += n
	}
	if ok, _ := legacyTableExists(ctx, db, "reset_watermarks"); ok {
		n, err := s.importLegacyResets(ctx, db)
		if err != nil {
			return result, err
		}
		result.Resets += n
	}
	if result.Limits > 0 || result.Resets > 0 {
		_ = s.Audit(ctx, "system", "import_legacy_quota_sqlite", source, map[string]any{
			"path":   path,
			"limits": result.Limits,
			"resets": result.Resets,
		})
	}
	return result, nil
}

func legacyTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(1) from sqlite_master where type='table' and name=?`, name).Scan(&count)
	return count > 0, err
}

func legacyColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`pragma table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) importLegacyPortalLimits(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select policy_id, five_hour_limit_usd, monthly_limit_usd from key_limits`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var fiveHour, monthly sql.NullFloat64
		if err := rows.Scan(&id, &fiveHour, &monthly); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `update keys set
			five_hour_limit_usd=coalesce(five_hour_limit_usd, ?),
			monthly_limit_usd=coalesce(monthly_limit_usd, ?),
			updated_at=?
			where id=?`,
			nullFloatValue(fiveHour), nullFloatValue(monthly), time.Now().Unix(), id)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) importLegacyGovernorLimits(ctx context.Context, db *sql.DB) (int, error) {
	columns, err := legacyColumns(ctx, db, "keys")
	if err != nil {
		return 0, err
	}
	for _, required := range []string{"id", "five_hour_limit_usd", "daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd"} {
		if !columns[required] {
			return 0, nil
		}
	}
	rows, err := db.QueryContext(ctx, `select id, five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd from keys`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var fiveHour, daily, weekly, monthly sql.NullFloat64
		if err := rows.Scan(&id, &fiveHour, &daily, &weekly, &monthly); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `update keys set
			five_hour_limit_usd=coalesce(five_hour_limit_usd, ?),
			daily_limit_usd=coalesce(daily_limit_usd, ?),
			weekly_limit_usd=coalesce(weekly_limit_usd, ?),
			monthly_limit_usd=coalesce(monthly_limit_usd, ?),
			updated_at=?
			where id=?`,
			nullFloatValue(fiveHour), nullFloatValue(daily), nullFloatValue(weekly),
			nullFloatValue(monthly), time.Now().Unix(), id)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) importLegacyResets(ctx context.Context, db *sql.DB) (int, error) {
	columns, err := legacyColumns(ctx, db, "reset_watermarks")
	if err != nil {
		return 0, err
	}
	idColumn := ""
	for _, candidate := range []string{"key_id", "policy_id"} {
		if columns[candidate] {
			idColumn = candidate
			break
		}
	}
	timeColumn := ""
	for _, candidate := range []string{"reset_at", "reset_at_ms"} {
		if columns[candidate] {
			timeColumn = candidate
			break
		}
	}
	if idColumn == "" || timeColumn == "" || !columns["window"] {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`select %s, window, %s from reset_watermarks`, idColumn, timeColumn))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, window string
		var resetAt sql.NullInt64
		if err := rows.Scan(&id, &window, &resetAt); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" || strings.TrimSpace(window) == "" || !resetAt.Valid {
			continue
		}
		ts := resetAt.Int64
		if ts > 1_000_000_000_000 {
			ts = ts / 1000
		}
		res, err := s.db.ExecContext(ctx, `insert into reset_watermarks(key_id, window, reset_at) values(?, ?, ?)
			on conflict(key_id, window) do update set reset_at=excluded.reset_at
			where excluded.reset_at > reset_watermarks.reset_at`, id, window, ts)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) syncDeletedKeys(ctx context.Context, seen map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `select id from keys`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := s.db.ExecContext(ctx, `delete from keys where id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListKeys(ctx context.Context) ([]KeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select id, name, key_hash, enabled, preview, rpm, concurrency, max_active_sessions, models_json, prices_json,
		five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, archived, archived_at,
			coalesce(source, ''), coalesce(source_present, 1), coalesce(alias, ''), coalesce(inherited_from, ''),
		coalesce(inherit_conflict, 0), coalesce(hidden, 0), coalesce(last_enabled, enabled)
		from keys order by hidden asc, name collate nocase`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyRecord
	for rows.Next() {
		var key KeyRecord
		var enabled, archived, sourcePresent, inheritConflict, hidden, lastEnabled int
		var modelsJSON, pricesJSON string
		var fiveHour, daily, weekly, monthly sql.NullFloat64
		if err := rows.Scan(
			&key.ID, &key.Name, &key.KeyHash, &enabled, &key.Preview, &key.RPM, &key.Concurrency, &key.MaxActiveSessions,
			&modelsJSON, &pricesJSON, &fiveHour, &daily, &weekly, &monthly,
			&archived, &key.ArchivedAt, &key.Source, &sourcePresent, &key.Alias, &key.InheritedFrom,
			&inheritConflict, &hidden, &lastEnabled,
		); err != nil {
			return nil, err
		}
		key.Enabled = enabled != 0
		key.Archived = archived != 0
		key.SourcePresent = sourcePresent != 0
		key.InheritConflict = inheritConflict != 0
		key.Hidden = hidden != 0
		key.LastEnabled = lastEnabled != 0
		key.FiveHourUSD = nullFloatPtr(fiveHour)
		key.DailyLimitUSD = nullFloatPtr(daily)
		key.WeeklyLimitUSD = nullFloatPtr(weekly)
		key.MonthlyLimitUSD = nullFloatPtr(monthly)
		_ = json.Unmarshal([]byte(modelsJSON), &key.Models)
		_ = json.Unmarshal([]byte(pricesJSON), &key.Prices)
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Store) SetArchived(ctx context.Context, id string, archived bool, at time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("missing key id")
	}
	archivedAt := int64(0)
	if archived {
		archivedAt = at.Unix()
	}
	res, err := s.db.ExecContext(ctx, `update keys set archived=?, archived_at=?, updated_at=? where id=?`,
		boolInt(archived), archivedAt, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	action := "archive_key"
	if !archived {
		action = "restore_key"
	}
	return s.Audit(ctx, "admin", action, id, map[string]any{"archived": archived, "archived_at": archivedAt})
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing key id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, preview string
	if err := tx.QueryRowContext(ctx, `select name, preview from keys where id=?`, id).Scan(&name, &preview); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("unknown key: %s", id)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from reset_watermarks where key_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from active_sessions where key_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from keys where id=?`, id); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "preview": preview, "kept_usage_history": true})
	if _, err := tx.ExecContext(ctx, `insert into audit_log(timestamp, actor, action, target, detail_json) values(?, ?, ?, ?, ?)`,
		time.Now().Unix(), "admin", "delete_key", id, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindKeyByHash(ctx context.Context, hash string) (KeyRecord, bool, error) {
	keys, err := s.ListKeys(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	normalized, err := NormalizeHash(hash)
	if err != nil {
		return KeyRecord{}, false, nil
	}
	for _, key := range keys {
		keyHash, err := NormalizeHash(key.KeyHash)
		if err == nil && keyHash == normalized {
			return key, true, nil
		}
	}
	return KeyRecord{}, false, nil
}

func (s *Store) SetLimits(ctx context.Context, id string, fiveHour, monthly *float64) error {
	for _, limit := range []*float64{fiveHour, monthly} {
		if limit != nil && *limit < 0 {
			return fmt.Errorf("usd limits must not be negative")
		}
	}
	res, err := s.db.ExecContext(ctx, `update keys set five_hour_limit_usd=?, monthly_limit_usd=?, updated_at=? where id=?`, fiveHour, monthly, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	return s.Audit(ctx, "admin", "set_limits", id, map[string]any{"five_hour_usd": fiveHour, "monthly_usd": monthly})
}

func (s *Store) SaveKeySettings(ctx context.Context, key KeyRecord) error {
	if err := ValidateKeyRecord(key); err != nil {
		return err
	}
	models, _ := json.Marshal(key.Models)
	prices, _ := json.Marshal(key.Prices)
	res, err := s.db.ExecContext(ctx, `update keys set
		enabled=?,
		rpm=?,
		concurrency=?,
		max_active_sessions=?,
		models_json=?,
		prices_json=?,
		five_hour_limit_usd=?,
		daily_limit_usd=?,
		weekly_limit_usd=?,
		monthly_limit_usd=?,
		last_enabled=?,
		updated_at=?
		where id=?`,
		boolInt(key.Enabled),
		key.RPM,
		key.Concurrency,
		key.MaxActiveSessions,
		string(models),
		string(prices),
		key.FiveHourUSD,
		key.DailyLimitUSD,
		key.WeeklyLimitUSD,
		key.MonthlyLimitUSD,
		boolInt(key.Enabled),
		time.Now().Unix(),
		key.ID,
	)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", key.ID)
	}
	return s.Audit(ctx, "admin", "save_key_settings", key.ID, map[string]any{
		"enabled":             key.Enabled,
		"rpm":                 key.RPM,
		"concurrency":         key.Concurrency,
		"max_active_sessions": key.MaxActiveSessions,
	})
}

func (s *Store) SyncNativeKeys(ctx context.Context, inputs []NativeKeySyncInput) error {
	existing, err := s.ListKeys(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]KeyRecord, len(existing))
	for _, key := range existing {
		byID[key.ID] = key
	}
	seen := map[string]bool{}
	now := time.Now().Unix()
	for _, input := range inputs {
		next, ok := NativeKeyRecord(input.RawKey, input.Alias)
		if !ok {
			continue
		}
		seen[next.ID] = true
		if current, exists := byID[next.ID]; exists {
			enabled := current.Enabled
			if isDefaultEmptyNativePolicy(current) {
				enabled = true
			}
			current.KeyHash = next.KeyHash
			current.Preview = next.Preview
			current.Source = NativeCPASource
			current.SourcePresent = true
			current.Hidden = false
			current.Alias = next.Alias
			current.Name = next.Name
			if _, err := s.db.ExecContext(ctx, `update keys set
				name=?, key_hash=?, preview=?, enabled=?, source=?, source_present=1, hidden=0, alias=?, updated_at=?
				where id=?`,
				current.Name, current.KeyHash, current.Preview, boolInt(enabled), current.Source, current.Alias, now, current.ID); err != nil {
				return err
			}
			continue
		}
		templates := policyTemplatesByAlias(existing, next.Alias)
		if len(templates) == 1 {
			next = CopyPolicyFields(next, templates[0])
			next.InheritedFrom = templates[0].ID
		} else if len(templates) > 1 {
			next.Enabled = false
			next.InheritConflict = true
		}
		if err := s.UpsertKey(ctx, next); err != nil {
			return err
		}
		if next.InheritedFrom != "" {
			_ = s.Audit(ctx, "system", "native_key_policy_inherited", next.ID, map[string]any{"from": next.InheritedFrom, "alias": next.Alias})
		}
	}
	for _, key := range existing {
		if key.Source != NativeCPASource || seen[key.ID] || !key.SourcePresent {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `update keys set enabled=0, source_present=0, hidden=1, last_enabled=?, updated_at=? where id=?`, boolInt(key.Enabled), now, key.ID); err != nil {
			return err
		}
		_ = s.Audit(ctx, "system", "native_key_source_removed", key.ID, map[string]any{"alias": key.Alias, "preview": key.Preview})
	}
	if err := s.retireInheritedLegacyTemplates(ctx, now); err != nil {
		return err
	}
	return nil
}

func isDefaultEmptyNativePolicy(key KeyRecord) bool {
	return key.Source == NativeCPASource &&
		key.SourcePresent &&
		!key.Enabled &&
		!key.LastEnabled &&
		key.RPM == 0 &&
		key.Concurrency == 0 &&
		key.MaxActiveSessions == 0 &&
		len(key.Models) == 0 &&
		len(key.Prices) == 0 &&
		key.FiveHourUSD == nil &&
		key.DailyLimitUSD == nil &&
		key.WeeklyLimitUSD == nil &&
		key.MonthlyLimitUSD == nil &&
		!key.InheritConflict
}

func policyTemplatesByAlias(keys []KeyRecord, alias string) []KeyRecord {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return nil
	}
	var out []KeyRecord
	for _, key := range keys {
		if templateAlias(key) != alias {
			continue
		}
		if key.Source == NativeCPASource && !key.SourcePresent {
			out = append(out, key)
			continue
		}
		if key.Source == "" && key.SourcePresent && !key.Hidden {
			out = append(out, key)
		}
	}
	return out
}

func templateAlias(key KeyRecord) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(key.Alias, key.Name)))
}

func (s *Store) retireInheritedLegacyTemplates(ctx context.Context, now int64) error {
	rows, err := s.db.QueryContext(ctx, `select distinct inherited_from from keys where source=? and inherited_from != ''`, NativeCPASource)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `update keys set enabled=0, source=?, source_present=0, hidden=1, last_enabled=case when last_enabled != 0 then last_enabled else enabled end, updated_at=? where id=? and coalesce(source, '')=''`,
			LegacyPlusSource, now, id); err != nil {
			return err
		}
	}
	return nil
}

func LoadNativeKeysFromCPAConfig(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var body struct {
		APIKeys []string `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.APIKeys))
	for _, key := range body.APIKeys {
		key = NormalizeSubmittedKey(key)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

func LoadAPIKeyAliasesFromSQLite(ctx context.Context, path string) (map[string]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	exists, err := legacyTableExists(ctx, db, "api_key_aliases")
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]string{}, nil
	}
	rows, err := db.QueryContext(ctx, `select api_key_hash, alias from api_key_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var hash, alias string
		if err := rows.Scan(&hash, &alias); err != nil {
			return nil, err
		}
		normalized, err := NormalizeHash(hash)
		if err != nil {
			continue
		}
		if alias = strings.TrimSpace(alias); alias != "" {
			out[normalized] = alias
		}
	}
	return out, rows.Err()
}

func LoadAPIKeyAliasesFromSQLitePaths(ctx context.Context, paths []string) (map[string]string, []string, error) {
	out := map[string]string{}
	var errs []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		aliases, err := LoadAPIKeyAliasesFromSQLite(ctx, path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", path, Brief(err.Error(), 160)))
			continue
		}
		for hash, alias := range aliases {
			if strings.TrimSpace(alias) == "" {
				continue
			}
			out[hash] = alias
		}
	}
	if len(errs) > 0 && len(out) == 0 {
		return out, errs, fmt.Errorf(strings.Join(errs, "; "))
	}
	return out, errs, nil
}

func (s *Store) Reset(ctx context.Context, id, window string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into reset_watermarks(key_id, window, reset_at) values(?, ?, ?)
		on conflict(key_id, window) do update set reset_at=excluded.reset_at`, id, window, at.Unix())
	if err != nil {
		return err
	}
	return s.Audit(ctx, "admin", "reset_usage", id, map[string]any{"window": window, "reset_at": at.Unix()})
}

func (s *Store) InsertUsage(ctx context.Context, event UsageEvent) error {
	if event.RequestedAt.IsZero() {
		event.RequestedAt = time.Now()
	}
	breakdown, _ := json.Marshal(event.CostBreakdown)
	_, err := s.db.ExecContext(
		ctx,
		`insert into usage_events(
			request_id, key_id, key_preview, model, requested_model, actual_model, provider, executor_type,
			endpoint, requested_at, latency_ms, ttft_ms, reasoning_effort, service_tier, status_code, failed, failure,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost, cost_breakdown_json
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.KeyID, event.KeyPreview, event.Model, event.RequestedModel, event.ActualModel,
		event.Provider, event.ExecutorType, event.Endpoint, event.RequestedAt.Unix(),
		event.LatencyMS, event.TTFTMS, event.ReasoningEffort, event.ServiceTier, event.StatusCode,
		boolInt(event.Failed), event.Failure,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CachedTokens, event.Usage.CacheReadTokens,
		event.Usage.CacheCreationTokens, event.Usage.ReasoningTokens, event.Usage.TotalTokens,
		event.Cost, string(breakdown),
	)
	return err
}

func (s *Store) UsageSum(ctx context.Context, keyID string, window Window) (float64, error) {
	var total sql.NullFloat64
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	args := []any{from, window.To.Unix()}
	query := `select coalesce(sum(cost), 0) from usage_events where requested_at >= ? and requested_at <= ?`
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

func (s *Store) ResetAt(ctx context.Context, keyID, window string) (int64, bool) {
	if s == nil || s.db == nil || keyID == "" || window == "" || keyID == "all" {
		return 0, false
	}
	var resetAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select reset_at from reset_watermarks where key_id=? and window=?`, keyID, window).Scan(&resetAt); err != nil {
		return 0, false
	}
	return resetAt.Int64, resetAt.Valid
}

func (s *Store) UsageSummary(ctx context.Context, keyID string, window Window) (UsageSummary, error) {
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	args := []any{from, window.To.Unix()}
	query := `select
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end), 0),
		coalesce(sum(cost), 0),
		coalesce(sum(input_tokens), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(cached_tokens), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(total_tokens), 0)
		from usage_events where requested_at >= ? and requested_at <= ?`
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	var summary UsageSummary
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.Calls,
		&summary.Failed,
		&summary.TotalCost,
		&summary.Usage.InputTokens,
		&summary.Usage.OutputTokens,
		&summary.Usage.CachedTokens,
		&summary.Usage.CacheReadTokens,
		&summary.Usage.CacheCreationTokens,
		&summary.Usage.ReasoningTokens,
		&summary.Usage.TotalTokens,
	); err != nil {
		return UsageSummary{}, err
	}
	return summary, nil
}

func (s *Store) RecentEvents(ctx context.Context, keyID string, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select coalesce(request_id, ''), coalesce(key_id, ''), coalesce(key_preview, ''), coalesce(model, ''),
		coalesce(requested_model, ''), coalesce(actual_model, ''), coalesce(provider, ''), coalesce(executor_type, ''),
		coalesce(endpoint, ''), requested_at, coalesce(latency_ms, 0), coalesce(ttft_ms, 0),
		coalesce(reasoning_effort, ''), coalesce(service_tier, ''), coalesce(status_code, 0), coalesce(failed, 0), coalesce(failure, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0), coalesce(cost, 0), coalesce(cost_breakdown_json, '{}')
		from usage_events`
	args := []any{}
	if keyID != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by requested_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageEvent
	for rows.Next() {
		var event UsageEvent
		var ts int64
		var failed int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.RequestedModel, &event.ActualModel,
			&event.Provider, &event.ExecutorType, &event.Endpoint, &ts, &event.LatencyMS, &event.TTFTMS,
			&event.ReasoningEffort, &event.ServiceTier, &event.StatusCode, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		_ = json.Unmarshal([]byte(breakdown), &event.CostBreakdown)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) RecentEventsWindow(ctx context.Context, keyID string, window Window, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	query := `select coalesce(request_id, ''), coalesce(key_id, ''), coalesce(key_preview, ''), coalesce(model, ''),
		coalesce(requested_model, ''), coalesce(actual_model, ''), coalesce(provider, ''), coalesce(executor_type, ''),
		coalesce(endpoint, ''), requested_at, coalesce(latency_ms, 0), coalesce(ttft_ms, 0),
		coalesce(reasoning_effort, ''), coalesce(service_tier, ''), coalesce(status_code, 0), coalesce(failed, 0), coalesce(failure, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0), coalesce(cost, 0), coalesce(cost_breakdown_json, '{}')
		from usage_events where requested_at >= ? and requested_at <= ?`
	args := []any{from, window.To.Unix()}
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by requested_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageEvent
	for rows.Next() {
		var event UsageEvent
		var ts int64
		var failed int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.RequestedModel, &event.ActualModel,
			&event.Provider, &event.ExecutorType, &event.Endpoint, &ts, &event.LatencyMS, &event.TTFTMS,
			&event.ReasoningEffort, &event.ServiceTier, &event.StatusCode, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		_ = json.Unmarshal([]byte(breakdown), &event.CostBreakdown)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) SaveCodexSummary(ctx context.Context, requestID, keyID, model, protection string, summary any) error {
	raw, _ := json.Marshal(summary)
	_, err := s.db.ExecContext(ctx, `insert into codexcont_summaries(request_id, key_id, model, protection, summary_json, updated_at)
		values(?, ?, ?, ?, ?, ?)
		on conflict(request_id) do update set key_id=excluded.key_id, model=excluded.model,
		protection=excluded.protection, summary_json=excluded.summary_json, updated_at=excluded.updated_at`,
		requestID, keyID, model, protection, string(raw), time.Now().Unix())
	return err
}

func (s *Store) RecentCodexSummaries(ctx context.Context, keyID string, limit int) ([]CodexSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, model, protection, summary_json, updated_at from codexcont_summaries`
	args := []any{}
	if keyID != "" && keyID != "all" {
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

func RecentCodexSummariesFromSQLite(ctx context.Context, path, keyID string, limit int) ([]CodexSummary, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, model, protection, summary_json, updated_at from codexcont_summaries`
	args := []any{}
	if keyID != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by updated_at desc limit ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
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

func (s *Store) Audit(ctx context.Context, actor, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	_, err := s.db.ExecContext(ctx, `insert into audit_log(timestamp, actor, action, target, detail_json) values(?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, target, string(raw))
	return err
}

func (s *Store) AuditCount(ctx context.Context, action string) (int, error) {
	var count int
	args := []any{}
	query := `select count(1) from audit_log`
	if strings.TrimSpace(action) != "" {
		query += ` where action = ?`
		args = append(args, action)
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `select key, value from settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (s *Store) SaveSettings(ctx context.Context, values map[string]string) error {
	for key, value := range values {
		if _, err := s.db.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?, ?, ?)
			on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`,
			key, value, time.Now().Unix()); err != nil {
			return err
		}
	}
	return s.Audit(ctx, "admin", "set_settings", "policyplus", values)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	v := value.Float64
	return &v
}

func nullFloatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}
