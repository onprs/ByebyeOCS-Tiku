package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/OCS-Tiku/internal/engine"
	"github.com/onprs/OCS-Tiku/internal/platform/answer"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

const testToken = "fork-test-service-token-0123456789abcdef"

type fakeLLM struct {
	server          *httptest.Server
	mu              sync.Mutex
	mode            string
	answerCalls     int
	captionCalls    int
	lastImages      int
	lastAuth        string
	lastShortAnswer bool
	lastMaxTokens   int
}

func (f *fakeLLM) setMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
}

func (f *fakeLLM) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answerCalls, f.captionCalls
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
		return
	}
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	mode := f.mode
	images := 0
	shortAnswer := false
	for _, msg := range body.Messages {
		images += bytes.Count(msg.Content, []byte(`"type":"image_url"`))
		if bytes.Contains(msg.Content, []byte("简答题")) {
			shortAnswer = true
		}
	}
	if strings.Contains(body.Model, "caption") {
		f.captionCalls++
	} else {
		f.answerCalls++
		f.lastImages = images
		f.lastAuth = r.Header.Get("Authorization")
		f.lastShortAnswer = shortAnswer
		f.lastMaxTokens = body.MaxTokens
	}
	f.mu.Unlock()
	if mode == "delay" {
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	if mode == "block" {
		<-r.Context().Done()
		return
	}
	if mode == "rate_limited" {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if mode == "leaky_error" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"api_key=sk-live-secret-123456 question=敏感题干 https://internal.example/path"}}`))
		return
	}
	content := "<answer>A</answer>"
	if strings.Contains(body.Model, "caption") {
		content = "图片中的文字"
	}
	if shortAnswer {
		content = "<answer>光合作用需要光能、水和叶绿体。\n温度也会影响。</answer>"
	}
	if mode == "empty" {
		content = ""
	}
	response := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	}
	if mode != "missing_usage" {
		usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
		if mode == "missing_completion" {
			delete(usage, "completion_tokens")
		}
		if mode == "bad_total" {
			usage["total_tokens"] = 1
		}
		response["usage"] = usage
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

type fixtureOptions struct {
	mode           string
	vision         bool
	caption        bool
	shared         bool
	timeoutSeconds int
}

type fixture struct {
	handler http.Handler
	cfg     *config.Config
	store   *store.Store
	llm     *fakeLLM
	pool    *pgxpool.Pool
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	llm := &fakeLLM{mode: opts.mode}
	llm.server = httptest.NewTLSServer(llm)
	t.Cleanup(llm.server.Close)
	caPath := filepath.Join(t.TempDir(), "upstream-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: llm.server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen: "127.0.0.1:0", DatabaseDSNFile: "unused", ServiceTokenFile: "unused",
		UpstreamCAFile: caPath,
		DefaultModelID: "standard", AllowLoopbackUpstreams: true,
		PromptVersion: "test-p1", NormalizationVersion: "test-n1",
		Limits: config.Limits{
			MaxRequestBytes: 8 << 20, MaxImages: 8, MaxImageBytes: 1 << 20, MaxImagePixels: 4_000_000,
			MaxOutputTokens: 256, Concurrency: 2, TimeoutSeconds: 5, IdempotencyRetentionHours: 168,
		},
		Cache: config.Cache{TTLDays: 7, MaxTTLDays: 30, SharedEnabled: opts.shared},
		Models: []config.Model{
			{ID: "standard", DisplayName: "测试模型", Protocol: engine.ProtocolOpenAIChat, BaseURL: llm.server.URL + "/v1", Model: "fake-answer", APIKeyFile: "unused", Vision: opts.vision, ToolCall: true, Context: 128000},
			{ID: "caption-model", DisplayName: "转述模型", Protocol: engine.ProtocolOpenAIChat, BaseURL: llm.server.URL + "/v1", Model: "fake-caption", APIKeyFile: "unused", Vision: true},
		},
	}
	if opts.caption {
		cfg.CaptionModelID = "caption-model"
	}
	if opts.timeoutSeconds > 0 {
		cfg.Limits.TimeoutSeconds = opts.timeoutSeconds
	}
	pool := testPool(t)
	st := store.New(pool)
	client, err := modelclient.NewWithOptions(modelclient.Options{AllowLoopback: true, CAFile: caPath, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	svc := answer.New(cfg, st, map[string]string{"standard": "test-api-key", "caption-model": "test-api-key"}, client)
	return &fixture{handler: New(cfg, svc, st, testToken, nil), cfg: cfg, store: st, llm: llm, pool: pool}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	path := os.Getenv("BYEBYEOCS_TIKU_TEST_DATABASE_DSN_FILE")
	if path == "" {
		path = os.Getenv("BYEBYEOCS_TEST_DATABASE_DSN_FILE")
	}
	if path == "" {
		t.Skip("未提供隔离 PostgreSQL 连接文件，跳过 fork 集成测试")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("无法读取测试数据库连接文件")
	}
	dsn := strings.TrimSpace(string(data))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("无法连接隔离测试数据库")
	}
	name := "tiku_it_" + strings.ReplaceAll(store.NewUUID(), "-", "")[:16]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatalf("无法创建临时数据库，测试角色需要 CREATEDB")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("清理临时数据库失败")
		}
		admin.Close(cleanupCtx)
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("测试数据库连接串无效")
	}
	parsed.Path = "/" + name
	parsed.RawPath = ""
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatalf("无法连接临时数据库")
	}
	t.Cleanup(pool.Close)
	if _, err := store.New(pool).Migrate(ctx); err != nil {
		t.Fatalf("临时数据库迁移失败: %v", err)
	}
	return pool
}

func v2Request(title, options, mode, requestID, idemKey string, images []string) answer.Request {
	return answer.Request{
		RequestID: requestID, ModelOfferingID: "standard", Mode: mode,
		CacheScope: "user", CacheScopeKey: "user-scope-key-001",
		Question: answer.QuestionInput{Title: title, Options: options, Type: "single", Images: images},
		Budget:   answer.Budget{MaxOutputTokens: 128},
	}
}

func postJSON(t *testing.T, handler http.Handler, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func authHeaders(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + testToken, "Idempotency-Key": key}
}

func answerV2(t *testing.T, f *fixture, body answer.Request, key string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, f.handler, "/api/v2/answers", body, authHeaders(key))
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) answer.Response {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response answer.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应不是有效 JSON：%v", err)
	}
	return response
}

func decodeErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误响应无效：%v", err)
	}
	return body.Error.Code
}

func TestLegacySearchCompatibility(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	recorder := postJSON(t, f.handler, "/api/search", map[string]any{
		"title": "1+1=?", "options": "A. 1\nB. 2", "type": "single",
	}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("v1 返回 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["question"] != "1+1=?" || response["answer"] != "A" {
		t.Fatalf("v1 响应字段不兼容：%#v", response)
	}
	probe := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	f.handler.ServeHTTP(probe, req)
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"ok":true`) {
		t.Fatalf("v1 探测失败：%d %s", probe.Code, probe.Body.String())
	}
}

