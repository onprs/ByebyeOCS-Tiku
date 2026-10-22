package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Record struct {
	ID               int64       `json:"id"`
	Timestamp        string      `json:"timestamp"`
	Type             string      `json:"type"`
	Status           string      `json:"status"`
	RawTitle         string      `json:"raw_title"`
	RawOptions       string      `json:"raw_options"`
	ProcessedTitle   string      `json:"processed_title"`
	ProcessedOptions string      `json:"processed_options"`
	Images           []ImageInfo `json:"images"`
	FinalPrompt      string      `json:"final_prompt"`
	Reasoning        string      `json:"reasoning"`
	RawContent       string      `json:"raw_content"`
	Answer           string      `json:"answer"`
	AnswerModel      string      `json:"answer_model"`
	CaptionModel     string      `json:"caption_model"`
	Vision           bool        `json:"vision"`
	AgentError       string      `json:"agent_error,omitempty"`
	TotalTimeMS      int64       `json:"total_time_ms"`
	Calls            []ModelCall `json:"calls,omitempty"`
}

// RecordSummary 是列表轮询使用的记录摘要。
type RecordSummary struct {
	ID             int64  `json:"id"`
	Timestamp      string `json:"timestamp"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	ProcessedTitle string `json:"processed_title"`
	Answer         string `json:"answer"`
	TotalTimeMS    int64  `json:"total_time_ms"`
}

// ModelCall 是一次模型调用的用量记录。
type ModelCall struct {
	ID               int64  `json:"id"`
	RecordID         int64  `json:"record_id"`
	Slot             string `json:"slot"`
	Vendor           string `json:"vendor"`
	Protocol         string `json:"protocol"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
	Status           string `json:"status"`
	Error            string `json:"error,omitempty"`
	Timestamp        string `json:"timestamp"`
}

type Store struct{ db *sql.DB }

const recordColumns = "id, timestamp, type, status, raw_title, raw_options, processed_title, processed_options, images, final_prompt, reasoning, raw_content, answer, answer_model, caption_model, vision, agent_error, total_time_ms"

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS records (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL, type TEXT NOT NULL,
		status TEXT DEFAULT 'complete', raw_title TEXT DEFAULT '', raw_options TEXT DEFAULT '',
		processed_title TEXT DEFAULT '', processed_options TEXT DEFAULT '', images TEXT DEFAULT '[]',
		final_prompt TEXT DEFAULT '', reasoning TEXT DEFAULT '', raw_content TEXT DEFAULT '',
		answer TEXT DEFAULT '', answer_model TEXT DEFAULT '', caption_model TEXT DEFAULT '',
		vision INTEGER DEFAULT 0, agent_error TEXT DEFAULT '', total_time_ms INTEGER DEFAULT 0);
		CREATE TABLE IF NOT EXISTS model_calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT, record_id INTEGER NOT NULL, slot TEXT DEFAULT '',
		vendor TEXT DEFAULT '', protocol TEXT DEFAULT '', model TEXT DEFAULT '',
		prompt_tokens INTEGER DEFAULT 0, completion_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0,
		latency_ms INTEGER DEFAULT 0, status TEXT DEFAULT 'ok', error TEXT DEFAULT '', timestamp TEXT DEFAULT '');
		CREATE INDEX IF NOT EXISTS idx_model_calls_record ON model_calls(record_id);
		CREATE INDEX IF NOT EXISTS idx_model_calls_timestamp ON model_calls(timestamp);
		CREATE TABLE IF NOT EXISTS usage_totals (id INTEGER PRIMARY KEY CHECK (id=1), total_tokens INTEGER DEFAULT 0);
		CREATE INDEX IF NOT EXISTS idx_records_timestamp ON records(timestamp);
		CREATE TABLE IF NOT EXISTS record_totals (id INTEGER PRIMARY KEY CHECK (id=1),
		complete_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, total_time_ms INTEGER DEFAULT 0);`); err != nil {
		db.Close()
		return nil, err
	}
	// 首次建立用量总计表时纳入已有调用，之后由每次写入增量维护。
	var initialized int
	if err = db.QueryRow(`SELECT COUNT(*) FROM usage_totals WHERE id=1`).Scan(&initialized); err != nil {
		db.Close()
		return nil, err
	}
	if initialized == 0 {
		if _, err = db.Exec(`INSERT OR IGNORE INTO usage_totals (id, total_tokens)
			SELECT 1, COALESCE(SUM(total_tokens),0) FROM model_calls`); err != nil {
			db.Close()
			return nil, err
		}
	}
	rows, err := db.Query("PRAGMA table_info(records)")
	if err != nil {
		db.Close()
		return nil, err
	}
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def sql.NullString
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			break
		}
		cols[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, name := range []string{"status", "reasoning", "raw_content"} {
		if !cols[name] {
			if _, err = db.Exec("ALTER TABLE records ADD COLUMN " + name + " TEXT DEFAULT ''"); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	// 旧库中的模型列更名为回答模型与图片转述模型。
	for old, current := range map[string]string{"text_model": "answer_model", "vision_model": "caption_model"} {
		if cols[old] && !cols[current] {
			if _, err = db.Exec("ALTER TABLE records RENAME COLUMN " + old + " TO " + current); err != nil {
				db.Close()
				return nil, err
			}
			delete(cols, old)
			cols[current] = true
		}
	}
	if !cols["vision"] {
		if _, err = db.Exec("ALTER TABLE records ADD COLUMN vision INTEGER DEFAULT 0"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if !cols["agent_error"] {
		if _, err = db.Exec("ALTER TABLE records ADD COLUMN agent_error TEXT DEFAULT ''"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if !cols["status"] {
		if _, err = db.Exec("UPDATE records SET status='complete' WHERE status='' OR status IS NULL"); err != nil {
			db.Close()
			return nil, err
		}
	}
	// 记录统计同样只在首次建立时回填，此时旧库列已补齐。
	if err = db.QueryRow(`SELECT COUNT(*) FROM record_totals WHERE id=1`).Scan(&initialized); err != nil {
		db.Close()
		return nil, err
	}
	if initialized == 0 {
		if _, err = db.Exec(`INSERT OR IGNORE INTO record_totals (id, complete_count, success_count, total_time_ms)
			SELECT 1,
				COALESCE(SUM(CASE WHEN status='complete' THEN 1 ELSE 0 END),0),
				COALESCE(SUM(CASE WHEN status='complete' AND trim(answer)!='' AND answer NOT LIKE 'ERROR%' THEN 1 ELSE 0 END),0),
				COALESCE(SUM(CASE WHEN status='complete' THEN total_time_ms ELSE 0 END),0)
			FROM records`); err != nil {
			db.Close()
			return nil, err
		}
	}
	store := &Store{db: db}
	if err := store.migrateSearch(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) close() error { return s.db.Close() }

// addModelCall 写入一次模型调用的用量记录，并在同一事务中累计总用量。
func (s *Store) addModelCall(ctx context.Context, call ModelCall) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO model_calls (record_id, slot, vendor, protocol, model,
		prompt_tokens, completion_tokens, total_tokens, latency_ms, status, error, timestamp)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, call.RecordID, call.Slot, call.Vendor, call.Protocol, call.Model,
		call.PromptTokens, call.CompletionTokens, call.TotalTokens, call.LatencyMS, call.Status, call.Error, call.Timestamp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_totals (id, total_tokens) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET total_tokens = total_tokens + excluded.total_tokens`, call.TotalTokens); err != nil {
		return err
	}
	return tx.Commit()
}

