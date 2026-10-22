// Package httpapi 暴露 fork 题库的 v1 兼容接口、v2 作答接口和内部管理接口。
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/answer"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

const legacyMaxRequestBytes = 32 << 20

type Server struct {
	cfg    *config.Config
	svc    *answer.Service
	store  *store.Store
	token  string
	logger *slog.Logger
}

func New(cfg *config.Config, svc *answer.Service, st *store.Store, serviceToken string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{cfg: cfg, svc: svc, store: st, token: serviceToken, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/search", s.legacySearch)
	mux.HandleFunc("GET /api/search", s.legacyProbe)
	mux.HandleFunc("POST /api/v2/answers", s.answerV2)
	mux.HandleFunc("GET /internal/v1/models", s.internalModels)
	mux.HandleFunc("PATCH /internal/v1/models/{id}", s.internalModelPatch)
	mux.HandleFunc("POST /internal/v1/models/{id}/tests", s.internalModelTest)
	mux.HandleFunc("POST /internal/v1/cache/invalidations", s.internalCacheInvalidate)
	mux.HandleFunc("GET /internal/v1/usage", s.internalUsage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return s.wrap(mux)
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		recorder.Header().Set("Cache-Control", "no-store")
		recorder.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recover() != nil {
				if !recorder.wrote {
					writeErrorV2(recorder, http.StatusInternalServerError, "internal_error", "题库服务内部错误", "")
				}
				s.logger.Error("fork 请求处理异常", "route", routeName(r), "request_id", r.Context().Value(requestIDKey{}))
			}
			// 只记录固定路由、状态和耗时，不记录题目、响应内容或密钥。
			s.logger.Info("fork HTTP 请求", "route", routeName(r), "status", recorder.status,
				"duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(recorder, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.status = status
	r.wrote = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(data)
}

func routeName(r *http.Request) string {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/search":
		return "legacy_search"
	case r.Method == http.MethodGet && r.URL.Path == "/api/search":
		return "legacy_probe"
	case r.URL.Path == "/api/v2/answers":
		return "v2_answers"
	case r.URL.Path == "/internal/v1/models":
		return "internal_models"
	case strings.HasPrefix(r.URL.Path, "/internal/v1/models/"):
		if strings.HasSuffix(r.URL.Path, "/tests") {
			return "internal_model_test"
		}
		return "internal_model_patch"
	case r.URL.Path == "/internal/v1/cache/invalidations":
		return "internal_cache_invalidation"
	case r.URL.Path == "/internal/v1/usage":
		return "internal_usage"
	case r.URL.Path == "/healthz":
		return "health"
	default:
		return "not_found"
	}
}

type requestIDKey struct{}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	provided := ""
	if strings.HasPrefix(header, "Bearer ") {
		provided = strings.TrimPrefix(header, "Bearer ")
	}
	// 对两侧做固定长度摘要比较，不把令牌长度或前缀差异暴露在比较分支中。
	providedSum := sha256.Sum256([]byte(provided))
	expectedSum := sha256.Sum256([]byte(s.token))
	return subtle.ConstantTimeCompare(providedSum[:], expectedSum[:]) == 1
}

func (s *Server) requireServiceToken(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" || !s.authorized(r) {
		writeErrorV2(w, http.StatusUnauthorized, "unauthenticated", "需要有效的服务令牌", "")
		return false
	}
	return true
}

func (s *Server) legacyProbe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type legacyRequest struct {
	Title             string   `json:"title"`
	Options           string   `json:"options"`
	Type              string   `json:"type"`
	Images            []string `json:"images"`
	SuggestionTitle   string   `json:"suggestion_title"`
	SuggestionOptions string   `json:"suggestion_options"`
}

func (s *Server) legacySearch(w http.ResponseWriter, r *http.Request) {
	var request legacyRequest
	if !decodeJSON(w, r, &request, legacyMaxRequestBytes, true) {
		return
	}
	if request.Type == "" {
		request.Type = "single"
	}
	switch request.Type {
	case "single", "multiple", "judgement", "completion":
	default:
		writeErrorV1(w, http.StatusBadRequest, "题目类型无效")
		return
	}
	title, options := request.Title, request.Options
	if request.SuggestionTitle != "" {
		title = request.SuggestionTitle
	}
	if request.SuggestionOptions != "" {
		options = request.SuggestionOptions
	}
	question, answerText, err := s.svc.Legacy(r.Context(), title, options, request.Type, request.Images)
	if err != nil {
		e := answer.AsError(err)
		writeErrorV1(w, legacyStatus(e.HTTPStatus), e.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"question": question, "answer": answerText})
}

