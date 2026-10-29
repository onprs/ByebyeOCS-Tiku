// Package answer 实现 fork 的 v2 作答、精确缓存、幂等与服务端用量事实。
package answer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
	"github.com/onprs/OCS-Tiku/internal/platform/normalize"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

type QuestionInput struct {
	Title   string   `json:"title"`
	Options string   `json:"options"`
	Type    string   `json:"type"`
	Images  []string `json:"images"`
}

type Budget struct {
	MaxOutputTokens int `json:"max_output_tokens"`
}

type CachePolicy struct {
	Allow   bool `json:"allow"`
	TTLDays int  `json:"ttl_days"`
}

type Request struct {
	RequestID       string        `json:"request_id"`
	ModelOfferingID string        `json:"model_offering_id"`
	Mode            string        `json:"mode"`
	CacheScope      string        `json:"cache_scope"`
	CacheScopeKey   string        `json:"cache_scope_key"`
	Question        QuestionInput `json:"question"`
	Budget          Budget        `json:"budget"`
	CachePolicy     *CachePolicy  `json:"cache_policy,omitempty"`
}

type ModelInfo struct {
	OfferingID   string            `json:"offering_id"`
	DisplayName  string            `json:"display_name"`
	Capabilities []string          `json:"capabilities"`
	Profile      CapabilityProfile `json:"profile"`
}

type CapabilityProfile struct {
	Vision    bool `json:"vision"`
	ToolCall  bool `json:"tool_call"`
	Reasoning bool `json:"reasoning"`
	Context   int  `json:"context"`
}

type CacheInfo struct {
	Hit       bool       `json:"hit"`
	EntryID   *string    `json:"entry_id"`
	CreatedAt *time.Time `json:"created_at"`
}

type Call struct {
	Slot          string `json:"slot"`
	ModelID       string `json:"-"`
	InputTokens   *int64 `json:"input_tokens"`
	OutputTokens  *int64 `json:"output_tokens"`
	TotalTokens   *int64 `json:"total_tokens"`
	UsageComplete bool   `json:"-"`
	Status        string `json:"status"`
	LatencyMS     int64  `json:"latency_ms"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type Usage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
	Complete     bool   `json:"complete"`
	Calls        []Call `json:"calls"`
}

type Response struct {
	RequestID string    `json:"request_id"`
	Status    string    `json:"status"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer,omitempty"`
	Model     ModelInfo `json:"model"`
	Mode      string    `json:"mode"`
	Cache     CacheInfo `json:"cache"`
	Usage     Usage     `json:"usage"`
	LatencyMS int64     `json:"latency_ms"`
}

type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	Retryable  bool
}

func (e *Error) Error() string { return e.Message }

func newError(code, message string, status int, retryable bool) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status, Retryable: retryable}
}

func AsError(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return newError("internal_error", "题库服务内部错误", 500, false)
}

type ModelView struct {
	ID           string            `json:"id"`
	DisplayName  string            `json:"display_name"`
	Capabilities []string          `json:"capabilities"`
	Profile      CapabilityProfile `json:"profile"`
	Enabled      bool              `json:"enabled"`
	Health       string            `json:"health"`
}

