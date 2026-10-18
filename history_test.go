package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func addHistoryRecord(t testing.TB, store *Store, title, processedTitle, answer string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := store.pending(ctx, "single")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.complete(ctx, Record{ID: id, Timestamp: time.Now().Format("2006-01-02T15:04:05"), Type: "single",
		RawTitle: title, ProcessedTitle: processedTitle, RawOptions: "A. 氮气\nB. 氧气", ProcessedOptions: "A. 氮气\nB. 氧气", Answer: answer}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIndexedHistorySearch(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	for _, title := range []string{"单调函数的性质", "函数与数单和单调", "FUNCTION 是英文", "完成比例 100%", "变量 x_y", `路径 C:\course`, "氮气是主要气体"} {
		addHistoryRecord(t, store, title, title, "A")
	}
	addHistoryRecord(t, store, "识别后的题目", "转述结果中的唯一关键词", "B")
	addHistoryRecord(t, store, "失败的函数题目", "失败的函数题目", "ERROR: 调用失败")
	addHistoryRecord(t, store, "空白的函数题目", "空白的函数题目", " ")
	for _, test := range []struct {
		query string
		count int
	}{
		{"函数", 2}, {"单调函数", 1}, {"函数单调", 0}, {"function", 1}, {"%", 1}, {"100%", 1},
		{"_", 1}, {"x_y", 1}, {`C:\course`, 1}, {"氮", 1}, {"唯一关键词", 1}, {"不存在的片段", 0}, {"", 0},
	} {
		t.Run(test.query, func(t *testing.T) {
			records, err := store.search(t.Context(), test.query, 10)
			if err != nil || len(records) != test.count {
				t.Fatalf("关键词 %q 返回 %d 条，预期 %d 条: %v", test.query, len(records), test.count, err)
			}
		})
	}
	result, err := (&historyTool{store: store}).InvokableRun(t.Context(), `{"query":"主要气体"}`)
	if err != nil || !strings.Contains(result, "A. 氮气") || !strings.Contains(result, "B. 氧气") || !strings.Contains(result, "答案：A") || !strings.Contains(result, "单选题") {
		t.Fatalf("历史工具缺少原选项、题型或答案: %s %v", result, err)
	}
	records, err := store.search(t.Context(), "函数", 1)
	if err != nil || len(records) != 1 || records[0].RawTitle != "函数与数单和单调" {
		t.Fatalf("检索没有保留最近记录优先和条数限制: %+v %v", records, err)
	}
}

func TestHistoryMigrationRepairsEmptyAnswerStatistics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"A", "", "ERROR: 旧错误"} {
		if _, err := store.db.Exec(`INSERT INTO records(timestamp,type,status,raw_title,processed_title,answer)
			VALUES (?, 'single', 'complete', '旧版函数题目', '旧版函数题目', ?)`, time.Now().Format("2006-01-02T15:04:05"), answer); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟升级前的数据库：没有检索索引，旧空答案曾计入成功。
	if _, err := store.db.Exec(`DROP TABLE record_search_terms; PRAGMA user_version=0;
		UPDATE record_totals SET complete_count=3, success_count=2, total_time_ms=300 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	store.close()
	for range 2 {
		store, err = openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := store.stats(t.Context())
		if err != nil || stats.Total != 3 || stats.SuccessRate != 33 || stats.AvgTimeMS != 100 {
			t.Fatalf("旧统计修正异常: %+v %v", stats, err)
		}
		records, err := store.search(t.Context(), "函数", 10)
		if err != nil || len(records) != 1 || records[0].Answer != "A" {
			t.Fatalf("历史索引回填异常: %+v %v", records, err)
		}
		store.close()
	}
}

func TestHistoryMigrationBackfillsMultipleBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 205 {
		if _, err := store.db.Exec(`INSERT INTO records(timestamp,type,status,raw_title,processed_title,answer)
			VALUES ('2026-01-01', 'single', 'complete', ?, ?, 'A')`, fmt.Sprintf("第 %d 道跨批次历史题目", i), fmt.Sprintf("第 %d 道跨批次历史题目", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("DROP TABLE record_search_terms; PRAGMA user_version=0; UPDATE record_totals SET complete_count=205, success_count=205 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	store.close()
	store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	records, err := store.search(t.Context(), "跨批次历史", 300)
	if err != nil || len(records) != 205 || records[0].ID != 205 || records[204].ID != 1 {
		t.Fatalf("历史索引跨批次回填遗漏或乱序: 条数=%d，错误=%v", len(records), err)
	}
}

func TestStatisticsAndHistoryUseIndexes(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	for _, test := range []struct {
		query string
		index string
	}{
		{`SELECT caption_model FROM records WHERE status='complete' AND caption_model!='' ORDER BY id DESC LIMIT 1`, "idx_records_latest_caption"},
		{`SELECT answer_model FROM records WHERE status='complete' AND answer_model!='' ORDER BY id DESC LIMIT 1`, "idx_records_latest_answer"},
		{`SELECT COUNT(*) FROM records WHERE status='complete' AND timestamp>='2026-01-01' AND timestamp<'2026-01-02'`, "idx_records_complete_timestamp"},
		{`SELECT record_id FROM record_search_terms WHERE term='函数'`, "PRIMARY KEY"},
	} {
		rows, err := store.db.Query("EXPLAIN QUERY PLAN " + test.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !strings.Contains(plan.String(), test.index) {
			t.Fatalf("查询未使用索引 %s: %s %v", test.index, plan.String(), err)
		}
	}
}

func BenchmarkHistorySearch(b *testing.B) {
	store, err := openStore(filepath.Join(b.TempDir(), "records.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.close()
	for i := range 2000 {
		title := fmt.Sprintf("第 %d 道基础练习题目", i)
		addHistoryRecord(b, store, title, title, "A")
	}
	for b.Loop() {
		if _, err := store.search(context.Background(), "没有收录的知识点", 5); err != nil {
			b.Fatal(err)
		}
	}
}
