package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/answer"
)

func TestPublishedCachePolicyControlsStorageAndReuse(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	request := v2Request("模型缓存期限题", "A. 第一项\nB. 第二项", "cache_first", "req-policy-one", "unused", nil)
	request.CachePolicy = &answer.CachePolicy{Allow: true, TTLDays: 1}
	one := decodeResponse(t, answerV2(t, f, request, "idem-policy-one"))
	if one.Status != "answered" {
		t.Fatal("首次查询没有调用模型")
	}
	cacheID := func(source string) string {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(ctx, `SELECT id::text FROM tiku.cache_entries WHERE source_request_id=$1`, source).Scan(&id); err != nil {
			t.Fatal("模型成功后未生成缓存", err)
		}
		return id
	}
	oneID := cacheID(one.RequestID)
	var hours float64
	if err := f.pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM(expires_at-created_at))/3600 FROM tiku.cache_entries WHERE id=$1`, oneID).Scan(&hours); err != nil || hours > 24.01 || hours < 23.99 {
		t.Fatal("请求策略没有控制实际过期时间", hours, err)
	}
	request.RequestID = "req-policy-hit"
	hit := decodeResponse(t, answerV2(t, f, request, "idem-policy-hit"))
	if hit.Status != "cache_hit" || hit.Usage.TotalTokens == nil || *hit.Usage.TotalTokens != 0 {
		t.Fatal("策略内缓存未正常命中")
	}
	request.RequestID = "req-policy-seven"
	request.CachePolicy.TTLDays = 7
	seven := decodeResponse(t, answerV2(t, f, request, "idem-policy-seven"))
	if seven.Status != "answered" || cacheID(seven.RequestID) == oneID {
		t.Fatal("不同期限复用了相同缓存条目")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tiku.cache_entries SET created_at=now()-interval '2 days',expires_at=now()+interval '5 days' WHERE id=$1`, oneID); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "req-policy-expired"
	request.CachePolicy.TTLDays = 1
	fresh := decodeResponse(t, answerV2(t, f, request, "idem-policy-expired"))
	if fresh.Status != "answered" || cacheID(fresh.RequestID) == oneID {
		t.Fatal("超过策略期限的旧缓存仍被使用或阻止重新写入")
	}
	request.RequestID = "req-policy-fresh-hit"
	if result := decodeResponse(t, answerV2(t, f, request, "idem-policy-fresh-hit")); result.Status != "cache_hit" {
		t.Fatal("过期后的新缓存无法命中")
	}
	answers, _ := f.llm.counts()
	if answers != 3 {
		t.Fatal("缓存策略产生了意外模型调用次数", answers)
	}
}

func TestCachePolicyRejectsInvalidTTLAndRespectsDisabledWrites(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	request := v2Request("不保留缓存题", "A. 第一项\nB. 第二项", "always_llm", "req-policy-disabled", "unused", nil)
	request.CachePolicy = &answer.CachePolicy{Allow: false, TTLDays: 1}
	decodeResponse(t, answerV2(t, f, request, "idem-policy-disabled"))
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.cache_entries`).Scan(&count); err != nil || count != 0 {
		t.Fatal("禁止缓存的策略仍保存了答案缓存", count, err)
	}
	request.RequestID = "req-policy-cache-denied"
	request.Mode = "cache_first"
	if result := answerV2(t, f, request, "idem-policy-cache-denied"); result.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, result) != "cache_not_allowed" {
		t.Fatal("禁止缓存的查询模式没有被拒绝")
	}
	request.Mode = "always_llm"
	for _, days := range []int{0, -1, 31} {
		request.RequestID = "req-policy-bad-ttl"
		request.CachePolicy = &answer.CachePolicy{Allow: true, TTLDays: days}
		if result := answerV2(t, f, request, "idem-policy-bad-ttl"); result.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, result) != "invalid_cache_policy" {
			t.Fatal("无效期限没有在模型调用前被拒绝", days)
		}
	}
	answers, _ := f.llm.counts()
	if answers != 1 {
		t.Fatal("拒绝的策略调用了模型", answers)
	}
}