func TestExactCacheAndOptionOrder(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	first := decodeResponse(t, answerV2(t, f, v2Request("题目一", "A. 甲\nB. 乙", "cache_first", "req-first-0001", "idem-first-0001", nil), "idem-first-0001"))
	if first.Status != "answered" || first.Usage.TotalTokens == nil || *first.Usage.TotalTokens != 110 || len(first.Usage.Calls) != 1 {
		t.Fatalf("首次作答事实不符：%#v", first)
	}
	second := decodeResponse(t, answerV2(t, f, v2Request("题目一", "A. 甲\nB. 乙", "cache_first", "req-second-0002", "idem-second-0002", nil), "idem-second-0002"))
	if second.Status != "cache_hit" || !second.Cache.Hit || second.Cache.EntryID == nil || second.Cache.CreatedAt == nil {
		t.Fatalf("精确缓存未命中：%#v", second)
	}
	if second.Usage.TotalTokens == nil || *second.Usage.TotalTokens != 0 || len(second.Usage.Calls) != 0 {
		t.Fatalf("缓存命中 token/calls 必须为 0/空：%#v", second.Usage)
	}
	third := decodeResponse(t, answerV2(t, f, v2Request("题目一", "B. 乙\nA. 甲", "cache_first", "req-third-0003", "idem-third-0003", nil), "idem-third-0003"))
	if third.Status != "answered" || third.Cache.Hit {
		t.Fatalf("选项顺序不同不应命中缓存：%#v", third)
	}
	answers, _ := f.llm.counts()
	if answers != 2 {
		t.Fatalf("期望 2 次回答调用，实际 %d", answers)
	}
}

func TestUpstreamErrorDoesNotLeak(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "leaky_error", vision: true, caption: true})
	recorder := answerV2(t, f, v2Request("错误安全题", "A. 甲", "cache_first", "req-leak-0001", "idem-leak-0001", nil), "idem-leak-0001")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游 500 应映射为 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if decodeErrorCode(t, recorder) != "model_unavailable" {
		t.Fatalf("错误码不稳定: %s", recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, secret := range []string{"sk-live-secret-123456", "敏感题干", "internal.example", "api_key"} {
		if strings.Contains(body, secret) {
			t.Fatalf("v2 错误响应泄露原始上游内容 %q: %s", secret, body)
		}
	}
}

func TestV2UsesSharedEngineOverTLS(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	if f.llm.server.TLS == nil {
		t.Fatal("provider 夹具必须走本地 TLS")
	}
	response := decodeResponse(t, answerV2(t, f, v2Request("共享核心题", "A. 甲\nB. 乙", "cache_first", "req-engine-0001", "idem-engine-0001", nil), "idem-engine-0001"))
	if response.Status != "answered" || response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 110 {
		t.Fatalf("v2 未取得共享核心返回的真实 token：%#v", response)
	}
	f.llm.mu.Lock()
	auth := f.llm.lastAuth
	f.llm.mu.Unlock()
	if auth != "Bearer test-api-key" {
		t.Fatalf("v2 未通过共享核心发送认证头：%q", auth)
	}
}

func TestShortAnswerKeepsFullContentAndCaches(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	want := "光合作用需要光能、水和叶绿体。\n温度也会影响。"
	request := func(requestID, idemKey string) answer.Request {
		body := v2Request("简述光合作用的条件。", "", "cache_first", requestID, idemKey, nil)
		body.Question.Type = "shortanswer"
		return body
	}
	first := decodeResponse(t, answerV2(t, f, request("req-short-0001", "idem-short-0001"), "idem-short-0001"))
	if first.Status != "answered" || first.Answer != want {
		t.Fatalf("简答题应保留完整内容：%#v", first)
	}
	f.llm.mu.Lock()
	sawShortAnswer := f.llm.lastShortAnswer
	f.llm.mu.Unlock()
	if !sawShortAnswer {
		t.Fatal("模型提示词缺少简答题题型标签")
	}
	second := decodeResponse(t, answerV2(t, f, request("req-short-0002", "idem-short-0002"), "idem-short-0002"))
	if second.Status != "cache_hit" || second.Answer != want || second.Usage.TotalTokens == nil || *second.Usage.TotalTokens != 0 {
		t.Fatalf("简答题精确缓存异常：%#v", second)
	}
	answers, _ := f.llm.counts()
	if answers != 1 {
		t.Fatalf("简答题缓存命中不应再次调用模型，实际 %d", answers)
	}
}

func TestSimilarTitleDoesNotUseCache(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	_ = decodeResponse(t, answerV2(t, f, v2Request("下列说法正确的是", "A. 甲\nB. 乙", "cache_first", "req-near-0001", "idem-near-0001", nil), "idem-near-0001"))
	near := decodeResponse(t, answerV2(t, f, v2Request("下列说法正确的是？", "A. 甲\nB. 乙", "cache_first", "req-near-0002", "idem-near-0002", nil), "idem-near-0002"))
	if near.Status != "answered" || near.Cache.Hit {
		t.Fatalf("相似但不完全相同的题目不得伪装成缓存命中：%#v", near)
	}
	answers, _ := f.llm.counts()
	if answers != 2 {
		t.Fatalf("相似题应再次调用模型，实际 %d", answers)
	}
}

func TestCaptionCallDecomposition(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: false, caption: true})
	imageURL := testPNG(t)
	response := decodeResponse(t, answerV2(t, f, v2Request("图片题[图片1]", "A. 甲\nB. 乙", "cache_first", "req-caption-0001", "idem-caption-0001", []string{imageURL}), "idem-caption-0001"))
	if response.Status != "answered" || len(response.Usage.Calls) != 2 {
		t.Fatalf("图片转述与回答应分别记录调用：%#v", response)
	}
	if response.Usage.Calls[0].Slot != "caption" || response.Usage.Calls[1].Slot != "answer" {
		t.Fatalf("调用顺序或槽位不符：%#v", response.Usage.Calls)
	}
	if response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 220 {
		t.Fatalf("转述与回答 token 应合计：%#v", response.Usage)
	}
	answers, captions := f.llm.counts()
	if answers != 1 || captions != 1 {
		t.Fatalf("转述/回答调用次数不符：%d/%d", captions, answers)
	}
}