type TestResult struct {
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Reachable  bool   `json:"reachable"`
	Functional bool   `json:"functional"`
	LatencyMS  int64  `json:"latency_ms"`
	ErrorCode  string `json:"error_code,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
}

type Service struct {
	cfg    *config.Config
	store  *store.Store
	models *models.Registry
	client *modelclient.Client
	sem    chan struct{}
	now    func() time.Time
}

// New 保留既有构造方式，按初始配置与密钥创建不可变快照。
func New(cfg *config.Config, st *store.Store, keys map[string]string, client *modelclient.Client) *Service {
	return NewWithRegistry(cfg, st, models.New(cfg.Models, keys), client)
}

// NewWithRegistry 使用与维护面板共享的模型注册表，支持配置热更新。
func NewWithRegistry(cfg *config.Config, st *store.Store, registry *models.Registry, client *modelclient.Client) *Service {
	registry.SetDefaults(cfg.DefaultModelID, cfg.CaptionModelID)
	return &Service{
		cfg: cfg, store: st, models: registry, client: client,
		sem: make(chan struct{}, cfg.Limits.Concurrency),
		now: time.Now,
	}
}

func (s *Service) Answer(ctx context.Context, req Request, idempotencyKey string) (*Response, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, newError("idempotency_required", "缺少 Idempotency-Key", 400, false)
	}
	if len(req.RequestID) < 6 || len(req.RequestID) > 128 {
		return nil, newError("invalid_question", "request_id 无效", 422, false)
	}
	if req.CacheScope == "" {
		req.CacheScope = "user"
	}
	model, prepared, key, scopeHash, err := s.validate(req)
	if err != nil {
		return nil, err
	}
	model, modelKey, caption, captionKey := s.models.ResolveForAnswer(model.ID, len(prepared.Images) > 0)
	if model == nil {
		return nil, newError("model_not_configured", "模型条目不存在或未配置", 503, false)
	}
	effectiveBudget := model.EffectiveMaxOutputTokens(s.cfg.Limits.MaxOutputTokens)
	if req.Budget.MaxOutputTokens > 0 && req.Budget.MaxOutputTokens < effectiveBudget {
		effectiveBudget = req.Budget.MaxOutputTokens
	}
	key = store.Hash(fmt.Sprintf("%s:config:%s:budget:%d", key, cacheConfiguration(model, modelKey, caption, captionKey), effectiveBudget))
	fingerprint := fingerprint(req, idempotencyKey)
	// 模型停用与声明参数都在写入请求记录前判定：这里既不调用模型，也不留下
	// 终态幂等记录；管理员恢复配置或启用模型后，同一请求键可以重新执行。
	disabled, err := s.store.ModelDisabled(ctx, model.ID)
	if err != nil {
		return nil, newError("internal_error", "无法读取模型状态", 500, false)
	}
	if disabled || model.Disabled {
		return nil, newError("model_disabled", "该模型已下架，请重新选择", 409, false)
	}
	created, record, err := s.store.BeginRequest(ctx, idempotencyKey, req.RequestID, fingerprint)
	if err != nil {
		return nil, newError("internal_error", "无法保存请求幂等记录", 500, false)
	}
	if record == nil {
		return nil, newError("internal_error", "无法读取请求幂等记录", 500, false)
	}
	if !created {
		return s.replay(ctx, record, idempotencyKey, req.RequestID, fingerprint)
	}
	resp, err := s.execute(ctx, req, model, modelKey, caption, captionKey, prepared, key, scopeHash, idempotencyKey)
	if err != nil {
		code := AsError(err).Code
		// 超时/取消可能已产生上游费用：保留 unknown 事实，禁止同键自动重放。
		failCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if code == "upstream_timeout" || code == "request_cancelled" || code == "request_unknown" {
			_ = s.store.MarkUnknown(failCtx, idempotencyKey, code)
		} else {
			_ = s.store.FailRequest(failCtx, idempotencyKey, code)
		}
		cancel()
		return nil, err
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, newError("internal_error", "无法保存响应", 500, false)
	}
	// 模型已成功时要让幂等事实可重放，不能因客户端断开而停在 pending。
	persistCtx, cancelPersist := persistContext(ctx)
	defer cancelPersist()
	if err := s.store.CompleteRequest(persistCtx, idempotencyKey, data); err != nil {
		return nil, newError("internal_error", "无法保存幂等响应", 500, false)
	}
	return resp, nil
}

// persistTimeout 限制已产生上游结果后的写库；不继承已取消的请求上下文，但不能无限等待。
const persistTimeout = 5 * time.Second

// pendingGrace 是崩溃遗留 pending 记录的额外宽限；超过上游调用总时限仍为 pending
// 就视为结果不明，避免同键重试一直阻塞到调用方超时。
const pendingGrace = 30 * time.Second

// persistContext 让缓存、调用事实和幂等记录脱离调用方取消，并仍受有界超时约束。
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (s *Service) pendingStale(record *store.RequestRecord) bool {
	return s.now().Sub(record.CreatedAt) > s.cfg.Timeout()+pendingGrace
}

// cacheConfiguration 只把配置与密钥摘要用于缓存隔离，不保存或返回密钥内容。
func cacheConfiguration(model *config.Model, modelKey string, caption *config.Model, captionKey string) string {
	type profile struct {
		Protocol, Vendor, BaseURL, Model, Key string
		Vision                                bool
		MaxOutputTokens                       int
	}
	makeProfile := func(item *config.Model, key string) *profile {
		if item == nil {
			return nil
		}
		return &profile{Protocol: item.Protocol, Vendor: item.Vendor, BaseURL: item.BaseURL,
			Model: item.Model, Key: store.Hash(key), Vision: item.Vision, MaxOutputTokens: item.MaxOutputTokens}
	}
	data, _ := json.Marshal(struct {
		Model   *profile `json:"model"`
		Caption *profile `json:"caption"`
	}{makeProfile(model, modelKey), makeProfile(caption, captionKey)})
	return store.Hash(string(data))
}

func fingerprint(req Request, key string) string {
	// 幂等键本身进入摘要：同 request_id 配不同键也会被识别为冲突。
	data, _ := json.Marshal(struct {
		Key    string  `json:"key"`
		Reques Request `json:"request"`
	}{Key: key, Reques: req})
	return store.Hash(string(data))
}

func (s *Service) replay(ctx context.Context, record *store.RequestRecord, key, requestID, fingerprint string) (*Response, error) {
	if record.IdempotencyKey != key || record.RequestID != requestID || record.Fingerprint != fingerprint {
		return nil, newError("idempotency_conflict", "相同幂等键或 request_id 对应不同请求", 409, false)
	}
	// 同一请求的并发重试很少见；低频轮询避免在等待期间产生大量数据库查询。
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch record.State {
		case "completed":
			var resp Response
			if err := json.Unmarshal(record.Response, &resp); err != nil {
				return nil, newError("internal_error", "已保存的响应无效", 500, false)
			}
			return &resp, nil
		case "failed":
			return nil, errorFromCode(record.ErrorCode)
		case "unknown":
			return nil, newError("request_unknown", "上游调用结果未知，已保留幂等事实；请人工核对或重新发起新请求", 409, false)
		case "expired":
			// 响应载荷已按保留期清理；键仍保留，不会因旧键再次调用模型。
			return nil, newError("request_unknown", "幂等结果已过期，已保留事实；不会再次调用模型", 409, false)
		case "pending":
			if s.pendingStale(record) {
				// 原请求已超过上游调用时限，不会再写入结果，交人工核对。
				return nil, newError("request_unknown", "上游调用结果未知，已保留幂等事实；请人工核对或重新发起新请求", 409, false)
			}
			select {
			case <-ctx.Done():
				return nil, newError("request_in_progress", "相同请求仍在处理中，请稍后重试", 409, true)
			case <-ticker.C:
			}
			next, err := s.store.FindRequest(ctx, key, requestID)
			if err != nil {
				return nil, newError("internal_error", "无法读取请求状态", 500, false)
			}
			if next == nil {
				return nil, newError("internal_error", "请求状态丢失", 500, false)
			}
			record = next
		default:
			return nil, newError("internal_error", "请求状态无效", 500, false)
		}
	}
}

func errorFromCode(code string) error {
	switch code {
	case "model_disabled":
		return newError(code, "该模型已下架，请重新选择", 409, false)
	case "model_unavailable", "model_auth_failed":
		return newError(code, "模型服务不可用", 503, true)
	case "vision_unsupported":
		return newError(code, "当前模型不支持图片，且未配置图片转述模型", 422, false)
	case "budget_exceeded":
		return newError(code, "请求预算超过服务上限", 422, false)
	case "upstream_timeout":
		return newError(code, "模型调用超时，结果未知", 504, true)
	case "upstream_rate_limited":
		return newError(code, "模型服务限流，请稍后人工重试", 429, true)
	case "request_cancelled":
		return newError(code, "请求已取消，模型结果未知", 499, false)
	case "empty_answer":
		return newError(code, "模型未返回可提交的答案", 502, true)
	case "response_too_large":
		return newError(code, "模型响应超过大小上限", 502, false)
	case "usage_incomplete":
		return newError(code, "模型用量不完整，结果待复核", 502, false)
	default:
		return newError(code, "模型调用失败", 502, false)
	}
}

func (s *Service) validate(req Request) (*config.Model, normalize.Question, string, string, error) {
	if req.CachePolicy != nil {
		if req.CachePolicy.TTLDays < 1 || req.CachePolicy.TTLDays > s.cfg.Cache.MaxTTLDays {
			return nil, normalize.Question{}, "", "", newError("invalid_cache_policy", "缓存期限超过允许范围", 422, false)
		}
		if !req.CachePolicy.Allow && req.Mode == "cache_first" {
			return nil, normalize.Question{}, "", "", newError("cache_not_allowed", "本次策略未允许答案缓存", 422, false)
		}
	}
	if req.Mode != "cache_first" && req.Mode != "always_llm" {
		return nil, normalize.Question{}, "", "", newError("invalid_mode", "作答方式无效", 422, false)
	}
	if req.CacheScope == "" {
		req.CacheScope = "user"
	}
	if req.CacheScope != "user" && req.CacheScope != "shared" {
		return nil, normalize.Question{}, "", "", newError("invalid_scope", "缓存范围无效", 422, false)
	}
	if req.CacheScope == "shared" && !s.cfg.Cache.SharedEnabled {
		return nil, normalize.Question{}, "", "", newError("shared_cache_disabled", "共享缓存尚未启用", 409, false)
	}
	if req.CacheScope == "user" {
		if len(req.CacheScopeKey) < 8 || len(req.CacheScopeKey) > 256 {
			return nil, normalize.Question{}, "", "", newError("invalid_scope_key", "用户缓存范围键无效", 422, false)
		}
	}
	modelID := req.ModelOfferingID
	if modelID == "" {
		modelID = s.models.DefaultID()
	}
	if modelID == "" {
		return nil, normalize.Question{}, "", "", newError("model_unavailable", "未配置可用的默认模型", 503, false)
	}
	model := s.models.Model(modelID)
	if model == nil {
		// 使用独立错误码：这里在写入请求记录前就失败，没有模型调用或费用，
		// 与上游调用阶段的 model_unavailable 区分，调用方可安全重试。
		return nil, normalize.Question{}, "", "", newError("model_not_configured", "模型条目不存在或未配置", 503, false)
	}
	prepared, err := normalize.Prepare(req.Question.Title, req.Question.Options, req.Question.Type, req.Question.Images, normalize.Limits{
		MaxImages: s.cfg.Limits.MaxImages, MaxImageBytes: s.cfg.Limits.MaxImageBytes, MaxImagePixels: s.cfg.Limits.MaxImagePixels,
	})
	if err != nil {
		return nil, normalize.Question{}, "", "", newError("invalid_question", err.Error(), 422, false)
	}
	if req.Budget.MaxOutputTokens < 0 {
		return nil, normalize.Question{}, "", "", newError("budget_invalid", "输出预算无效", 422, false)
	}
	// 输出预算只是上限：高于单个模型配置时由 solve 按 min(模型上限, 预算) 收敛，
	// 不能因此拒绝请求——自动做题固定发送的预算会让低上限模型永远失败。
	// 服务级硬上限仍然拒绝：单次请求不能超过部署允许的输出规模。
	if req.Budget.MaxOutputTokens > s.cfg.Limits.MaxOutputTokens {
		return nil, normalize.Question{}, "", "", newError("budget_exceeded",
			fmt.Sprintf("输出预算不能超过 %d", s.cfg.Limits.MaxOutputTokens), 422, false)
	}
	scopeHash := normalize.ScopeKeyHash(req.CacheScope, req.CacheScopeKey)
	key := normalize.CacheKey(model.ID, s.cfg.PromptVersion, s.cfg.NormalizationVersion,
		req.CacheScope, scopeHash, req.Question.Title, req.Question.Options, prepared.Type, prepared.Images)
	if req.CachePolicy != nil {
		// 不同保留期分开缓存；新策略不会复用旧策略留下的长有效期条目。
		key = store.Hash(fmt.Sprintf("%s:ttl:%d", key, req.CachePolicy.TTLDays))
	}
	return model, prepared, key, scopeHash, nil
}

func (s *Service) execute(ctx context.Context, req Request, model *config.Model, modelKey string, caption *config.Model, captionKey string, prepared normalize.Question, cacheKey, scopeHash, idemKey string) (*Response, error) {
	start := s.now()
	allowCache, ttlDays := true, s.cfg.Cache.TTLDays
	if req.CachePolicy != nil {
		allowCache, ttlDays = req.CachePolicy.Allow, req.CachePolicy.TTLDays
	}
	if req.Mode == "cache_first" && allowCache {
		entry, err := s.store.LookupCache(ctx, cacheKey, start)
		if err != nil {
			return nil, newError("internal_error", "无法读取精确缓存", 500, false)
		}
		if entry != nil && entry.CreatedAt.Add(time.Duration(ttlDays)*24*time.Hour).After(start) {
			zero := int64(0)
			entryCreated := entry.CreatedAt.UTC()
			resp := &Response{
				RequestID: req.RequestID,
				Status:    "cache_hit",
				Question:  prepared.TitleText,
				Answer:    entry.Answer,
				Model:     modelInfo(model),
				Mode:      req.Mode,
				Cache:     CacheInfo{Hit: true, EntryID: &entry.ID, CreatedAt: &entryCreated},
				Usage:     Usage{InputTokens: &zero, OutputTokens: &zero, TotalTokens: &zero, Complete: true, Calls: []Call{}},
				LatencyMS: time.Since(start).Milliseconds(),
			}
			persistCtx, cancelPersist := persistContext(ctx)
			s.recordQuery(persistCtx, req, model.ID, scopeHash, idemKey, resp, entry.ID, entry.CreatedAt)
			cancelPersist()
			return resp, nil
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-callCtx.Done():
		return nil, newError("request_cancelled", "请求已取消，模型尚未调用", 499, false)
	}

	answer, usage, solveErr := s.solve(callCtx, model, modelKey, caption, captionKey, prepared, req.Budget.MaxOutputTokens)
	latency := time.Since(start).Milliseconds()
	// 即使后续调用失败，也保留已经发生的模型调用事实。
	callsCtx, cancelCalls := persistContext(ctx)
	callsErr := s.recordCalls(callsCtx, req.RequestID, usage.Calls)
	cancelCalls()
	if callsErr != nil {
		s.recordUnknownQuery(ctx, req, model.ID, scopeHash, idemKey, usage, latency)
		return nil, newError("request_unknown", "模型调用事实未能完整保存，请核对用量", 409, false)
	}
	if solveErr != nil {
		code := AsError(solveErr).Code
		if code == "upstream_timeout" || code == "request_cancelled" {
			s.recordUnknownQuery(ctx, req, model.ID, scopeHash, idemKey, usage, latency)
			return nil, solveErr
		}
		if code == "model_unavailable" || code == "response_too_large" || code == "empty_answer" {
			s.recordUnknownQuery(ctx, req, model.ID, scopeHash, idemKey, usage, latency)
			return nil, newError("request_unknown", "模型调用可能已产生用量，请核对结果", 409, false)
		}
		for _, call := range usage.Calls {
			if call.Status == "ok" {
				s.recordUnknownQuery(ctx, req, model.ID, scopeHash, idemKey, usage, latency)
				return nil, newError("request_unknown", "部分模型调用已产生用量，请核对结果", 409, false)
			}
		}
		return nil, solveErr
	}
	status := "answered"
	if !usage.Complete {
		status = "usage_incomplete"
	}
	resp := &Response{
		RequestID: req.RequestID, Status: status, Question: prepared.TitleText, Answer: answer,
		Model: modelInfo(model),
		Mode:  req.Mode, Cache: CacheInfo{Hit: false}, Usage: usage, LatencyMS: latency,
	}
	// 模型调用已成功，事实与缓存必须在有界后台上下文内写完，客户端断开不能丢失已付费结果。
	persistCtx, cancelPersist := persistContext(ctx)
	defer cancelPersist()
	if allowCache && usage.Complete && strings.TrimSpace(answer) != "" {
		expires := s.now().Add(time.Duration(ttlDays) * 24 * time.Hour)
		// 缓存只是优化：写入失败不能让已成功的模型作答失败或丢失幂等结果。
		_, _, _ = s.store.PutCache(persistCtx, store.CacheEntry{
			ID: store.NewUUID(), CacheKey: cacheKey, OfferingID: model.ID,
			PromptVersion: s.cfg.PromptVersion, NormalizationVersion: s.cfg.NormalizationVersion,
			Scope: req.CacheScope, ScopeKeyHash: scopeHash,
			QuestionTitle: prepared.TitleText, QuestionOptions: normalize.CanonicalOptions(prepared.Options),
			QuestionType: prepared.Type, ImagesDigest: normalize.ImagesDigest(prepared.Images),
			Answer: answer, SourceRequestID: req.RequestID, ExpiresAt: expires,
			MaxAge: time.Duration(ttlDays) * 24 * time.Hour,
		})
	}
	s.recordQuery(persistCtx, req, model.ID, scopeHash, idemKey, resp, "", time.Time{})
	return resp, nil
}

func (s *Service) solve(ctx context.Context, model *config.Model, modelKey string, caption *config.Model, captionKey string, q normalize.Question, budget int) (string, Usage, error) {
	maxTokens := model.EffectiveMaxOutputTokens(s.cfg.Limits.MaxOutputTokens)
	if budget > 0 && budget < maxTokens {
		maxTokens = budget
	}
	calls := make([]Call, 0, 2)
	usage := Usage{Calls: calls, Complete: true}
	var captions []string
	if len(q.Images) > 0 && !model.Vision {
		if caption == nil {
			return "", usage, newError("vision_unsupported", "当前模型不支持图片，且未配置图片转述模型", 422, false)
		}
		disabled, err := s.store.ModelDisabled(ctx, caption.ID)
		if err != nil {
			return "", usage, newError("internal_error", "无法读取图片转述模型状态", 500, false)
		}
		if disabled || caption.Disabled {
			return "", usage, newError("model_disabled", "图片转述模型已下架", 409, false)
		}
		for _, img := range q.Images {
			result, err := s.client.Chat(ctx, modelclient.Call{
				Protocol: caption.Protocol, Vendor: caption.Vendor,
				BaseURL: caption.BaseURL, APIKey: captionKey, Model: caption.Model,
				System: "你只描述图片中可见的文字和关键信息，不回答题目。",
				Prompt: "请转述这张题目图片的文字与关键信息。", Images: []normalize.Image{img},
				MaxOutputTokens: minInt(512, caption.EffectiveMaxOutputTokens(s.cfg.Limits.MaxOutputTokens)), Temperature: 0.1,
			})
			call := callFromResult("caption", caption.ID, result, err)
			calls = append(calls, call)
			if err != nil {
				usage.Calls = calls
				return "", usage, mapClientError(err)
			}
			usage.Complete = usage.Complete && result.Usage.Complete
			if strings.TrimSpace(result.Content) != "" {
				captions = append(captions, fmt.Sprintf("[图片%d] %s", img.Index, strings.TrimSpace(result.Content)))
			}
		}
	}
	prompt := buildPrompt(q, captions)
	result, err := s.client.Chat(ctx, modelclient.Call{
		Protocol: model.Protocol, Vendor: model.Vendor,
		BaseURL: model.BaseURL, APIKey: modelKey, Model: model.Model,
		System: "你是学习通题库答题助手。请根据题目给出答案，输出 <answer>答案</answer>。",
		Prompt: prompt, Images: pickImages(q.Images, model.Vision), MaxOutputTokens: maxTokens, Temperature: 0,
	})
	calls = append(calls, callFromResult("answer", model.ID, result, err))
	usage.Calls = calls
	if err != nil {
		return "", usage, mapClientError(err)
	}
	usage.Complete = usage.Complete && result.Usage.Complete
	if usage.Complete {
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens = sumUsage(calls)
		usage.Complete = usage.InputTokens != nil && usage.OutputTokens != nil && usage.TotalTokens != nil
	}
	answer := ExtractAnswer(result.Content, result.Reasoning, q.Type)
	if strings.TrimSpace(answer) == "" {
		return "", usage, newError("empty_answer", "模型未返回可提交的答案", 502, true)
	}
	return answer, usage, nil
}

func pickImages(images []normalize.Image, vision bool) []normalize.Image {
	if !vision {
		return nil
	}
	return images
}

func modelInfo(model *config.Model) ModelInfo {
	profile := model.Profile()
	return ModelInfo{
		OfferingID: model.ID, DisplayName: model.DisplayName, Capabilities: model.Capabilities(),
		Profile: CapabilityProfile{Vision: profile.Vision, ToolCall: profile.ToolCall, Reasoning: profile.Reasoning, Context: profile.Context},
	}
}

func callErrorCode(err error) string {
	var upstream *modelclient.Error
	if errors.As(err, &upstream) {
		return upstream.Code
	}
	return "model_unavailable"
}

func callFromResult(slot, modelID string, result modelclient.Result, err error) Call {
	call := Call{Slot: slot, ModelID: modelID, Status: "ok", LatencyMS: result.Latency.Milliseconds(), UsageComplete: result.Usage.Complete,
		InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, TotalTokens: result.Usage.TotalTokens}
	if err != nil {
		call.Status = "error"
		call.ErrorCode = callErrorCode(err)
	}
	return call
}

func sumUsage(calls []Call) (*int64, *int64, *int64) {
	var input, output, total int64
	for _, call := range calls {
		if call.InputTokens == nil || call.OutputTokens == nil || call.TotalTokens == nil ||
			*call.InputTokens < 0 || *call.OutputTokens < 0 || *call.TotalTokens < 0 ||
			*call.InputTokens > math.MaxInt64-input || *call.OutputTokens > math.MaxInt64-output ||
			*call.TotalTokens > math.MaxInt64-total {
			return nil, nil, nil
		}
		input += *call.InputTokens
		output += *call.OutputTokens
		total += *call.TotalTokens
	}
	return &input, &output, &total
}

func mapClientError(err error) error {
	var upstream *modelclient.Error
	if errors.As(err, &upstream) {
		return errorFromCode(upstream.Code)
	}
	return newError("model_unavailable", "模型服务不可用", 503, true)
}

func buildPrompt(q normalize.Question, captions []string) string {
	label := map[string]string{"single": "单选题", "multiple": "多选题", "judgement": "判断题", "completion": "填空题", "shortanswer": "简答题"}[q.Type]
	parts := []string{"题型：" + label, "题目：" + q.TitleText}
	if q.Type != "completion" && q.Type != "shortanswer" && strings.TrimSpace(q.Options) != "" {
		parts = append(parts, "选项：\n"+normalize.CanonicalOptions(q.Options))
	}
	if len(captions) > 0 {
		parts = append(parts, "图片转述：\n"+strings.Join(captions, "\n"))
	}
	parts = append(parts, "请输出 <answer>答案</answer>")
	return strings.Join(parts, "\n")
}

// recordUnknownQuery 保留已确认的部分用量，整体仍标记为不完整，不能自动结算。
func (s *Service) recordUnknownQuery(ctx context.Context, req Request, modelID, scopeHash, idemKey string, usage Usage, latency int64) {
	known := make([]Call, 0, len(usage.Calls))
	for _, call := range usage.Calls {
		if call.Status == "ok" && call.UsageComplete {
			known = append(known, call)
		}
	}
	partial := Usage{Complete: false, Calls: usage.Calls}
	if len(known) > 0 {
		partial.InputTokens, partial.OutputTokens, partial.TotalTokens = sumUsage(known)
	}
	persistCtx, cancel := persistContext(ctx)
	defer cancel()
	s.recordQuery(persistCtx, req, modelID, scopeHash, idemKey, &Response{
		RequestID: req.RequestID, Status: "unknown", Usage: partial, LatencyMS: latency,
	}, "", time.Time{})
}

func (s *Service) recordQuery(ctx context.Context, req Request, modelID, scopeHash, idemKey string, resp *Response, cacheEntryID string, cacheCreated time.Time) {
	usage, err := store.MarshalUsage(resp.Usage)
	if err != nil {
		return
	}
	q := store.QueryFact{
		RequestID: resp.RequestID, IdempotencyKey: idemKey, OfferingID: modelID, Mode: req.Mode,
		Scope: req.CacheScope, ScopeKeyHash: scopeHash, Status: resp.Status, Answer: resp.Answer,
		CacheHit: resp.Cache.Hit, Usage: usage, LatencyMS: resp.LatencyMS,
		CreatedAt: s.now(), CompletedAt: s.now(),
	}
	if cacheEntryID != "" {
		q.CacheEntryID = &cacheEntryID
		q.CacheCreatedAt = &cacheCreated
	}
	// 查询事实写入失败不回滚已返回结果；由幂等记录保证不重复调用模型。
	_ = s.store.RecordQuery(ctx, q)
}

// recordCalls 保存每次真实模型调用的事实；缓存命中没有调用，不写入伪记录。
func (s *Service) recordCalls(ctx context.Context, requestID string, calls []Call) error {
	for _, call := range calls {
		complete := call.UsageComplete
		if err := s.store.RecordModelCall(ctx, store.ModelCall{
			ID: store.NewUUID(), RequestID: requestID, Slot: call.Slot, ModelID: call.ModelID,
			InputTokens: call.InputTokens, OutputTokens: call.OutputTokens, TotalTokens: call.TotalTokens,
			UsageComplete: complete, Status: call.Status, LatencyMS: call.LatencyMS, ErrorCode: call.ErrorCode,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Legacy 保留 v1 `/api/search`：固定默认模型、每次都调用模型、只返回字符串字段。
func (s *Service) Legacy(ctx context.Context, title, options, questionType string, images []string) (string, string, error) {
	model, modelKey, caption, captionKey := s.models.ResolveForAnswer(s.models.DefaultID(), len(images) > 0)
	if model == nil {
		return "", "", newError("model_unavailable", "未配置可用的默认模型", 503, false)
	}
	disabled, err := s.store.ModelDisabled(ctx, model.ID)
	if err != nil {
		return "", "", newError("internal_error", "无法读取模型状态", 500, false)
	}
	if disabled || model.Disabled {
		return "", "", newError("model_disabled", "该模型已下架，请重新选择", 409, false)
	}
	prepared, err := normalize.Prepare(title, options, questionType, images, normalize.Limits{
		MaxImages: s.cfg.Limits.MaxImages, MaxImageBytes: s.cfg.Limits.MaxImageBytes, MaxImagePixels: s.cfg.Limits.MaxImagePixels,
	})
	if err != nil {
		return "", "", newError("invalid_question", err.Error(), 422, false)
	}
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-callCtx.Done():
		return "", "", newError("request_cancelled", "请求已取消，模型尚未调用", 499, false)
	}
	answer, usage, err := s.solve(callCtx, model, modelKey, caption, captionKey, prepared, 0)
	if err != nil {
		return "", "", err
	}
	requestID := "legacy_" + store.NewUUID()
	persistCtx, cancelPersist := persistContext(ctx)
	defer cancelPersist()
	s.recordCalls(persistCtx, requestID, usage.Calls)
	_ = s.store.RecordQuery(persistCtx, store.QueryFact{
		RequestID: requestID, OfferingID: model.ID, Mode: "always_llm",
		Scope: "user", ScopeKeyHash: "legacy", Status: "answered", Answer: answer,
		Usage: mustUsage(usage), LatencyMS: 0, CreatedAt: s.now(), CompletedAt: s.now(),
	})
	return title, answer, nil
}

func mustUsage(usage Usage) []byte {
	data, err := store.MarshalUsage(usage)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}

func (s *Service) Models(ctx context.Context) ([]ModelView, error) {
	overrides, err := s.store.ModelDisabledMap(ctx)
	if err != nil {
		return nil, newError("internal_error", "无法读取模型状态", 500, false)
	}
	items := s.models.Models()
	result := make([]ModelView, 0, len(items))
	for i := range items {
		model := &items[i]
		disabled := model.Disabled || overrides[model.ID]
		health := "configured"
		if disabled {
			health = "disabled"
		}
		result = append(result, ModelView{ID: model.ID, DisplayName: model.DisplayName,
			Capabilities: model.Capabilities(), Enabled: !disabled, Health: health,
			Profile: CapabilityProfile{Vision: model.Vision, ToolCall: model.ToolCall, Reasoning: model.Reasoning, Context: model.Context}})
	}
	return result, nil
}

func (s *Service) SetModelDisabled(ctx context.Context, modelID string, disabled bool) error {
	if s.models.Model(modelID) == nil {
		return newError("not_found", "模型条目不存在", 404, false)
	}
	if err := s.store.SetModelDisabled(ctx, modelID, disabled); err != nil {
		return newError("internal_error", "无法保存模型状态", 500, false)
	}
	return nil
}

func (s *Service) TestModel(ctx context.Context, modelID, kind string, confirmCost bool) (TestResult, error) {
	model, modelKey := s.models.Resolve(modelID)
	if model == nil {
		return TestResult{}, newError("not_found", "模型条目不存在", 404, false)
	}
	start := s.now()
	result := TestResult{Kind: kind}
	if kind != "reachability" && kind != "functional" {
		return TestResult{}, newError("invalid_test_kind", "测试类型必须是 reachability 或 functional", 422, false)
	}
	if kind == "functional" && !confirmCost {
		return TestResult{}, newError("cost_confirmation_required", "功能性测试会真实调用模型并产生 token 费用，需要显式确认", 428, false)
	}
	var testErr error
	var usage *Usage
	if kind == "reachability" {
		testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		testErr = s.client.Reachability(testCtx, model.Protocol, model.BaseURL, modelKey)
		cancel()
		result.Reachable = testErr == nil
	} else {
		testCtx, cancel := context.WithTimeout(ctx, minDuration(s.cfg.Timeout(), 30*time.Second))
		defer cancel()
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-testCtx.Done():
			testErr = &modelclient.Error{Code: "request_cancelled", Message: "功能测试已取消"}
		}
		if testErr != nil {
			cancel()
			result.Status = "failed"
			result.ErrorCode = "request_cancelled"
			result.LatencyMS = time.Since(start).Milliseconds()
			_ = s.recordModelTest(testCtx, store.ModelTest{
				ID: store.NewUUID(), ModelID: modelID, Kind: kind, Status: result.Status,
				ErrorCode: result.ErrorCode, LatencyMS: result.LatencyMS, CostConfirmed: confirmCost, CreatedAt: s.now(),
			})
			return result, nil
		}
		response, err := s.client.Chat(testCtx, modelclient.Call{
			Protocol: model.Protocol, Vendor: model.Vendor,
			BaseURL: model.BaseURL, APIKey: modelKey, Model: model.Model,
			System: "只回复 OK", Prompt: "请回复 OK", MaxOutputTokens: 16, Temperature: 0,
		})
		testErr = err
		if err == nil && strings.TrimSpace(response.Content) == "" {
			testErr = &modelclient.Error{Code: "empty_answer", Message: "模型返回空内容"}
		}
		if testErr == nil {
			result.Functional = true
			result.Reachable = true
			call := callFromResult("answer", model.ID, response, nil)
			u := Usage{Complete: response.Usage.Complete, Calls: []Call{call}}
			if u.Complete {
				u.InputTokens, u.OutputTokens, u.TotalTokens = sumUsage(u.Calls)
			}
			usage = &u
			result.Usage = usage
		}
	}
	result.LatencyMS = time.Since(start).Milliseconds()
	if testErr != nil {
		result.Status = "failed"
		var upstream *modelclient.Error
		if errors.As(testErr, &upstream) {
			result.ErrorCode = upstream.Code
		} else {
			result.ErrorCode = "internal_error"
		}
	} else {
		result.Status = "ok"
		if kind == "reachability" {
			result.Reachable = true
		}
	}
	_ = s.recordModelTest(ctx, store.ModelTest{
		ID: store.NewUUID(), ModelID: modelID, Kind: kind, Status: result.Status,
		ErrorCode: result.ErrorCode, LatencyMS: result.LatencyMS, CostConfirmed: confirmCost, CreatedAt: s.now(),
	})
	return result, nil
}

// recordModelTest 写入测试事实；调用方取消时仍以有界上下文保存结果。
func (s *Service) recordModelTest(ctx context.Context, test store.ModelTest) error {
	persistCtx, cancel := persistContext(ctx)
	defer cancel()
	return s.store.RecordModelTest(persistCtx, test)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
