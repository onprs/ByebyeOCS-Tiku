package httpapi

import (
	"context"
	"strings"
	"testing"
)

func TestV2ChoiceWordsSurviveExtractionAndCache(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	f.llm.setMode("choice_word")
	first := v2Request("选择对应的组织缩写", "A. WCO\nB. WEO\nC. WHO\nD. WTO", "cache_first", "choice-word-first", "choice-word-first", nil)
	response := decodeResponse(t, answerV2(t, f, first, "choice-word-first"))
	if response.Answer != "WHO" {
		t.Fatalf("完整选项被截断: %q", response.Answer)
	}
	var version, answer string
	if err := f.pool.QueryRow(context.Background(), `SELECT normalization_version,answer FROM tiku.cache_entries WHERE source_request_id=$1`, first.RequestID).Scan(&version, &answer); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(version, "/choice-text-v2") || answer != "WHO" {
		t.Fatalf("新规则未隔离旧缓存: %q/%q", version, answer)
	}
	second := first
	second.RequestID = "choice-word-second"
	cached := decodeResponse(t, answerV2(t, f, second, "choice-word-second"))
	if cached.Answer != "WHO" || !cached.Cache.Hit {
		t.Fatalf("缓存未保留完整选项: %+v", cached)
	}
	calls, _ := f.llm.counts()
	if calls != 1 {
		t.Fatalf("相同题目重复调用了上游: %d", calls)
	}
}