func TestAlwaysLLMSkipsCacheAndWritesCache(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	_ = decodeResponse(t, answerV2(t, f, v2Request("缓存题", "A. 甲\nB. 乙", "cache_first", "req-cache-0001", "idem-cache-0001", nil), "idem-cache-0001"))
	always := decodeResponse(t, answerV2(t, f, v2Request("缓存题", "A. 甲\nB. 乙", "always_llm", "req-always-0002", "idem-always-0002", nil), "idem-always-0002"))
	if always.Status != "answered" || always.Cache.Hit || always.Usage.TotalTokens == nil || *always.Usage.TotalTokens != 110 {
		t.Fatalf("always_llm 不应读取缓存：%#v", always)
	}
	hit := decodeResponse(t, answerV2(t, f, v2Request("缓存题", "A. 甲\nB. 乙", "cache_first", "req-after-0003", "idem-after-0003", nil), "idem-after-0003"))
	if hit.Status != "cache_hit" {
		t.Fatalf("always_llm 成功结果应可供后续缓存命中：%#v", hit)
	}
	answers, _ := f.llm.counts()
	if answers != 2 {
		t.Fatalf("期望 2 次模型调用，实际 %d", answers)
	}
}

func TestAlwaysLLMTwiceThenCacheHit(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	request := func(requestID, idemKey string) answer.Request {
		return v2Request("连续模型题", "A. 甲\nB. 乙", "always_llm", requestID, idemKey, nil)
	}
	first := decodeResponse(t, answerV2(t, f, request("req-always-twice-0001", "idem-always-twice-0001"), "idem-always-twice-0001"))
	second := decodeResponse(t, answerV2(t, f, request("req-always-twice-0002", "idem-always-twice-0002"), "idem-always-twice-0002"))
	for index, response := range []answer.Response{first, second} {
		if response.Status != "answered" || response.Cache.Hit || response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 110 {
			t.Fatalf("第 %d 次 always_llm 应真实调用模型：%#v", index+1, response)
		}
	}
	answers, _ := f.llm.counts()
	if answers != 2 {
		t.Fatalf("连续两次 always_llm 应产生两次模型调用，实际 %d", answers)
	}
	hit := decodeResponse(t, answerV2(t, f, v2Request("连续模型题", "A. 甲\nB. 乙", "cache_first", "req-always-twice-0003", "idem-always-twice-0003", nil), "idem-always-twice-0003"))
	if hit.Status != "cache_hit" || !hit.Cache.Hit || hit.Usage.TotalTokens == nil || *hit.Usage.TotalTokens != 0 || len(hit.Usage.Calls) != 0 {
		t.Fatalf("后续 cache_first 应命中且 token 为 0：%#v", hit)
	}
	answers, _ = f.llm.counts()
	if answers != 2 {
		t.Fatalf("缓存命中不应再次调用模型，实际 %d", answers)
	}
}

