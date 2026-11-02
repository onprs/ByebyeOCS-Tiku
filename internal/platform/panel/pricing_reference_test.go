package panel

import (
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func pricingFixture(t *testing.T, catalog string) *pricingReferenceService {
	t.Helper()
	service := newPricingReferenceService()
	service.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	service.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := catalog
		if request.URL.String() == currencyRateURL {
			body = `{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return service
}

func pricingFailureCode(err error) string {
	var failure *pricingLookupError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func TestPricingReferenceConvertsTieredRatesAndCachesSources(t *testing.T) {
	var requests atomic.Int32
	service := newPricingReferenceService()
	service.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	service.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := `{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.27,"output":1.1,"cache_read":0.07,"cache_write":0.35}}}}}`
		if request.URL.String() == currencyRateURL {
			body = `{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for range 2 {
		reference, err := service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
		if err != nil {
			t.Fatal(err)
		}
		if reference.Rates.Input != "1797444" || reference.Rates.Output != "7322920" ||
			reference.Rates.CacheRead == nil || *reference.Rates.CacheRead != "466004" ||
			reference.Rates.CacheWrite == nil || *reference.Rates.CacheWrite != "2330020" {
			t.Fatalf("人民币分档费率换算错误：%+v", reference.Rates)
		}
		if reference.ExchangeDate != "2026-10-30" || reference.ExchangeRate != "6.6572" || reference.Source != "models.dev" {
			t.Fatalf("参考来源事实缺失：%+v", reference)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("缓存未复用模型目录和汇率：请求次数 %d", requests.Load())
	}
}

func TestPricingReferenceMatchesCanonicalAndSnapshotNames(t *testing.T) {
	catalog := `{"deepseek":{"models":{
		"deepseek-v4-flash":{"id":"deepseek-v4-flash","name":"DeepSeek V4.1 Flash","canonical_model_id":"deepseek/deepseek-v4.1-flash-1017","cost":{"input":0.15,"output":0.6,"cache_read":0.003}},
		"deepseek-v4-pro":{"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","canonical_model_id":"deepseek/deepseek-v4-pro-0813","cost":{"input":0.66,"output":1.98}}
	}}}`
	service := pricingFixture(t, catalog)
	for _, name := range []string{"deepseek/deepseek-v4.1-flash", "deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash-1017", "deepseek-v4-flash"} {
		reference, err := service.Lookup(context.Background(), "deepseek", name)
		if err != nil {
			t.Fatalf("%s 应匹配到官方价格：%v", name, err)
		}
		if reference.Model != "deepseek-v4-flash" || reference.Rates.Input != "998580" || reference.Rates.Output != "3994320" {
			t.Fatalf("%s 匹配了错误条目：%+v", name, reference)
		}
		if reference.Rates.CacheRead == nil || *reference.Rates.CacheRead != "19972" {
			t.Fatalf("%s 缺少缓存读取价：%+v", name, reference.Rates)
		}
	}
	reference, err := service.Lookup(context.Background(), "deepseek", "deepseek/deepseek-v4-pro")
	if err != nil || reference.Model != "deepseek-v4-pro" || reference.Rates.CacheWrite != nil {
		t.Fatalf("没有缓存创建费的模型应正常返回且不伪造费率：%+v/%v", reference.Rates, err)
	}
}

func TestPricingReferenceDoesNotGuessAcrossDifferentModels(t *testing.T) {
	service := pricingFixture(t, `{"deepseek":{"models":{"deepseek-v4-pro":{"cost":{"input":0.66,"output":1.98}}}}}`)
	if _, err := service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash"); pricingFailureCode(err) != pricingModelUnknown {
		t.Fatalf("不同模型错误复用了价格：%v", err)
	}
	if _, err := service.Lookup(context.Background(), "missing", "some-model"); pricingFailureCode(err) != pricingProviderUnknown {
		t.Fatalf("未知提供商错误使用了其他条目：%v", err)
	}
}

func TestPricingReferenceReportsIncompleteAndAmbiguousCosts(t *testing.T) {
	incomplete := pricingFixture(t, `{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.15}}}}}`)
	if _, err := incomplete.Lookup(context.Background(), "deepseek", "deepseek-v4-flash"); pricingFailureCode(err) != pricingIncomplete {
		t.Fatalf("缺少输出价格时没有报告不完整：%v", err)
	}
	ambiguous := pricingFixture(t, `{"deepseek":{"models":{
		"deepseek-v4-flash":{"canonical_model_id":"deepseek/deepseek-v4.1-flash-1017","cost":{"input":0.15,"output":0.6}},
		"deepseek-flash":{"canonical_model_id":"deepseek/deepseek-v4.1-flash-0813","cost":{"input":0.25,"output":0.9}}
	}}}`)
	if _, err := ambiguous.Lookup(context.Background(), "deepseek", "deepseek/deepseek-v4.1-flash"); pricingFailureCode(err) != pricingAmbiguous {
		t.Fatalf("快照价格不一致时没有报告歧义：%v", err)
	}
}

func TestPricingReferenceUsesUniqueCrossProviderCanonicalPrice(t *testing.T) {
	service := pricingFixture(t, `{"gateway":{"models":{}},"alpha":{"models":{"acme-model":{"canonical_model_id":"acme/model","cost":{"input":0.2,"output":0.8}}}}}`)
	reference, err := service.Lookup(context.Background(), "gateway", "acme/model")
	if err != nil || reference.Provider != "alpha" || reference.Model != "acme-model" {
		t.Fatalf("唯一跨提供商规范价格未生效：%+v/%v", reference, err)
	}
}

