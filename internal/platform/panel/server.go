package panel

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/onprs/OCS-Tiku/internal/platform/answer"
	"github.com/onprs/OCS-Tiku/internal/platform/catalog"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

//go:embed assets/*
var assets embed.FS

type Server struct {
	cfg       *config.Config
	svc       *answer.Service
	store     *store.Store
	catalog   *catalog.Service
	registry  *models.Registry
	editor    *modelconfig.Editor
	auth      *authentication
	secure    bool
	catalogMu sync.Mutex
}

func New(cfg *config.Config, svc *answer.Service, st *store.Store, registry *models.Registry) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	auth, err := newAuthentication(cfg.Panel)
	if err != nil {
		return nil, err
	}
	editor, _ := modelconfig.NewEditor(cfg, registry)
	server := &Server{cfg: cfg, svc: svc, store: st, catalog: catalog.NewWithRegistry(cfg, st.Pool(), registry),
		registry: registry, editor: editor, auth: auth, secure: strings.HasPrefix(cfg.Panel.Origin, "https://")}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /panel/api/login", server.login)
	mux.HandleFunc("GET /panel/api/session", server.protect(false, server.session))
	mux.HandleFunc("POST /panel/api/logout", server.protect(false, server.logout))
	mux.HandleFunc("GET /panel/api/overview", server.protect(false, server.overview))
	mux.HandleFunc("GET /panel/api/models", server.protect(false, server.models))
	mux.HandleFunc("PATCH /panel/api/models/{id}", server.protect(true, server.setModel))
	mux.HandleFunc("POST /panel/api/models/{id}/tests", server.protect(true, server.testModel))
	mux.HandleFunc("GET /panel/api/offerings", server.protect(false, server.offerings))
	mux.HandleFunc("GET /panel/api/offerings/{id}", server.protect(false, server.offering))
	mux.HandleFunc("POST /panel/api/offerings", server.protect(true, server.createOffering))
	mux.HandleFunc("PATCH /panel/api/offerings/{id}", server.protect(true, server.updateOffering))
	mux.HandleFunc("GET /panel/api/offerings/{id}/price-versions", server.protect(false, server.priceVersions))
	mux.HandleFunc("POST /panel/api/offerings/{id}/tests", server.protect(true, server.testOffering))
	mux.HandleFunc("POST /panel/api/offerings/{id}/retirements", server.protect(true, server.retireOffering))
	mux.HandleFunc("GET /panel/api/providers", server.protect(false, server.providers))
	mux.HandleFunc("POST /panel/api/providers", server.protect(true, server.createProvider))
	mux.HandleFunc("PATCH /panel/api/providers/{id}", server.protect(true, server.updateProvider))
	mux.HandleFunc("DELETE /panel/api/providers/{id}", server.protect(true, server.deleteProvider))
	mux.HandleFunc("POST /panel/api/upstream-models", server.protect(true, server.createModel))
	mux.HandleFunc("PATCH /panel/api/upstream-models/{id}", server.protect(true, server.updateModel))
	mux.HandleFunc("DELETE /panel/api/upstream-models/{id}", server.protect(true, server.deleteModel))
	mux.HandleFunc("GET /panel/api/requests", server.protect(false, server.requests))
	mux.HandleFunc("GET /panel/api/requests/{id}", server.protect(false, server.request))
	mux.HandleFunc("GET /panel/api/cache", server.protect(false, server.cache))
	mux.HandleFunc("POST /panel/api/cache/{id}/invalidations", server.protect(true, server.invalidate))
	mux.HandleFunc("GET /panel/api/settings", server.protect(false, server.settings))
	mux.HandleFunc("GET /panel/api/audit", server.protect(false, server.audit))
	mux.HandleFunc("/panel/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "not_found", "页面或操作不存在") })
	files, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	static := http.StripPrefix("/panel/", http.FileServer(http.FS(files)))
	mux.Handle("/panel/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, 405, "method_not_allowed", "此页面不支持该操作")
			return
		}
		static.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/panel/api/") {
			if (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != cfg.Panel.Origin) || strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				fail(w, 403, "origin_rejected", "请求来源无效")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !validOrigin(r, cfg.Panel.Origin) {
				fail(w, 403, "origin_rejected", "请求来源无效")
				return
			}
			deadline := 10 * time.Second
			if strings.HasSuffix(r.URL.Path, "/tests") {
				deadline = 40 * time.Second
			}
			ctx, cancel := context.WithTimeout(r.Context(), deadline)
			defer cancel()
			r = r.WithContext(ctx)
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// Mount 将维护面板挂载到独立路径，业务接口仍走原有服务令牌保护。
func Mount(api, maintenance http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/panel/", maintenance)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/panel/", http.StatusSeeOther) })
	mux.Handle("/", api)
	return mux
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func databaseFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "not_found", "记录不存在")
		return
	}
	fail(w, 503, "service_unavailable", "暂时无法读取或保存数据，请稍后重试")
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		fail(w, 415, "invalid_content_type", "请求格式无效")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		fail(w, 400, "invalid_request", "请求内容无效")
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid_request", "请求内容无效")
		return false
	}
	return true
}
func (s *Server) protect(write bool, next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, ok := s.auth.current(r)
		if !ok {
			fail(w, 401, "unauthorized", "请先登录维护面板")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameCSRF(r.Header.Get("X-CSRF-Token"), current.CSRF) {
			fail(w, 403, "csrf_rejected", "会话校验失败，请刷新后重试")
			return
		}
		if write && current.Role != "maintainer" {
			fail(w, 403, "forbidden", "当前账号仅可查看数据")
			return
		}
		next(w, r, current)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.permitLogin(r) {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "rate_limited", "登录尝试较多，请稍后重试")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.Username) > 64 || len(input.Password) > 72 || len(input.Password) == 0 {
		fail(w, 401, "invalid_credentials", "账号或密码不正确")
		return
	}
	select {
	case s.auth.semaphore <- struct{}{}:
		defer func() { <-s.auth.semaphore }()
	default:
		fail(w, 429, "rate_limited", "登录服务繁忙，请稍后重试")
		return
	}
	if !s.auth.authenticate(input.Username, input.Password) {
		fail(w, 401, "invalid_credentials", "账号或密码不正确")
		return
	}
	if err := s.store.PanelRecordAudit(r.Context(), input.Username, "session.login", "", map[string]any{}); err != nil {
		databaseFailure(w, err)
		return
	}
	token, current, err := s.auth.create(input.Username)
	if err != nil {
		fail(w, 429, "session_limit", "会话数量已达上限，请稍后重试")
		return
	}
	s.auth.remove(r)
	setCookie(w, token, s.secure, 8*60*60)
	jsonResponse(w, 200, current)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request, current session) {
	jsonResponse(w, 200, current)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, current session) {
	s.auth.remove(r)
	setCookie(w, "", s.secure, -1)
	_ = s.store.PanelRecordAudit(r.Context(), current.Username, "session.logout", "", map[string]any{})
	jsonResponse(w, 200, map[string]bool{"logged_out": true})
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request, current session) {
	value, err := s.store.PanelOverview(r.Context())
	if err != nil {
		databaseFailure(w, err)
		return
	}
	var summary map[string]json.RawMessage
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(value, &summary); err != nil {
		databaseFailure(w, err)
		return
	}
	if err := json.Unmarshal(summary["by_model"], &rows); err != nil {
		databaseFailure(w, err)
		return
	}
	for _, row := range rows {
		var id string
		if json.Unmarshal(row["model_id"], &id) == nil {
			if model := s.registry.Model(id); model != nil {
				row["display_name"], _ = json.Marshal(model.DisplayName)
			}
		}
	}
	summary["by_model"], _ = json.Marshal(rows)
	jsonResponse(w, 200, summary)
}
func (s *Server) models(w http.ResponseWriter, r *http.Request, current session) {
	models, err := s.svc.Models(r.Context())
	if err != nil {
		databaseFailure(w, err)
		return
	}
	last, err := s.store.PanelModelTests(r.Context())
	if err != nil {
		databaseFailure(w, err)
		return
	}
	items := make([]any, 0, len(models))
	for _, model := range models {
		configured := s.registry.Model(model.ID)
		if configured == nil {
			continue
		}
		items = append(items, map[string]any{"id": model.ID, "display_name": model.DisplayName, "capabilities": model.Capabilities, "enabled": model.Enabled, "health": model.Health,
			"provider_id": configured.ProviderID, "protocol": configured.Protocol, "vendor": configured.Vendor, "base_url": configured.BaseURL,
			"upstream_model": configured.Model, "vision": configured.Vision, "tool_call": configured.ToolCall,
			"reasoning": configured.Reasoning, "context": configured.Context, "max_output_tokens": configured.MaxOutputTokens,
			"key_configured": configured.APIKeyFile != "", "locked": configured.Disabled,
			"is_caption": s.registry.CaptionID() == model.ID, "last_test": last[model.ID]})
	}
	jsonResponse(w, 200, map[string]any{"items": items})
}
func (s *Server) setModel(w http.ResponseWriter, r *http.Request, current session) {
	model := s.registry.Model(r.PathValue("id"))
	if model == nil {
		fail(w, 404, "not_found", "模型不存在")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		fail(w, 422, "validation_failed", "请选择模型状态")
		return
	}
	if model.Disabled && *input.Enabled {
		fail(w, 409, "model_locked", "此模型暂时无法启用")
		return
	}
	if err := s.store.PanelSetModel(r.Context(), current.Username, model.ID, *input.Enabled); err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"id": model.ID, "enabled": *input.Enabled})
}
func (s *Server) testModel(w http.ResponseWriter, r *http.Request, current session) {
	modelID := r.PathValue("id")
	if s.registry.Model(modelID) == nil {
		fail(w, 404, "not_found", "模型不存在")
		return
	}
	var input struct {
		Kind        string `json:"kind"`
		ConfirmCost bool   `json:"confirm_cost"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Kind != "reachability" && input.Kind != "functional" {
		fail(w, 422, "validation_failed", "测试类型无效")
		return
	}
	if input.Kind == "functional" && !input.ConfirmCost {
		fail(w, 428, "cost_confirmation_required", "功能测试会产生模型用量，请确认后继续")
		return
	}
	if err := s.store.PanelRecordAudit(r.Context(), current.Username, "model.test.request", modelID, map[string]any{"kind": input.Kind, "cost_confirmed": input.ConfirmCost}); err != nil {
		databaseFailure(w, err)
		return
	}
	result, err := s.svc.TestModel(r.Context(), modelID, input.Kind, input.ConfirmCost)
	if err != nil {
		e := answer.AsError(err)
		fail(w, e.HTTPStatus, e.Code, e.Message)
		return
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.store.PanelRecordAudit(persist, current.Username, "model.test.result", modelID, map[string]any{"kind": input.Kind, "status": result.Status, "error_code": result.ErrorCode}); err != nil {
		fail(w, 503, "audit_unavailable", "测试已结束，记录暂时不可用，请查看最近测试结果")
		return
	}
	jsonResponse(w, 200, result)
}
func listFilter(w http.ResponseWriter, r *http.Request, statuses string) (store.PanelFilter, bool) {
	filter := store.PanelFilter{Cursor: r.URL.Query().Get("cursor"), Model: r.URL.Query().Get("model"), Status: r.URL.Query().Get("status"), Limit: 30}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(w, 422, "validation_failed", "分页数量无效")
			return filter, false
		}
		filter.Limit = n
	}
	_, cursorID, cursorErr := store.PanelCursor(filter.Cursor)
	if cursorErr != nil || (filter.Cursor != "" && (strings.HasSuffix(r.URL.Path, "/cache") || strings.HasSuffix(r.URL.Path, "/audit")) && !uuidPattern.MatchString(cursorID)) || len(filter.Model) > 64 || (filter.Status != "" && !strings.Contains("|"+statuses+"|", "|"+filter.Status+"|")) {
		fail(w, 422, "validation_failed", "筛选条件无效")
		return filter, false
	}
	return filter, true
}
func (s *Server) requests(w http.ResponseWriter, r *http.Request, current session) {
	filter, ok := listFilter(w, r, "answered|cache_hit|usage_incomplete|pending|completed|failed|unknown|expired")
	if !ok {
		return
	}
	page, err := s.store.PanelRequests(r.Context(), filter)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, page)
}
func (s *Server) request(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if len(id) < 1 || len(id) > 128 {
		fail(w, 422, "validation_failed", "请求编号无效")
		return
	}
	value, err := s.store.PanelRequest(r.Context(), id)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, value)
}
func (s *Server) cache(w http.ResponseWriter, r *http.Request, current session) {
	filter, ok := listFilter(w, r, "active|expired|invalidated")
	if !ok {
		return
	}
	page, err := s.store.PanelCache(r.Context(), filter)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, page)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) invalidate(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		fail(w, 422, "validation_failed", "缓存编号无效")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || utf8.RuneCountInString(input.Reason) > 1000 {
		fail(w, 422, "validation_failed", "请填写不超过1000字的失效原因")
		return
	}
	changed, err := s.store.PanelInvalidate(r.Context(), current.Username, id, input.Reason)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"entry_id": id, "invalidated": changed})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request, current session) {
	offerings, err := s.catalog.List(r.Context())
	if err != nil {
		databaseFailure(w, err)
		return
	}
	enabled, defaultOffering := 0, ""
	for _, item := range offerings {
		if item.Enabled && item.Health != catalog.HealthUnavailable {
			enabled++
		}
		if item.Default {
			defaultOffering = item.PublicID
		}
	}
	jsonResponse(w, 200, map[string]any{"cache_ttl_days": s.cfg.Cache.TTLDays, "cache_max_ttl_days": s.cfg.Cache.MaxTTLDays, "shared_cache_enabled": s.cfg.Cache.SharedEnabled,
		"max_output_tokens": s.cfg.Limits.MaxOutputTokens, "concurrency": s.cfg.Limits.Concurrency, "timeout_seconds": s.cfg.Limits.TimeoutSeconds,
		"model_count": len(s.registry.Models()), "offering_count": len(offerings), "published_offering_count": enabled,
		"default_offering_id": defaultOffering, "caption_model_id": s.registry.CaptionID()})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request, current session) {
	filter, ok := listFilter(w, r, "")
	if !ok {
		return
	}
	page, err := s.store.PanelAudit(r.Context(), filter)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, page)
}
