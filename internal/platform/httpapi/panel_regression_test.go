package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

func TestPostgresModelConfigurationSeparatesCaches(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	query := func(number int) bool {
		id := fmt.Sprintf("config-cache-%04d", number)
		return decodeResponse(t, answerV2(t, f, v2Request("缓存隔离题", "A. 甲", "cache_first", id, id, nil), id)).Cache.Hit
	}
	if query(1) || !query(2) {
		t.Fatal("原模型的精确缓存行为异常")
	}
	items := f.registry.Models()
	items[0].DisplayName = "更新显示名称"
	if err := f.registry.Replace(items, f.keys); err != nil {
		t.Fatal(err)
	}
	if !query(3) {
		t.Fatal("仅更改显示名称不应使缓存失效")
	}
	items[0].Model = "another-model"
	if err := f.registry.Replace(items, f.keys); err != nil {
		t.Fatal(err)
	}
	if query(4) || !query(5) {
		t.Fatal("变更上游模型后不应复用旧模型答案")
	}
	keys := map[string]string{"standard": "different-key", "caption-model": f.keys["caption-model"]}
	if err := f.registry.Replace(items, keys); err != nil {
		t.Fatal(err)
	}
	if query(6) {
		t.Fatal("更换凭据后不应复用旧连接的答案")
	}
}

func TestPostgresOutputBudgetSeparatesCaches(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	query := func(id string, budget int) bool {
		input := v2Request("预算隔离题", "A. 甲", "cache_first", id, id, nil)
		input.Budget.MaxOutputTokens = budget
		return decodeResponse(t, answerV2(t, f, input, id)).Cache.Hit
	}
	if query("budget-cache-0001", 64) || !query("budget-cache-0002", 64) || query("budget-cache-0003", 128) {
		t.Fatal("不同的有效输出预算不应复用同一缓存")
	}
}

func TestPostgresEmptyFunctionalTestIsNotSuccessful(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "empty", vision: true})
	result, err := f.svc.TestModel(context.Background(), "standard", "functional", true)
	if err != nil || result.Status != "failed" || result.Functional || result.ErrorCode != "empty_answer" {
		t.Fatalf("空回答不得标记测试成功：%+v %v", result, err)
	}
}

func TestPostgresCaptionConfigurationSeparatesCaches(t *testing.T) {
	f := newFixture(t, fixtureOptions{caption: true})
	image := testPNG(t)
	query := func(number int) bool {
		id := fmt.Sprintf("caption-cache-%04d", number)
		return decodeResponse(t, answerV2(t, f, v2Request("看图[图片1]", "A. 甲", "cache_first", id, id, []string{image}), id)).Cache.Hit
	}
	if query(1) || !query(2) {
		t.Fatal("原图片转述模型缓存行为异常")
	}
	items := f.registry.Models()
	items[1].Model = "updated-caption"
	if err := f.registry.Replace(items, f.keys); err != nil {
		t.Fatal(err)
	}
	if query(3) {
		t.Fatal("更换图片转述模型后不应复用旧答案")
	}
}