func TestIdempotencySameConflictAndConcurrent(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	request := v2Request("幂等题", "A. 甲\nB. 乙", "cache_first", "req-idem-0001", "idem-key-0001", nil)
	first := decodeResponse(t, answerV2(t, f, request, "idem-key-0001"))
	second := decodeResponse(t, answerV2(t, f, request, "idem-key-0001"))
	if first.Answer != second.Answer || second.RequestID != request.RequestID {
		t.Fatalf("相同幂等请求应返回同一结果")
	}
	answers, _ := f.llm.counts()
	if answers != 1 {
		t.Fatalf("幂等重放不应重复调用模型，实际 %d", answers)
	}
	conflict := answerV2(t, f, v2Request("不同题目", "A. 甲\nB. 乙", "cache_first", "req-idem-0001", "idem-key-0001", nil), "idem-key-0001")
	if conflict.Code != http.StatusConflict || decodeErrorCode(t, conflict) != "idempotency_conflict" {
		t.Fatalf("不同摘要应返回 409：%d %s", conflict.Code, conflict.Body.String())
	}
	f.llm.setMode("delay")
	concurrent := v2Request("并发题", "A. 甲\nB. 乙", "cache_first", "req-concurrent-0001", "idem-concurrent-0001", nil)
	var wait sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			recorder := answerV2(t, f, concurrent, "idem-concurrent-0001")
			codes[index] = recorder.Code
		}(i)
	}
	wait.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("并发同请求应都返回 200，实际 %v", codes)
	}
	f.llm.mu.Lock()
	after := f.llm.answerCalls
	f.llm.mu.Unlock()
	if after != 2 {
		t.Fatalf("并发同请求只应新增一次模型调用，累计 %d", after)
	}
}

func TestModelDisabledNoFallback(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	if err := f.store.SetModelDisabled(context.Background(), "standard", true); err != nil {
		t.Fatal(err)
	}
	recorder := answerV2(t, f, v2Request("下架题", "A. 甲\nB. 乙", "cache_first", "req-disabled-0001", "idem-disabled-0001", nil), "idem-disabled-0001")
	if recorder.Code != http.StatusConflict || decodeErrorCode(t, recorder) != "model_disabled" {
		t.Fatalf("模型下架应拒绝且不切换：%d %s", recorder.Code, recorder.Body.String())
	}
	answers, _ := f.llm.counts()
	if answers != 0 {
		t.Fatalf("下架模型不应产生调用，实际 %d", answers)
	}
	modelsReq := httptest.NewRequest(http.MethodGet, "/internal/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+testToken)
	models := httptest.NewRecorder()
	f.handler.ServeHTTP(models, modelsReq)
	if strings.Contains(models.Body.String(), "fake-answer") || strings.Contains(models.Body.String(), "test-api-key") {
		t.Fatal("内部模型列表不得泄露上游模型名或密钥")
	}
}

func TestUsageIncompleteIsNotCached(t *testing.T) {
	for _, mode := range []string{"missing_usage", "missing_completion", "bad_total"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{mode: mode, vision: true, caption: true})
			first := decodeResponse(t, answerV2(t, f, v2Request("用量题", "A. 甲\nB. 乙", "cache_first", "req-usage-0001", "idem-usage-0001", nil), "idem-usage-0001"))
			if first.Status != "usage_incomplete" || first.Usage.Complete || first.Usage.TotalTokens != nil {
				t.Fatalf("用量缺失必须标记不完整且不补 0：%#v", first)
			}
			if first.Answer != "A" || len(first.Usage.Calls) != 1 {
				t.Fatalf("应保留答案与调用事实：%#v", first)
			}
			second := decodeResponse(t, answerV2(t, f, v2Request("用量题", "A. 甲\nB. 乙", "cache_first", "req-usage-0002", "idem-usage-0002", nil), "idem-usage-0002"))
			if second.Cache.Hit {
				t.Fatalf("用量不完整结果不得写入缓存")
			}
			answers, _ := f.llm.counts()
			if answers != 2 {
				t.Fatalf("未缓存应再次调用模型，实际 %d", answers)
			}
		})
	}
}

func TestImagesBudgetAndVisionFallback(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	imageURL := testPNG(t)
	imgReq := v2Request("看图[图片1]", "A. 甲\nB. 乙", "cache_first", "req-image-0001", "idem-image-0001", []string{imageURL})
	img := decodeResponse(t, answerV2(t, f, imgReq, "idem-image-0001"))
	if img.Status != "answered" {
		t.Fatalf("视觉模型应直接作答：%#v", img)
	}
	f.llm.mu.Lock()
	if f.llm.lastImages != 1 {
		t.Fatalf("视觉模型应收到 1 张图片，实际 %d", f.llm.lastImages)
	}
	f.llm.mu.Unlock()

	textOnly := newFixture(t, fixtureOptions{vision: false, caption: false})
	unsupported := answerV2(t, textOnly, v2Request("看图[图片1]", "A. 甲", "cache_first", "req-image-0002", "idem-image-0002", []string{imageURL}), "idem-image-0002")
	if unsupported.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, unsupported) != "vision_unsupported" {
		t.Fatalf("无视觉能力且无转述模型应拒绝：%d %s", unsupported.Code, unsupported.Body.String())
	}
	overBudget := v2Request("预算题", "A. 甲", "cache_first", "req-budget-0001", "idem-budget-0001", nil)
	overBudget.Budget.MaxOutputTokens = 999
	budgetResp := answerV2(t, f, overBudget, "idem-budget-0001")
	if budgetResp.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, budgetResp) != "budget_exceeded" {
		t.Fatalf("超预算应拒绝：%d %s", budgetResp.Code, budgetResp.Body.String())
	}
	images := make([]string, 9)
	title := strings.Builder{}
	for i := range images {
		images[i] = imageURL
		fmt.Fprintf(&title, "[图片%d]", i+1)
	}
	tooMany := v2Request(title.String(), "A. 甲", "cache_first", "req-images-0001", "idem-images-0001", images)
	manyResp := answerV2(t, f, tooMany, "idem-images-0001")
	if manyResp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("超过图片数量上限应拒绝：%d", manyResp.Code)
	}
	badImage := v2Request("坏图[图片1]", "A. 甲", "cache_first", "req-bad-0001", "idem-bad-0001", []string{"data:image/png;base64,not-base64"})
	badResp := answerV2(t, f, badImage, "idem-bad-0001")
	if badResp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 Base64 图片应拒绝：%d", badResp.Code)
	}
}