// modelCalls 返回一条答题记录的全部模型调用。
func (s *Store) modelCalls(ctx context.Context, recordID int64) ([]ModelCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, record_id, slot, vendor, protocol, model, prompt_tokens,
		completion_tokens, total_tokens, latency_ms, status, error, timestamp FROM model_calls WHERE record_id=? ORDER BY id`, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ModelCall{}
	for rows.Next() {
		var call ModelCall
		if err = rows.Scan(&call.ID, &call.RecordID, &call.Slot, &call.Vendor, &call.Protocol, &call.Model,
			&call.PromptTokens, &call.CompletionTokens, &call.TotalTokens, &call.LatencyMS,
			&call.Status, &call.Error, &call.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	return result, rows.Err()
}

func (s *Store) pending(ctx context.Context, kind string) (int64, error) {
	r, err := s.db.ExecContext(ctx, "INSERT INTO records (timestamp, type, status) VALUES (?, ?, 'pending')", time.Now().Format("2006-01-02T15:04:05.000000"), kind)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// complete 把待处理记录写成完成状态，并同步更新记录统计。
func (s *Store) complete(ctx context.Context, r Record) error {
	if strings.TrimSpace(r.Answer) == "" {
		r.Answer = "ERROR: " + errEmptyAnswer.Error()
	}
	images, err := json.Marshal(r.Images)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE records SET timestamp=?, type=?, status='complete', raw_title=?, raw_options=?,
		processed_title=?, processed_options=?, images=?, final_prompt=?, reasoning=?, raw_content=?, answer=?,
		answer_model=?, caption_model=?, vision=?, agent_error=?, total_time_ms=? WHERE id=? AND status!='complete'`,
		r.Timestamp, r.Type, r.RawTitle, r.RawOptions, r.ProcessedTitle, r.ProcessedOptions, string(images),
		r.FinalPrompt, r.Reasoning, r.RawContent, r.Answer, r.AnswerModel, r.CaptionModel, r.Vision,
		r.AgentError, r.TotalTimeMS, r.ID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// 记录不存在或已完成，避免统计重复累计。
		return tx.Commit()
	}
	success := 0
	if successfulAnswer(r.Answer) {
		success = 1
		if err := indexHistory(ctx, tx, r.ID, r.RawTitle, r.ProcessedTitle); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO record_totals (id, complete_count, success_count, total_time_ms)
		VALUES (1, 1, ?, ?) ON CONFLICT(id) DO UPDATE SET complete_count=complete_count+1,
		success_count=success_count+excluded.success_count, total_time_ms=total_time_ms+excluded.total_time_ms`,
		success, r.TotalTimeMS); err != nil {
		return err
	}
	return tx.Commit()
}

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var images string
	err := row.Scan(&r.ID, &r.Timestamp, &r.Type, &r.Status, &r.RawTitle, &r.RawOptions, &r.ProcessedTitle,
		&r.ProcessedOptions, &images, &r.FinalPrompt, &r.Reasoning, &r.RawContent, &r.Answer, &r.AnswerModel,
		&r.CaptionModel, &r.Vision, &r.AgentError, &r.TotalTimeMS)
	if err != nil {
		return r, err
	}
	r.Images = []ImageInfo{}
	if images != "" {
		if err = json.Unmarshal([]byte(images), &r.Images); err != nil {
			r.Images = []ImageInfo{}
		}
	}
	return r, nil
}

func (s *Store) get(ctx context.Context, id int64) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, "SELECT "+recordColumns+" FROM records WHERE id=?", id))
}

func (s *Store) list(ctx context.Context, limit, offset int) ([]RecordSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, timestamp, type, status, substr(processed_title,1,300),
		substr(answer,1,200), total_time_ms FROM records ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RecordSummary{}
	for rows.Next() {
		var r RecordSummary
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.Type, &r.Status, &r.ProcessedTitle, &r.Answer, &r.TotalTimeMS); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM records").Scan(&n)
	return n, err
}

// escapeLike 转义 LIKE 模式中的通配符。
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

// successfulAnswer 排除空答案及模型错误，供统计和历史索引共用。
func successfulAnswer(answer string) bool {
	answer = strings.TrimSpace(answer)
	return answer != "" && !strings.HasPrefix(strings.ToUpper(answer), "ERROR")
}

type Stats struct {
	Total        int    `json:"total"`
	Today        int    `json:"today"`
	AvgTimeMS    int64  `json:"avg_time_ms"`
	SuccessRate  int    `json:"success_rate"`
	AnswerModel  string `json:"answer_model"`
	CaptionModel string `json:"caption_model"`
	TodayTokens  int64  `json:"today_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

func (s *Store) stats(ctx context.Context) (Stats, error) {
	var v Stats
	var complete, success, totalTimeMS int64
	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	// 完成记录数与耗时从增量统计表读取，今日记录数按时间范围统计。
	if err := s.db.QueryRowContext(ctx, `SELECT complete_count, success_count, total_time_ms FROM record_totals WHERE id=1`).
		Scan(&complete, &success, &totalTimeMS); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v.Total = int(complete)
	if complete > 0 {
		v.AvgTimeMS = int64(float64(totalTimeMS)/float64(complete) + 0.5)
		v.SuccessRate = int(float64(success)/float64(complete)*100 + 0.5)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records
		WHERE status='complete' AND timestamp>=? AND timestamp<?`, today, tomorrow).Scan(&v.Today); err != nil {
		return v, err
	}
	v.AnswerModel, v.CaptionModel = "-", "-"
	for _, item := range []struct {
		column string
		dest   *string
	}{{"answer_model", &v.AnswerModel}, {"caption_model", &v.CaptionModel}} {
		err := s.db.QueryRowContext(ctx, "SELECT "+item.column+" FROM records WHERE status='complete' AND "+item.column+" != '' ORDER BY id DESC LIMIT 1").Scan(item.dest)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return v, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(total_tokens,0) FROM usage_totals WHERE id=1`).
		Scan(&v.TotalTokens); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(total_tokens),0) FROM model_calls
		WHERE timestamp>=? AND timestamp<?`, today, tomorrow).Scan(&v.TodayTokens); err != nil {
		return v, err
	}
	return v, nil
}

type DailyCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func (s *Store) daily(ctx context.Context) ([]DailyCount, error) {
	start := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT substr(timestamp,1,10), COUNT(*) FROM records
		WHERE status='complete' AND timestamp>=? GROUP BY substr(timestamp,1,10) ORDER BY substr(timestamp,1,10)`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DailyCount{}
	for rows.Next() {
		var d DailyCount
		if err := rows.Scan(&d.Date, &d.Count); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) recent(ctx context.Context, since int64, limit int) ([]Record, error) {
	// 按 ID 过滤后再限制条数，避免新记录超过一页时丢失。
	rows, err := s.db.QueryContext(ctx, "SELECT "+recordColumns+" FROM records WHERE id>? ORDER BY id DESC LIMIT ?", since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
