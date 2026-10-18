package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestUsageTotalsOnReopen 校验数据库重开后累计用量不重复计算，并继续累加。
func TestUsageTotalsOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	today := time.Now().Format("2006-01-02T15:04:05.000000")
	for _, tokens := range []int{100, 50} {
		if err = store.addModelCall(ctx, ModelCall{RecordID: 1, Slot: "answer", Status: "ok",
			TotalTokens: tokens, Timestamp: today}); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.close(); err != nil {
		t.Fatal(err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	stats, err := store.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalTokens != 150 || stats.TodayTokens != 150 {
		t.Fatalf("重启后用量异常: %+v", stats)
	}
	if err = store.addModelCall(ctx, ModelCall{RecordID: 2, Slot: "answer", Status: "ok",
		TotalTokens: 30, Timestamp: today}); err != nil {
		t.Fatal(err)
	}
	stats, err = store.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalTokens != 180 || stats.TodayTokens != 180 {
		t.Fatalf("重启后继续累加异常: %+v", stats)
	}
	// 非当日调用的明细不计入今日用量。
	if err = store.addModelCall(ctx, ModelCall{RecordID: 3, Slot: "answer", Status: "ok",
		TotalTokens: 7, Timestamp: "2020-01-01T00:00:00.000000"}); err != nil {
		t.Fatal(err)
	}
	stats, err = store.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalTokens != 187 || stats.TodayTokens != 180 {
		t.Fatalf("跨日用量归属异常: %+v", stats)
	}
}

// TestCancelledSearchKeepsRecord 校验客户端中断后记录仍以失败结果落库。
func TestCancelledSearchKeepsRecord(t *testing.T) {
	s, web, _ := setupServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>A</answer>"}, "finish_reason": "stop"}},
		})
	}))
	defer fake.Close()
	configureAnswer(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "demo"},
		ModelConfig{Model: "slow-model", Vision: CapabilityAuto, ToolCall: CapabilityOff})

	body, _ := json.Marshal(map[string]string{"title": "超时题目"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", web.URL+"/api/search", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err = http.DefaultClient.Do(req); err == nil {
		t.Fatal("请求应因超时失败")
	}
	time.Sleep(500 * time.Millisecond)

	records, err := s.store.list(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != "complete" || !strings.HasPrefix(records[0].Answer, "ERROR:") {
		t.Fatalf("中断记录状态异常: %+v", records)
	}
	calls, err := s.store.modelCalls(context.Background(), records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Status != "error" {
		t.Fatalf("中断调用记录异常: %+v", calls)
	}
}

// TestRecordTotalsOnReopen 校验记录统计在重启后不重复累计且能继续累加。
func TestRecordTotalsOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	completeRecord := func(answer string, elapsed int64) int64 {
		t.Helper()
		id, err := store.pending(ctx, "single")
		if err != nil {
			t.Fatal(err)
		}
		if err = store.complete(ctx, Record{ID: id, Timestamp: time.Now().Format("2006-01-02T15:04:05.000000"),
			Type: "single", Status: "complete", Answer: answer, AnswerModel: "测试服务 (model)", TotalTimeMS: elapsed}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := completeRecord("A", 100)
	completeRecord("ERROR: 模型请求失败", 300)
	if _, err = store.pending(ctx, "single"); err != nil {
		t.Fatal(err)
	}
	// 重复完成同一条记录不应重复计数，也不应改写已有结果。
	if err = store.complete(ctx, Record{ID: first, Status: "complete", Answer: "B", TotalTimeMS: 100}); err != nil {
		t.Fatal(err)
	}
	expect := func(where string) {
		t.Helper()
		stats, err := store.stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Total != 2 || stats.Today != 2 || stats.AvgTimeMS != 200 || stats.SuccessRate != 50 {
			t.Fatalf("%s 统计异常: %+v", where, stats)
		}
	}
	expect("首次写入后")
	if err = store.close(); err != nil {
		t.Fatal(err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	expect("重启后")
	completeRecord("C", 100)
	stats, err := store.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Today != 3 {
		t.Fatalf("重启后继续累加异常: %+v", stats)
	}
}

// TestRecordTotalsMigration 模拟旧库首次建立统计表时回填已有记录。
func TestRecordTotalsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02T15:04:05.000000")
	for _, row := range []struct {
		answer  string
		elapsed int64
		model   string
	}{
		{"A", 100, "服务 (model)"},
		{"ERROR: 失败", 300, "服务 (model)"},
		{"A", 200, "服务 (model)"},
	} {
		if _, err = store.db.Exec(`INSERT INTO records (timestamp, type, status, answer, answer_model, total_time_ms)
			VALUES (?,?,?,?,?,?)`, today, "single", "complete", row.answer, row.model, row.elapsed); err != nil {
			t.Fatal(err)
		}
	}
	// 旧版本数据库没有统计表，升级时首次建立并回填。
	if _, err = store.db.Exec(`DROP TABLE record_totals`); err != nil {
		t.Fatal(err)
	}
	if err = store.close(); err != nil {
		t.Fatal(err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	stats, err := store.stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Today != 3 || stats.AvgTimeMS != 200 || stats.SuccessRate != 67 {
		t.Fatalf("迁移回填异常: %+v", stats)
	}
}

// TestUsageErrorTruncation 校验超长错误按多字节字符截断后仍是有效 UTF-8。
func TestUsageErrorTruncation(t *testing.T) {
	text := strings.Repeat("错误信息", 300)
	if got := truncateText(text, 500); len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("截断结果异常: %d 字节", len(got))
	}
	store, err := openStore(filepath.Join(t.TempDir(), "truncate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	ctx := t.Context()
	if err = store.addModelCall(ctx, ModelCall{RecordID: 1, Slot: "answer", Status: "error",
		Error: truncateText(text, 500), Timestamp: "2026-01-01T00:00:00.000000"}); err != nil {
		t.Fatal(err)
	}
	calls, err := store.modelCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !utf8.ValidString(calls[0].Error) || len(calls[0].Error) > 500 {
		t.Fatalf("存储的错误信息异常: %+v", calls)
	}
}