func TestCacheInvalidationKeepsEvidence(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	_ = decodeResponse(t, answerV2(t, f, v2Request("失效题", "A. 甲\nB. 乙", "cache_first", "req-inv-0001", "idem-inv-0001", nil), "idem-inv-0001"))
	hit := decodeResponse(t, answerV2(t, f, v2Request("失效题", "A. 甲\nB. 乙", "cache_first", "req-inv-0002", "idem-inv-0002", nil), "idem-inv-0002"))
	if hit.Cache.EntryID == nil {
		t.Fatal("缓存命中应返回条目 ID")
	}
	entryID := *hit.Cache.EntryID
	recorder := postJSON(t, f.handler, "/internal/v1/cache/invalidations", map[string]any{
		"entry_id": entryID, "reason": "用户报告答案错误", "actor": "admin:test",
	}, map[string]string{"Authorization": "Bearer " + testToken})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"invalidated":true`) {
		t.Fatalf("缓存失效失败：%d %s", recorder.Code, recorder.Body.String())
	}
	after := decodeResponse(t, answerV2(t, f, v2Request("失效题", "A. 甲\nB. 乙", "cache_first", "req-inv-0003", "idem-inv-0003", nil), "idem-inv-0003"))
	if after.Cache.Hit || after.Status != "answered" {
		t.Fatalf("失效后不应命中缓存：%#v", after)
	}
	var reason string
	var invalidatedAt *time.Time
	err := f.pool.QueryRow(context.Background(), `SELECT invalidation_reason, invalidated_at FROM tiku.cache_entries WHERE id = $1`, entryID).Scan(&reason, &invalidatedAt)
	if err != nil || reason == "" || invalidatedAt == nil {
		t.Fatalf("失效应保留最小证据：%v", err)
	}
	answers, _ := f.llm.counts()
	if answers != 2 {
		t.Fatalf("失效后应重新调用模型，实际 %d", answers)
	}
}

func TestModelTestsAndAuthorization(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	unauthorized := postJSON(t, f.handler, "/api/v2/answers", v2Request("未授权", "A. 甲", "cache_first", "req-auth-0001", "idem-auth-0001", nil), map[string]string{"Idempotency-Key": "idem-auth-0001"})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("缺少服务令牌应 401，实际 %d", unauthorized.Code)
	}
	noConfirm := postJSON(t, f.handler, "/internal/v1/models/standard/tests", map[string]any{"kind": "functional"}, map[string]string{"Authorization": "Bearer " + testToken})
	if noConfirm.Code != http.StatusPreconditionRequired || decodeErrorCode(t, noConfirm) != "cost_confirmation_required" {
		t.Fatalf("功能性测试必须显式确认成本：%d %s", noConfirm.Code, noConfirm.Body.String())
	}
	functional := postJSON(t, f.handler, "/internal/v1/models/standard/tests", map[string]any{"kind": "functional", "confirm_cost": true}, map[string]string{"Authorization": "Bearer " + testToken})
	if functional.Code != http.StatusOK || !strings.Contains(functional.Body.String(), `"functional":true`) {
		t.Fatalf("功能性测试失败：%d %s", functional.Code, functional.Body.String())
	}
	reachability := postJSON(t, f.handler, "/internal/v1/models/standard/tests", map[string]any{"kind": "reachability"}, map[string]string{"Authorization": "Bearer " + testToken})
	if reachability.Code != http.StatusOK || !strings.Contains(reachability.Body.String(), `"reachable":true`) {
		t.Fatalf("可达性测试失败：%d %s", reachability.Code, reachability.Body.String())
	}
}

func TestTimeoutLeavesUnknownFactWithoutReplay(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "block", vision: true, caption: true, timeoutSeconds: 1})
	body := v2Request("超时题", "A. 甲", "cache_first", "req-timeout-0001", "idem-timeout-0001", nil)
	first := answerV2(t, f, body, "idem-timeout-0001")
	if first.Code != http.StatusGatewayTimeout || decodeErrorCode(t, first) != "upstream_timeout" {
		t.Fatalf("超时应返回 upstream_timeout：%d %s", first.Code, first.Body.String())
	}
	second := answerV2(t, f, body, "idem-timeout-0001")
	if second.Code != http.StatusConflict || decodeErrorCode(t, second) != "request_unknown" {
		t.Fatalf("未知结果同键重试不得再次调用：%d %s", second.Code, second.Body.String())
	}
	answers, _ := f.llm.counts()
	if answers != 1 {
		t.Fatalf("超时同键重试不得重复调用模型，实际 %d", answers)
	}
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM tiku.requests WHERE idempotency_key = $1`, "idem-timeout-0001").Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("应保留 unknown 幂等事实：%s %v", state, err)
	}
}