func legacyStatus(status int) int {
	if status == 499 {
		return http.StatusBadRequest
	}
	if status <= 0 || status >= 500 {
		return http.StatusInternalServerError
	}
	return status
}

func (s *Server) answerV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeErrorV2(w, http.StatusBadRequest, "idempotency_required", "缺少 Idempotency-Key", "")
		return
	}
	if len(idempotencyKey) > 200 {
		writeErrorV2(w, http.StatusBadRequest, "idempotency_invalid", "Idempotency-Key 过长", "")
		return
	}
	var request answer.Request
	if !decodeJSON(w, r, &request, s.cfg.Limits.MaxRequestBytes, false) {
		return
	}
	response, err := s.svc.Answer(r.Context(), request, idempotencyKey)
	if err != nil {
		e := answer.AsError(err)
		writeErrorV2(w, e.HTTPStatus, e.Code, e.Message, request.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) internalModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	models, err := s.svc.Models(r.Context())
	if err != nil {
		e := answer.AsError(err)
		writeErrorV2(w, e.HTTPStatus, e.Code, e.Message, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": models})
}

func (s *Server) internalModelPatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body, 64<<10, false) {
		return
	}
	if body.Enabled == nil {
		writeErrorV2(w, http.StatusUnprocessableEntity, "validation_failed", "缺少 enabled", "")
		return
	}
	modelID := r.PathValue("id")
	if err := s.svc.SetModelDisabled(r.Context(), modelID, !*body.Enabled); err != nil {
		e := answer.AsError(err)
		writeErrorV2(w, e.HTTPStatus, e.Code, e.Message, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": modelID, "enabled": *body.Enabled})
}

func (s *Server) internalModelTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	var body struct {
		Kind        string `json:"kind"`
		ConfirmCost bool   `json:"confirm_cost"`
	}
	if !decodeJSON(w, r, &body, 64<<10, false) {
		return
	}
	result, err := s.svc.TestModel(r.Context(), r.PathValue("id"), body.Kind, body.ConfirmCost)
	if err != nil {
		e := answer.AsError(err)
		writeErrorV2(w, e.HTTPStatus, e.Code, e.Message, "")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) internalCacheInvalidate(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	var body struct {
		EntryID  string `json:"entry_id"`
		CacheKey string `json:"cache_key"`
		Reason   string `json:"reason"`
		Actor    string `json:"actor"`
	}
	if !decodeJSON(w, r, &body, 64<<10, false) {
		return
	}
	if strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 500 {
		writeErrorV2(w, http.StatusUnprocessableEntity, "validation_failed", "失效原因不能为空或过长", "")
		return
	}
	if (body.EntryID == "") == (body.CacheKey == "") {
		writeErrorV2(w, http.StatusUnprocessableEntity, "validation_failed", "entry_id 与 cache_key 必须且只能提供一个", "")
		return
	}
	id, invalidated, err := s.store.InvalidateCache(r.Context(), body.EntryID, body.CacheKey, body.Reason, body.Actor)
	if err != nil {
		writeErrorV2(w, http.StatusInternalServerError, "internal_error", "无法失效缓存", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invalidated": invalidated, "entry_id": id})
}

func (s *Server) internalUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireServiceToken(w, r) {
		return
	}
	since := time.Now().Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErrorV2(w, http.StatusUnprocessableEntity, "validation_failed", "since 必须是 RFC3339 时间", "")
			return
		}
		since = parsed
	}
	summary, err := s.store.Usage(r.Context(), since)
	if err != nil {
		writeErrorV2(w, http.StatusInternalServerError, "internal_error", "无法读取用量汇总", "")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// decodeJSON 严格解码 JSON；legacy 模式下错误使用上游兼容的 {"error": "..."}。
func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64, legacy bool) bool {
	fail := func(status int, message string) {
		if legacy {
			writeErrorV1(w, status, message)
		} else {
			writeErrorV2(w, status, "invalid_request", message, "")
		}
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(http.StatusUnsupportedMediaType, "需要 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	if !legacy {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		fail(http.StatusBadRequest, "请求 JSON 无效")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(http.StatusBadRequest, "请求包含多余内容")
		return false
	}
	return true
}

type envelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func writeErrorV1(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeErrorV2(w http.ResponseWriter, status int, code, message, requestID string) {
	var body envelope
	body.Error.Code, body.Error.Message, body.Error.RequestID = code, message, requestID
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
