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
	"strconv"
	"strings"
)

type Server struct {
	config   *Config
	store    *Store
	models   *ModelClient
	solver   *Solver
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
	mux.HandleFunc("GET /api/config", s.ocsConfig)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("PUT /api/settings/{kind}", s.updateSettings)
	mux.HandleFunc("GET /api/test-text", s.testModel)
	mux.HandleFunc("GET /api/test-vision", s.testModel)
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
	id, err := s.store.pending(r.Context(), request.Type)
	if err != nil {
		jsonError(w, 500, err)
		return
	}
	result, solveErr := s.solver.solve(r.Context(), request.Title, request.Options, request.Type)
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

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	v := s.config.snapshot()
	jsonResponse(w, 200, map[string]any{
		"text": v.Models.Text, "vision": v.Models.Vision, "answer": v.Answer,
		"text_providers": []string{"deepseek", "openai_compat"}, "vision_providers": []string{"openai_vl"},
	})
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var err error
	switch r.PathValue("kind") {
	case "text", "vision":
		var cfg ModelConfig
		if !decodeJSON(w, r, &cfg) {
			return
		}
		err = s.config.updateModel(r.PathValue("kind"), cfg)
	case "answer":
		var cfg AnswerConfig
		if !decodeJSON(w, r, &cfg) {
			return
		}
		err = s.config.updateAnswer(cfg)
	default:
		jsonError(w, 404, errors.New("设置不存在"))
		return
	}
	if err != nil {
		jsonError(w, 400, err)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.snapshot()
	var err error
	switch strings.TrimPrefix(r.URL.Path, "/api/test-") {
	case "text":
		if cfg.Models.Text.Provider == "" {
			err = errors.New("文本模型未配置")
		} else {
			err = s.models.testText(r.Context(), cfg.Models.Text)
		}
	case "vision":
		if cfg.Models.Vision.Provider == "" {
			err = errors.New("视觉模型未配置")
		} else {
			err = s.models.testVision(r.Context(), cfg.Models.Vision)
		}
	default:
		jsonError(w, 404, errors.New("模型类型不存在"))
		return
	}
	if err != nil {
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