// 缓存只是优化：写入失败不能把已成功的模型作答变成 500 或丢失幂等结果。
func TestCacheWriteFailureDoesNotFailAnswer(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION block_cache_write() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			RAISE EXCEPTION 'blocked cache write';
		END $body$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `CREATE TRIGGER block_cache_write BEFORE INSERT ON tiku.cache_entries
		FOR EACH ROW EXECUTE FUNCTION block_cache_write()`); err != nil {
		t.Fatal(err)
	}
	body := v2Request("缓存写入失败题", "A. 甲", "cache_first", "req-cache-fail-0001", "idem-cache-fail-0001", nil)
	first := decodeResponse(t, answerV2(t, f, body, "idem-cache-fail-0001"))
	if first.Status != "answered" || first.Answer != "A" || !first.Usage.Complete {
		t.Fatalf("缓存写入失败不应影响作答：%#v", first)
	}
	replay := decodeResponse(t, answerV2(t, f, body, "idem-cache-fail-0001"))
	if replay.RequestID != first.RequestID || replay.Answer != first.Answer {
		t.Fatalf("幂等结果必须可重放：%#v", replay)
	}
	if answers, _ := f.llm.counts(); answers != 1 {
		t.Fatalf("缓存写入失败不应导致重复调用模型，实际 %d", answers)
	}
	var entries int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tiku.cache_entries`).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("缓存写入失败时不应留下条目：%d/%v", entries, err)
	}
}

// 进程在调用中途崩溃会留下 pending 幂等记录；超过上游调用时限后同键重试必须
// 返回结果不明，而不是一直等待并反复查询数据库。
func TestStalePendingReplayReturnsUnknown(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	body := v2Request("崩溃恢复题", "A. 甲", "cache_first", "req-crash-0001", "idem-crash-0001", nil)
	_ = decodeResponse(t, answerV2(t, f, body, "idem-crash-0001"))
	if _, err := f.pool.Exec(context.Background(), `UPDATE tiku.requests SET state = 'pending', response = NULL,
		error_code = NULL, completed_at = NULL, created_at = now() - interval '10 minutes'
		WHERE idempotency_key = $1`, "idem-crash-0001"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	recorder := answerV2(t, f, body, "idem-crash-0001")
	if recorder.Code != http.StatusConflict || decodeErrorCode(t, recorder) != "request_unknown" {
		t.Fatalf("陈旧 pending 应返回 request_unknown：%d %s", recorder.Code, recorder.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("陈旧 pending 不应长时间轮询：%v", elapsed)
	}
	if answers, _ := f.llm.counts(); answers != 1 {
		t.Fatalf("陈旧 pending 重试不得再次调用模型，实际 %d", answers)
	}
}

func TestSharedCacheRequiresExplicitEnable(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true, shared: false})
	request := v2Request("共享题", "A. 甲", "cache_first", "req-shared-0001", "idem-shared-0001", nil)
	request.CacheScope = "shared"
	request.CacheScopeKey = ""
	recorder := answerV2(t, f, request, "idem-shared-0001")
	if recorder.Code != http.StatusConflict || decodeErrorCode(t, recorder) != "shared_cache_disabled" {
		t.Fatalf("共享缓存未开启应拒绝：%d %s", recorder.Code, recorder.Body.String())
	}
}

// 保留清理：过期缓存/答案/调用明细被清理，幂等键与 token 汇总保留，旧键不会再次付费。
func TestCleanupRetainsIdempotencyAndTokenTotals(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true})
	ctx := context.Background()
	oldBody := v2Request("旧清理题", "A. 甲", "cache_first", "req-clean-old-0001", "idem-clean-old-0001", nil)
	_ = decodeResponse(t, answerV2(t, f, oldBody, "idem-clean-old-0001"))
	contentBody := v2Request("到期内容题", "A. 甲", "cache_first", "req-clean-content", "idem-clean-content", nil)
	_ = decodeResponse(t, answerV2(t, f, contentBody, "idem-clean-content"))
	freshBody := v2Request("新清理题", "A. 甲", "cache_first", "req-clean-new-0001", "idem-clean-new-0001", nil)
	_ = decodeResponse(t, answerV2(t, f, freshBody, "idem-clean-new-0001"))
	if created, _, err := f.store.BeginRequest(ctx, "idem-clean-pending", "req-clean-pending", strings.Repeat("a", 64)); err != nil || !created {
		t.Fatalf("构造 pending 失败：%v/%v", created, err)
	}
	if created, _, err := f.store.BeginRequest(ctx, "idem-clean-failed", "req-clean-failed", strings.Repeat("b", 64)); err != nil || !created {
		t.Fatalf("构造 failed 失败：%v/%v", created, err)
	}
	if err := f.store.FailRequest(ctx, "idem-clean-failed", "invalid_question"); err != nil {
		t.Fatal(err)
	}
	// 旧时间戳夹具，不等待真实天数。
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE tiku.requests SET created_at = now() - interval '40 days'
		WHERE idempotency_key IN ('idem-clean-old-0001', 'idem-clean-pending', 'idem-clean-failed')`)
	exec(`UPDATE tiku.queries SET created_at = now() - interval '40 days'
		WHERE idempotency_key = 'idem-clean-old-0001'`)
	exec(`UPDATE tiku.model_calls SET created_at = now() - interval '40 days'
		WHERE request_id = 'req-clean-old-0001'`)
	exec(`UPDATE tiku.cache_entries SET expires_at = now() - interval '40 days'
		WHERE question_title = '旧清理题'`)
	// 内容按7天到期；调用明细按30天处理，缓存到期后无需额外保留原文。
	exec(`UPDATE tiku.requests SET created_at = now() - interval '8 days' WHERE idempotency_key = 'idem-clean-content'`)
	exec(`UPDATE tiku.queries SET created_at = now() - interval '8 days' WHERE idempotency_key = 'idem-clean-content'`)
	exec(`UPDATE tiku.model_calls SET created_at = now() - interval '8 days' WHERE request_id = 'req-clean-content'`)
	exec(`UPDATE tiku.cache_entries SET expires_at = now() - interval '1 hour' WHERE question_title = '到期内容题'`)

	result, err := f.store.Cleanup(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheEntries != 2 || result.RequestPayloads != 2 || result.QueryAnswers != 2 || result.ModelCalls != 1 {
		t.Fatalf("清理计数不符：%#v", result)
	}

	var key, requestID, fingerprint, state string
	var response []byte
	if err := f.pool.QueryRow(ctx, `SELECT idempotency_key, request_id, fingerprint, state, response
		FROM tiku.requests WHERE idempotency_key = 'idem-clean-old-0001'`).
		Scan(&key, &requestID, &fingerprint, &state, &response); err != nil {
		t.Fatal(err)
	}
	if state != "expired" || len(response) != 0 || key != "idem-clean-old-0001" ||
		requestID != "req-clean-old-0001" || len(fingerprint) != 64 {
		t.Fatalf("过期幂等事实未正确保留：%s %q %q %q %q", state, key, requestID, fingerprint, response)
	}

	answersBefore, _ := f.llm.counts()
	replay := answerV2(t, f, oldBody, "idem-clean-old-0001")
	if replay.Code != http.StatusConflict || decodeErrorCode(t, replay) != "request_unknown" {
		t.Fatalf("旧键不得重新调用：%d %s", replay.Code, replay.Body.String())
	}
	answersAfter, _ := f.llm.counts()
	if answersAfter != answersBefore {
		t.Fatalf("旧键重试不得再次调用模型：%d/%d", answersBefore, answersAfter)
	}

	var answerText *string
	var usage []byte
	if err := f.pool.QueryRow(ctx, `SELECT answer, usage FROM tiku.queries
		WHERE request_id = 'req-clean-old-0001'`).Scan(&answerText, &usage); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		TotalTokens *int64 `json:"total_tokens"`
		Complete    bool   `json:"complete"`
	}
	if err := json.Unmarshal(usage, &parsed); err != nil {
		t.Fatal(err)
	}
	if answerText != nil || parsed.TotalTokens == nil || *parsed.TotalTokens != 110 || !parsed.Complete {
		t.Fatalf("答案应清理而 token 汇总保留：%v %#v", answerText, parsed)
	}

	var pendingState, failedState, failedCode string
	if err := f.pool.QueryRow(ctx, `SELECT state FROM tiku.requests WHERE idempotency_key = 'idem-clean-pending'`).Scan(&pendingState); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT state, COALESCE(error_code, '') FROM tiku.requests
		WHERE idempotency_key = 'idem-clean-failed'`).Scan(&failedState, &failedCode); err != nil {
		t.Fatal(err)
	}
	if pendingState != "pending" || failedState != "failed" || failedCode != "invalid_question" {
		t.Fatalf("pending/failed 最小事实必须保留：%s/%s/%s", pendingState, failedState, failedCode)
	}

	var cacheCount, callCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tiku.cache_entries`).Scan(&cacheCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tiku.model_calls`).Scan(&callCount); err != nil {
		t.Fatal(err)
	}
	if cacheCount != 1 || callCount != 2 {
		t.Fatalf("缓存原文到期即清理，未满30天调用明细仍保留：%d/%d", cacheCount, callCount)
	}
	var retainedUsage []byte
	if err := f.pool.QueryRow(ctx, `SELECT answer, usage FROM tiku.queries WHERE request_id = 'req-clean-content'`).Scan(&answerText, &retainedUsage); err != nil {
		t.Fatal(err)
	}
	if answerText != nil || len(retainedUsage) == 0 {
		t.Fatal("超过7天的答案应清理并保留token汇总")
	}

	fresh := decodeResponse(t, answerV2(t, f, freshBody, "idem-clean-new-0001"))
	if fresh.Status != "answered" || fresh.Answer != "A" {
		t.Fatalf("未过期结果仍应可重放：%#v", fresh)
	}
}

func testPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes())
}

// fork 配置中不存在的模型属于入口校验失败：使用独立错误码，且不写请求记录、不调用模型，
// 管理员补齐配置后同一 request_id 与幂等键可以直接重试。
func TestV2UnknownModelIsNotConfigured(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	request := v2Request("缺失模型题", "A. 甲\nB. 乙", "cache_first", "req-absent-0001", "idem-absent-0001", nil)
	request.ModelOfferingID = "absent-model"
	recorder := answerV2(t, f, request, "idem-absent-0001")
	if recorder.Code != http.StatusServiceUnavailable || decodeErrorCode(t, recorder) != "model_not_configured" {
		t.Fatalf("未配置模型应明确分类：%d %s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.requests WHERE idempotency_key = $1`,
		"idem-absent-0001").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("入口校验失败不应留下幂等记录：%d", count)
	}
	if answers, _ := f.llm.counts(); answers != 0 {
		t.Fatalf("未配置模型不应调用上游：%d", answers)
	}
}

