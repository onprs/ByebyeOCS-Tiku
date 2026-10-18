package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type ImageInfo struct {
	URL         string `json:"url"`
	Description string `json:"description"`
	Src         string `json:"src"`
	Status      string `json:"status"`
}

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
	TotalTimeMS      int64       `json:"total_time_ms"`
}

type Store struct{ db *sql.DB }

const recordColumns = "id, timestamp, type, status, raw_title, raw_options, processed_title, processed_options, images, final_prompt, reasoning, raw_content, answer, answer_model, caption_model, vision, total_time_ms"

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
		vision INTEGER DEFAULT 0, total_time_ms INTEGER DEFAULT 0);`); err != nil {
		db.Close()
		return nil, err
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
	if !cols["status"] {
		if _, err = db.Exec("UPDATE records SET status='complete' WHERE status='' OR status IS NULL"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) close() error { return s.db.Close() }

func (s *Store) pending(ctx context.Context, kind string) (int64, error) {
	r, err := s.db.ExecContext(ctx, "INSERT INTO records (timestamp, type, status) VALUES (?, ?, 'pending')", time.Now().Format("2006-01-02T15:04:05.000000"), kind)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) complete(ctx context.Context, r Record) error {
	images, err := json.Marshal(r.Images)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE records SET timestamp=?, type=?, status='complete', raw_title=?, raw_options=?,
		processed_title=?, processed_options=?, images=?, final_prompt=?, reasoning=?, raw_content=?, answer=?,
		answer_model=?, caption_model=?, vision=?, total_time_ms=? WHERE id=?`, r.Timestamp, r.Type, r.RawTitle, r.RawOptions,
		r.ProcessedTitle, r.ProcessedOptions, string(images), r.FinalPrompt, r.Reasoning, r.RawContent,
		r.Answer, r.AnswerModel, r.CaptionModel, r.Vision, r.TotalTimeMS, r.ID)
	return err
}

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var images string
	err := row.Scan(&r.ID, &r.Timestamp, &r.Type, &r.Status, &r.RawTitle, &r.RawOptions, &r.ProcessedTitle,
		&r.ProcessedOptions, &images, &r.FinalPrompt, &r.Reasoning, &r.RawContent, &r.Answer, &r.AnswerModel,
		&r.CaptionModel, &r.Vision, &r.TotalTimeMS)
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

func (s *Store) list(ctx context.Context, limit, offset int) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+recordColumns+" FROM records ORDER BY id DESC LIMIT ? OFFSET ?", limit, offset)
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

func (s *Store) count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM records").Scan(&n)
	return n, err
}

type Stats struct {
	Total        int    `json:"total"`
	Today        int    `json:"today"`
	AvgTimeMS    int64  `json:"avg_time_ms"`
	SuccessRate  int    `json:"success_rate"`
	AnswerModel  string `json:"answer_model"`
	CaptionModel string `json:"caption_model"`
}

func (s *Store) stats(ctx context.Context) (Stats, error) {
	var v Stats
	var avg float64
	var success int
	today := time.Now().Format("2006-01-02")
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN substr(timestamp,1,10)=? THEN 1 ELSE 0 END),0),
		COALESCE(AVG(total_time_ms),0), COALESCE(SUM(CASE WHEN answer NOT LIKE 'ERROR%' THEN 1 ELSE 0 END),0)
		FROM records WHERE status='complete'`, today).Scan(&v.Total, &v.Today, &avg, &success)
	if err != nil {
		return v, err
	}
	v.AvgTimeMS = int64(avg + 0.5)
	if v.Total > 0 {
		v.SuccessRate = int(float64(success)/float64(v.Total)*100 + 0.5)
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
	return v, nil
}

type DailyCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func (s *Store) daily(ctx context.Context) ([]DailyCount, error) {
	start := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT substr(timestamp,1,10), COUNT(*) FROM records
		WHERE status='complete' AND substr(timestamp,1,10)>=? GROUP BY substr(timestamp,1,10) ORDER BY substr(timestamp,1,10)`, start)
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
