package main

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// historyTerms 建立单字与双字倒排索引，同时支持中文短词和英文片段。
func historyTerms(texts ...string) []string {
	unique := map[string]struct{}{}
	for _, text := range texts {
		runes := []rune(strings.ToLower(text))
		for i, r := range runes {
			unique[string(r)] = struct{}{}
			if i+1 < len(runes) {
				unique[string(runes[i:i+2])] = struct{}{}
			}
		}
	}
	terms := make([]string, 0, len(unique))
	for term := range unique {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

// queryTerms 取相邻双字作为候选条件，最终仍按完整关键词验证字面匹配。
func queryTerms(keyword string) []string {
	runes := []rune(strings.ToLower(keyword))
	if len(runes) == 0 {
		return nil
	}
	if len(runes) == 1 {
		return []string{string(runes)}
	}
	unique := map[string]bool{}
	terms := []string{}
	for i := 0; i+1 < len(runes) && len(terms) < 8; i++ {
		term := string(runes[i : i+2])
		if !unique[term] {
			unique[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

func indexHistory(ctx context.Context, tx *sql.Tx, id int64, title, processedTitle string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM record_search_terms WHERE record_id=?", id); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO record_search_terms(term, record_id) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, term := range historyTerms(title, processedTitle) {
		if _, err := statement.ExecContext(ctx, term, id); err != nil {
			return err
		}
	}
	return nil
}

// migrateSearch 只在升级时回填历史索引和修正旧空答案的成功统计。
func (s *Store) migrateSearch() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_records_complete_timestamp ON records(timestamp) WHERE status='complete';
		CREATE INDEX IF NOT EXISTS idx_records_latest_answer ON records(id DESC, answer_model)
			WHERE status='complete' AND answer_model!='';
		CREATE INDEX IF NOT EXISTS idx_records_latest_caption ON records(id DESC, caption_model)
			WHERE status='complete' AND caption_model!='';
		CREATE TABLE IF NOT EXISTS record_search_terms (
			term TEXT NOT NULL, record_id INTEGER NOT NULL, PRIMARY KEY(term, record_id)) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS idx_record_search_record ON record_search_terms(record_id);`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		var lastID, successCount int64
		for {
			rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(raw_title,''), COALESCE(processed_title,''), COALESCE(answer,'')
				FROM records WHERE status='complete' AND id>? ORDER BY id LIMIT 200`, lastID)
			if err != nil {
				return err
			}
			batch := []Record{}
			for rows.Next() {
				var record Record
				if err := rows.Scan(&record.ID, &record.RawTitle, &record.ProcessedTitle, &record.Answer); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, record)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			for _, record := range batch {
				lastID = record.ID
				if successfulAnswer(record.Answer) {
					successCount++
					if err := indexHistory(ctx, tx, record.ID, record.RawTitle, record.ProcessedTitle); err != nil {
						return err
					}
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE record_totals SET success_count=? WHERE id=1", successCount); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// search 先按倒排索引取候选，再验证完整关键词，保留 LIKE 转义和按 ID 排序的行为。
func (s *Store) search(ctx context.Context, keyword string, limit int) ([]Record, error) {
	terms := queryTerms(keyword)
	result := []Record{}
	if len(terms) == 0 || limit < 1 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(terms)), ",")
	query := `SELECT id, type, raw_title, raw_options, processed_title, processed_options, answer FROM records
		WHERE id IN (SELECT record_id FROM record_search_terms WHERE term IN (` + placeholders + `)
		GROUP BY record_id HAVING COUNT(*)=?) AND status='complete'
		AND (raw_title LIKE ? ESCAPE '\' OR processed_title LIKE ? ESCAPE '\') ORDER BY id DESC LIMIT ?`
	args := make([]any, 0, len(terms)+4)
	for _, term := range terms {
		args = append(args, term)
	}
	pattern := "%" + escapeLike(keyword) + "%"
	args = append(args, len(terms), pattern, pattern, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var record Record
		if err := rows.Scan(&record.ID, &record.Type, &record.RawTitle, &record.RawOptions,
			&record.ProcessedTitle, &record.ProcessedOptions, &record.Answer); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}
