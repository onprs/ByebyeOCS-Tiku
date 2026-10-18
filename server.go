package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/onprs/OCS-Tiku/internal/catalog"
)

type Server struct {
	config   *Config
	store    *Store
	models   *ModelClient
	solver   *Solver
	catalog  *catalog.Store
	assets   string
	imageDir string
	address  string
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func jsonError(w http.ResponseWriter, status int, err error) {
	jsonResponse(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		jsonError(w, http.StatusUnsupportedMediaType, errors.New("需要 application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Errorf("请求 JSON 无效: %w", err))
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		jsonError(w, http.StatusBadRequest, errors.New("请求包含多余内容"))
		return false
	}
	return true
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/search", s.search)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("GET /api/config", s.ocsConfig)
	mux.HandleFunc("PUT /api/settings/answer", s.updateAnswer)
	mux.HandleFunc("PUT /api/services/{id}", s.updateService)
	mux.HandleFunc("DELETE /api/services/{id}", s.deleteService)
	mux.HandleFunc("GET /api/capability", s.capability)
	mux.HandleFunc("GET /api/models/status", s.modelStatus)
	mux.HandleFunc("POST /api/models/update", s.updateModels)
	mux.HandleFunc("GET /api/models/vendors", s.modelVendors)
	mux.HandleFunc("GET /api/models/list", s.modelList)
	mux.HandleFunc("GET /api/test/{kind}", s.testModel)
	mux.HandleFunc("GET /api/records", s.records)
	mux.HandleFunc("GET /api/records/recent", s.recentRecords)
	mux.HandleFunc("GET /api/records/{id}", s.record)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/dashboard", s.dashboard)
	mux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir(s.imageDir))))
	mux.HandleFunc("GET /", s.frontend)
	return mux
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title   string `json:"title"`
		Options string `json:"options"`
		Type    string `json:"type"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Type == "" {
		request.Type = "single"
	}
	switch request.Type {
	case "single", "multiple", "judgement", "completion":
	default:
		jsonError(w, http.StatusBadRequest, errors.New("题目类型无效"))
		return
	}
	cfg := s.config.snapshot()
	if answerDisabled(cfg.Answer.Answer) || cfg.Services[cfg.Answer.Answer.Service].Protocol == "" {
		jsonError(w, http.StatusBadRequest, errAnswerMissing)
		return
	}
	id, err := s.store.pending(r.Context(), request.Type)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	result, solveErr := s.solver.solve(withRecordID(r.Context(), id), request.Title, request.Options, request.Type)
	result.ID = id
	if err := s.store.complete(r.Context(), result); err != nil {
		jsonError(w, 500, err)
		return
	}
	if solveErr != nil {
		jsonResponse(w, 500, map[string]string{"error": solveErr.Error(), "question": request.Title, "answer": ""})
		return
	}
	jsonResponse(w, 200, map[string]string{"question": result.ProcessedTitle, "answer": result.Answer})
}

type protocolInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type vendorInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Models int    `json:"models"`
}

type settingsView struct {
	Services   map[string]ServiceConfig `json:"services"`
	Templates  []ServiceTemplate        `json:"templates"`
	Protocols  []protocolInfo           `json:"protocols"`
	Answer     AnswerConfig             `json:"answer"`
	Capability map[string]Capability    `json:"capability"`
	Catalog    catalog.Status           `json:"catalog"`
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config.snapshot()
	view := settingsView{
		Services: cfg.Services, Templates: serviceTemplates, Answer: cfg.Answer,
		Protocols: make([]protocolInfo, 0, len(protocols)),
		Capability: map[string]Capability{
			"answer":  resolveCapability(s.catalog, cfg.Services[cfg.Answer.Answer.Service], cfg.Answer.Answer),
			"caption": resolveCapability(s.catalog, cfg.Services[cfg.Answer.Caption.Service], cfg.Answer.Caption),
		},
	}
	for _, protocol := range protocols {
		view.Protocols = append(view.Protocols, protocolInfo{ID: protocol, Label: protocolLabel(protocol)})
	}
	if s.catalog != nil {
		view.Catalog = s.catalog.Status()
	}
	jsonResponse(w, 200, view)
}

func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	var service ServiceConfig
	if !decodeJSON(w, r, &service) {
		return
	}
	if err := s.config.updateService(r.PathValue("id"), service); err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	if err := s.config.deleteService(r.PathValue("id")); err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func (s *Server) updateAnswer(w http.ResponseWriter, r *http.Request) {
	var answer AnswerConfig
	if !decodeJSON(w, r, &answer) {
		return
	}
	if err := s.config.updateAnswer(answer); err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// capability 按查询参数计算能力判定，供设置页在选择模型时实时预览。
func (s *Server) capability(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.snapshot()
	query := r.URL.Query()
	service, ok := cfg.Services[query.Get("service")]
	if !ok {
		jsonError(w, http.StatusBadRequest, errors.New("服务不存在"))
		return
	}
	model := ModelConfig{
		Service: query.Get("service"), Model: query.Get("model"),
		Vision: query.Get("vision"), ToolCall: query.Get("tool_call"),
	}
	jsonResponse(w, 200, resolveCapability(s.catalog, service, model))
}

func (s *Server) modelStatus(w http.ResponseWriter, _ *http.Request) {
	if s.catalog == nil {
		jsonError(w, 503, errors.New("模型库未初始化"))
		return
	}
	jsonResponse(w, 200, s.catalog.Status())
}

func (s *Server) updateModels(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		jsonError(w, 503, errors.New("模型库未初始化"))
		return
	}
	changed, err := s.catalog.Update(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"changed": changed, "catalog": s.catalog.Status()})
}

func (s *Server) modelVendors(w http.ResponseWriter, _ *http.Request) {
	if s.catalog == nil {
		jsonError(w, 503, errors.New("模型库未初始化"))
		return
	}
	index := s.catalog.Catalog()
	names := index.Vendors()
	vendors := make([]vendorInfo, 0, len(names))
	for id, name := range names {
		vendors = append(vendors, vendorInfo{ID: id, Name: name, Models: len(index.VendorModels(id))})
	}
	sort.Slice(vendors, func(i, j int) bool { return vendors[i].Name < vendors[j].Name })
	jsonResponse(w, 200, vendors)
}

func (s *Server) modelList(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		jsonError(w, 503, errors.New("模型库未初始化"))
		return
	}
	vendor := r.URL.Query().Get("vendor")
	if vendor == "" {
		jsonError(w, http.StatusBadRequest, errors.New("缺少厂商参数"))
		return
	}
	index := s.catalog.Catalog()
	jsonResponse(w, 200, map[string]any{
		"vendor": vendor, "name": index.VendorName(vendor), "models": index.VendorModels(vendor),
	})
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.snapshot()
	var service ServiceConfig
	var model ModelConfig
	switch r.PathValue("kind") {
	case "answer":
		service = cfg.Services[cfg.Answer.Answer.Service]
		model = cfg.Answer.Answer
	case "caption":
		service = cfg.Services[cfg.Answer.Caption.Service]
		model = cfg.Answer.Caption
	default:
		jsonError(w, 404, errors.New("模型类型不存在"))
		return
	}
	if model.Model == "" {
		jsonResponse(w, 400, map[string]any{"ok": false, "error": "模型未配置"})
		return
	}
	capability := resolveCapability(s.catalog, service, model)
	if err := s.models.test(r.Context(), service, model, capability.Vision); err != nil {
		jsonResponse(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func queryInt(r *http.Request, key string, defaultValue, max int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("参数 %s 超出范围", key)
	}
	return n, nil
}

func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page", 1, 1000000)
	if err != nil {
		jsonError(w, 400, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 100)
	if err != nil {
		jsonError(w, 400, err)
		return
	}
	count, err := s.store.count(r.Context())
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	items, err := s.store.list(r.Context(), limit, (page-1)*limit)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	pages := max(1, (count+limit-1)/limit)
	jsonResponse(w, 200, map[string]any{"records": items, "total": count, "page": page, "pages": pages})
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		jsonError(w, 404, errors.New("记录不存在"))
		return
	}
	item, err := s.store.get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, 404, errors.New("记录不存在"))
		return
	}
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	calls, err := s.store.modelCalls(r.Context(), id)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	item.Calls = calls
	jsonResponse(w, 200, item)
}

func (s *Server) recentRecords(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 20, 100)
	if err != nil {
		jsonError(w, 400, err)
		return
	}
	since := int64(0)
	if value := r.URL.Query().Get("since"); value != "" {
		since, err = strconv.ParseInt(value, 10, 64)
	}
	if err != nil || since < 0 {
		jsonError(w, 400, errors.New("参数 since 无效"))
		return
	}
	items, err := s.store.recent(r.Context(), since, limit)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	jsonResponse(w, 200, items)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.stats(r.Context())
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	jsonResponse(w, 200, v)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.stats(r.Context())
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	records, err := s.store.list(r.Context(), 20, 0)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	daily, err := s.store.daily(r.Context())
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"stats": stats, "records": records, "daily_counts": daily})
}

func (s *Server) ocsConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config.snapshot()
	address := s.address
	if address == "" {
		address = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	base := "http://" + address
	jsonResponse(w, 200, []any{map[string]any{
		"name": "OCS超星LLM题库", "url": base + "/api/search", "homepage": base,
		"method": "post", "contentType": "json", "type": "GM_xmlhttpRequest",
		"headers": map[string]string{"Content-Type": "application/json"},
		"data":    map[string]string{"title": "${title}", "options": "${options}", "type": "${type}"},
		"handler": "return (res)=>[res.question, res.answer]",
	}})
}

func (s *Server) frontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		jsonError(w, 404, errors.New("接口不存在"))
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "."
	}
	if name != "." && (!fs.ValidPath(name) || !filepath.IsLocal(name)) {
		http.NotFound(w, r)
		return
	}
	if name != "." {
		if f, err := os.Open(filepath.Join(s.assets, filepath.FromSlash(name))); err == nil {
			defer f.Close()
			if stat, err := f.Stat(); err == nil && stat.Mode().IsRegular() {
				http.ServeContent(w, r, name, stat.ModTime(), f)
				return
			}
		}
		if path.Ext(name) != "" || strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.ServeFile(w, r, s.assets+"/index.html")
}