func TestPricingReferenceWarmPrefetchesSources(t *testing.T) {
	var requests atomic.Int32
	service := newPricingReferenceService()
	service.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	service.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := `{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.15,"output":0.6}}}}}`
		if request.URL.String() == currencyRateURL {
			body = `{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	service.Warm(context.Background())
	if requests.Load() != 2 {
		t.Fatalf("预热没有读取两项价格来源：%d", requests.Load())
	}
	reference, err := service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || reference.Rates.Input != "998580" || requests.Load() != 2 {
		t.Fatalf("预热后仍重复请求来源或换算失败：requests=%d reference=%+v err=%v", requests.Load(), reference, err)
	}
}

func TestPricingReferenceRetriesTransientSourceFailure(t *testing.T) {
	catalogAttempts := 0
	service := newPricingReferenceService()
	service.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	service.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == currencyRateURL {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`))}, nil
		}
		catalogAttempts++
		if catalogAttempts == 1 {
			return nil, errors.New("临时网络错误")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.15,"output":0.6}}}}}`))}, nil
	})}
	reference, err := service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || catalogAttempts != 2 || reference.Rates.Input != "998580" {
		t.Fatalf("单次瞬时失败应自动重试：attempts=%d reference=%+v err=%v", catalogAttempts, reference, err)
	}
}

func TestPricingReferencePersistsCacheAcrossRestartAndStaleFallback(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := `{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.15,"output":0.6,"cache_read":0.003}}}}}`
		if request.URL.String() == currencyRateURL {
			body = `{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	first := newPricingReferenceServiceWithCache(dir, time.Hour, time.Hour)
	first.now = func() time.Time { return now }
	first.client = client
	if _, err := first.Lookup(context.Background(), "deepseek", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("首次查询应读取两项来源：%d", requests.Load())
	}
	for _, name := range []string{pricingCatalogCacheFile, pricingExchangeCacheFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("价格缓存文件缺失或权限错误：%s %v/%v", name, info, err)
		}
	}
	second := newPricingReferenceServiceWithCache(dir, time.Hour, time.Hour)
	second.now = func() time.Time { return now.Add(30 * time.Minute) }
	second.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("有效缓存仍发起了网络请求")
		return nil, errors.New("不应调用")
	})}
	reference, err := second.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || reference.Rates.Input != "998580" || reference.Stale || requests.Load() != 2 {
		t.Fatalf("重启后没有复用磁盘缓存：%+v/%v requests=%d", reference, err, requests.Load())
	}
	if status := second.Status(); status.CatalogEntries != 1 || status.Stale || status.ExchangeDate != "2026-10-30" {
		t.Fatalf("缓存状态错误：%+v", status)
	}
	third := newPricingReferenceServiceWithCache(dir, time.Hour, time.Hour)
	third.now = func() time.Time { return now.Add(2 * time.Hour) }
	third.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("网络不可用")
	})}
	reference, err = third.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || !reference.Stale || reference.Rates.Input != "998580" {
		t.Fatalf("来源失败时没有使用过期缓存：%+v/%v", reference, err)
	}
	if status := third.Status(); !status.Stale {
		t.Fatalf("过期缓存状态未标记：%+v", status)
	}
	if _, err := third.Refresh(context.Background()); err == nil {
		t.Fatal("刷新失败时返回成功")
	}
	if reference, err := third.Lookup(context.Background(), "deepseek", "deepseek-v4-flash"); err != nil || !reference.Stale {
		t.Fatalf("刷新失败清除了可用缓存：%+v/%v", reference, err)
	}
}

func TestPricingReferenceRefreshUpdatesCachedSources(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var version atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"deepseek":{"models":{"deepseek-v4-flash":{"cost":{"input":0.15,"output":0.6}}}}}`
		if request.URL.String() == currencyRateURL {
			body = `{"date":"2026-10-30","base":"USD","quote":"CNY","rate":6.6572}`
		}
		if version.Load() == 1 {
			body = strings.ReplaceAll(body, "0.15", "0.3")
			body = strings.ReplaceAll(body, "6.6572", "7.0")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	service := newPricingReferenceServiceWithCache(dir, 12*time.Hour, 24*time.Hour)
	service.now = func() time.Time { return now }
	service.client = client
	reference, err := service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || reference.Rates.Input != "998580" {
		t.Fatalf("初始价格错误：%+v/%v", reference, err)
	}
	version.Store(1)
	status, err := service.Refresh(context.Background())
	if err != nil || status.CatalogTTLHours != 12 || status.ExchangeTTLHours != 24 {
		t.Fatalf("强制刷新失败：%+v/%v", status, err)
	}
	reference, err = service.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || reference.Rates.Input != "2100000" || reference.ExchangeRate != "7.0" {
		t.Fatalf("刷新后价格未更新：%+v/%v", reference, err)
	}
	restarted := newPricingReferenceServiceWithCache(dir, 12*time.Hour, 24*time.Hour)
	restarted.now = func() time.Time { return now.Add(time.Minute) }
	restarted.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("刷新后的有效缓存仍发起网络请求")
		return nil, errors.New("不应调用")
	})}
	reference, err = restarted.Lookup(context.Background(), "deepseek", "deepseek-v4-flash")
	if err != nil || reference.Rates.Input != "2100000" {
		t.Fatalf("重启后没有读取刷新后的缓存：%+v/%v", reference, err)
	}
}

func TestUSDPerMillionToMicrosRoundsPrecisely(t *testing.T) {
	value, err := usdPerMillionToMicros("0.0000005", mustRational(t, "1"))
	if err != nil || value != "1" {
		t.Fatalf("半微元应按最近整数舍入：%q/%v", value, err)
	}
}

func mustRational(t *testing.T, raw string) *big.Rat {
	t.Helper()
	value, ok := new(big.Rat).SetString(raw)
	if !ok {
		t.Fatal("测试有理数无效")
	}
	return value
}