// 模型停用不再写入终态幂等记录：重新启用后同一幂等键可以完成作答，不重复计费。
func TestModelDisabledLeavesNoIdempotencyRecord(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	if err := f.store.SetModelDisabled(context.Background(), "standard", true); err != nil {
		t.Fatal(err)
	}
	request := v2Request("恢复题", "A. 甲\nB. 乙", "cache_first", "req-resume-0001", "idem-resume-0001", nil)
	disabled := answerV2(t, f, request, "idem-resume-0001")
	if disabled.Code != http.StatusConflict || decodeErrorCode(t, disabled) != "model_disabled" {
		t.Fatalf("停用模型应返回 409：%d %s", disabled.Code, disabled.Body.String())
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.requests WHERE idempotency_key = $1`,
		"idem-resume-0001").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("停用检查发生在写入请求记录前，不应留下幂等记录：%d", count)
	}
	if err := f.store.SetModelDisabled(context.Background(), "standard", false); err != nil {
		t.Fatal(err)
	}
	recovered := decodeResponse(t, answerV2(t, f, request, "idem-resume-0001"))
	if recovered.Status != "answered" {
		t.Fatalf("重新启用后应完成作答：%#v", recovered)
	}
	if answers, _ := f.llm.counts(); answers != 1 {
		t.Fatalf("恢复后只应调用一次上游，实际 %d", answers)
	}
}

// 主平台公开契约允许最长 1000 字的失效原因；中文按字符计数，不能按字节提前拒绝。
func TestCacheInvalidationAcceptsLongReason(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	decodeResponse(t, answerV2(t, f, v2Request("失效长原因题", "A. 甲\nB. 乙", "cache_first", "req-invalidate-0001", "idem-invalidate-0001", nil), "idem-invalidate-0001"))
	var entryID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM tiku.cache_entries WHERE invalidated_at IS NULL`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	recorder := postJSON(t, f.handler, "/internal/v1/cache/invalidations", map[string]any{
		"entry_id": entryID, "reason": strings.Repeat("核对失败原因", 100), "actor": "main-platform",
	}, map[string]string{"Authorization": "Bearer " + testToken})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"invalidated":true`) {
		t.Fatalf("长中文原因应被接受：%d %s", recorder.Code, recorder.Body.String())
	}
}

// 自动做题的平台题干不含 [图片N] 占位标记：图片按 DOM 顺序附加时仍应作答；
// 指向不存在图片的标记继续拒绝。
func TestV2AcceptsImagesWithoutTextMarkers(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	imageURL := testPNG(t)
	response := decodeResponse(t, answerV2(t, f,
		v2Request("平台题干未带占位标记", "A. 甲\nB. 乙", "cache_first", "req-nomarker-0001", "idem-nomarker-0001", []string{imageURL}),
		"idem-nomarker-0001"))
	if response.Status != "answered" {
		t.Fatalf("无占位标记的平台图片题应作答：%#v", response)
	}
	missing := answerV2(t, f,
		v2Request("指向缺失图片[图片2]", "A. 甲", "cache_first", "req-missing-0001", "idem-missing-0001", []string{imageURL}),
		"idem-missing-0001")
	if missing.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, missing) != "invalid_question" {
		t.Fatalf("标记指向不存在图片应拒绝：%d %s", missing.Code, missing.Body.String())
	}
}

// 输出预算只是上限：请求预算高于单个模型配置时按模型上限收敛作答，
// 不再返回 budget_exceeded；低于请求预算或全局硬上限时不允许放大。
func TestBudgetAboveModelLimitClampsToModelLimit(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	f.cfg.Models[0].MaxOutputTokens = 64
	request := v2Request("低上限模型题", "A. 甲\nB. 乙", "cache_first", "req-budget-clamp-0001", "idem-budget-clamp-0001", nil)
	request.Budget.MaxOutputTokens = 128
	response := decodeResponse(t, answerV2(t, f, request, "idem-budget-clamp-0001"))
	if response.Status != "answered" {
		t.Fatalf("高于模型上限的预算应按上限收敛：%#v", response)
	}
	f.llm.mu.Lock()
	got := f.llm.lastMaxTokens
	f.llm.mu.Unlock()
	if got != 64 {
		t.Fatalf("模型调用应使用模型上限 64，实际 %d", got)
	}

	smaller := v2Request("低预算题", "A. 甲\nB. 乙", "cache_first", "req-budget-small-0001", "idem-budget-small-0001", nil)
	smaller.Budget.MaxOutputTokens = 32
	decodeResponse(t, answerV2(t, f, smaller, "idem-budget-small-0001"))
	f.llm.mu.Lock()
	got = f.llm.lastMaxTokens
	f.llm.mu.Unlock()
	if got != 32 {
		t.Fatalf("请求预算低于模型上限时不能放大，实际 %d", got)
	}
}

// 服务级硬上限仍拒绝：单次请求不能超过部署允许的输出规模。
func TestBudgetAboveServerCapRejected(t *testing.T) {
	f := newFixture(t, fixtureOptions{vision: true, caption: true})
	request := v2Request("超全局预算题", "A. 甲\nB. 乙", "cache_first", "req-budget-cap-0001", "idem-budget-cap-0001", nil)
	request.Budget.MaxOutputTokens = f.cfg.Limits.MaxOutputTokens + 1
	recorder := answerV2(t, f, request, "idem-budget-cap-0001")
	if recorder.Code != http.StatusUnprocessableEntity || decodeErrorCode(t, recorder) != "budget_exceeded" {
		t.Fatalf("超过服务全局上限应拒绝：%d %s", recorder.Code, recorder.Body.String())
	}
	if answers, _ := f.llm.counts(); answers != 0 {
		t.Fatalf("预算被拒绝时不应调用上游：%d", answers)
	}
}