func TestPostgresFailedCallsKeepUsageFacts(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "caption_then_rate_limited", caption: true})
	id := "partial-call-0001"
	recorder := answerV2(t, f, v2Request("看图[图片1]", "A. 甲", "always_llm", id, id, []string{testPNG(t)}), id)
	if recorder.Code != 409 || decodeErrorCode(t, recorder) != "request_unknown" {
		t.Fatalf("部分调用已产生用量时必须待核对：%d %s", recorder.Code, recorder.Body.String())
	}
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM tiku.requests WHERE request_id=$1`, id).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("结果不明的幂等事实未保存：%s %v", state, err)
	}
	var knownInput, usageComplete string
	if err := f.pool.QueryRow(context.Background(), `SELECT usage->>'input_tokens',usage->>'complete' FROM tiku.queries WHERE request_id=$1`, id).Scan(&knownInput, &usageComplete); err != nil || knownInput != "100" || usageComplete != "false" {
		t.Fatalf("已确认的部分用量未保留或被误标完整：%s %s %v", knownInput, usageComplete, err)
	}
	rows, err := f.pool.Query(context.Background(), `SELECT slot,status,usage_complete FROM tiku.model_calls WHERE request_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	facts := map[string]string{}
	for rows.Next() {
		var slot, status string
		var complete bool
		if err := rows.Scan(&slot, &status, &complete); err != nil {
			t.Fatal(err)
		}
		facts[slot] = fmt.Sprintf("%s/%t", status, complete)
	}
	if rows.Err() != nil || facts["caption"] != "ok/true" || facts["answer"] != "error/false" {
		t.Fatalf("失败后调用事实不完整：%v %v", facts, rows.Err())
	}
	f.llm.setMode("bad_total")
	id = "incomplete-call-0002"
	_ = decodeResponse(t, answerV2(t, f, v2Request("不完整用量", "A. 甲", "always_llm", id, id, nil), id))
	var complete bool
	if err := f.pool.QueryRow(context.Background(), `SELECT usage_complete FROM tiku.model_calls WHERE request_id=$1`, id).Scan(&complete); err != nil || complete {
		t.Fatalf("矛盾的用量不得标记完整：%t %v", complete, err)
	}
}

func TestPostgresCallWriteFailureIsUnknownAndNotRepeated(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE tiku.model_calls ADD CONSTRAINT fixture_reject_calls CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	id := "failed-call-write-0001"
	input := v2Request("写入异常题", "A. 甲", "always_llm", id, id, nil)
	for attempt := 0; attempt < 2; attempt++ {
		recorder := answerV2(t, f, input, id)
		if recorder.Code != 409 || decodeErrorCode(t, recorder) != "request_unknown" {
			t.Fatalf("调用事实写入失败应待核对：%d %s", recorder.Code, recorder.Body.String())
		}
	}
	calls, _ := f.llm.counts()
	if calls != 1 {
		t.Fatalf("结果待核对时同键不得重复调用模型：%d", calls)
	}
}

func TestPostgresPanelPaginationSourcesAndIndexes(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO tiku.requests(idempotency_key,request_id,fingerprint,state,created_at)
 SELECT 'key-'||n,'request-'||lpad(n::text,5,'0'),repeat('a',64),'pending',now()-interval '1 second'
 FROM generate_series(1,5000) n`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `INSERT INTO tiku.queries(request_id,offering_id,mode,cache_scope,scope_key_hash,status,usage,latency_ms,created_at)
 SELECT 'request-'||lpad(n::text,5,'0'),'standard','always_llm','user','hash','answered','{}'::jsonb,0,now()-interval '1 second'
 FROM generate_series(1,12) n
 UNION ALL
 SELECT 'legacy-'||lpad(n::text,5,'0'),'standard','always_llm','user','hash','answered','{}'::jsonb,0,now()-interval '1 second'
 FROM generate_series(1,12) n`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	filter := store.PanelFilter{Limit: 7, Model: "standard", Status: "answered"}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := f.store.PanelRequests(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range page.Items {
			var item struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" || seen[item.ID] {
				t.Fatalf("分页重复或内容无效：%s %v", raw, err)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		filter.Cursor = *page.NextCursor
	}
	if len(seen) != 24 {
		t.Fatalf("有筛选时分页遗漏来源：%d", len(seen))
	}
	if _, err := f.pool.Exec(ctx, `ANALYZE tiku.requests; ANALYZE tiku.queries`); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := f.pool.QueryRow(ctx, `EXPLAIN SELECT request_id FROM tiku.requests ORDER BY created_at DESC,request_id DESC LIMIT 10`).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "Limit") {
		t.Fatalf("查询计划缺少有界读取：%s", plan)
	}
	rows, err := f.pool.Query(ctx, `EXPLAIN SELECT request_id FROM tiku.requests ORDER BY created_at DESC,request_id DESC LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil || !strings.Contains(strings.Join(lines, "\n"), "requests_panel_page_idx") {
		t.Fatalf("查询计划未使用分页索引：%v %v", lines, rows.Err())
	}
}
