package panel

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

type providerInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Vendor   string `json:"vendor"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
}

func (i providerInput) editorInput() modelconfig.ProviderInput {
	return modelconfig.ProviderInput{Name: i.Name, Protocol: i.Protocol, Vendor: i.Vendor, BaseURL: i.BaseURL, APIKey: i.APIKey}
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request, current session) {
	providers := s.cfg.Providers
	if s.editor != nil {
		providers = s.editor.Providers()
	}
	counts := make(map[string]int)
	for _, model := range s.registry.Models() {
		counts[model.ProviderID]++
	}
	items := make([]any, 0, len(providers))
	for _, provider := range providers {
		items = append(items, map[string]any{"id": provider.ID, "name": provider.Name, "protocol": provider.Protocol,
			"vendor": provider.Vendor, "base_url": provider.BaseURL, "key_configured": provider.APIKeyFile != "", "model_count": counts[provider.ID]})
	}
	jsonResponse(w, 200, map[string]any{"items": items})
}

func (s *Server) provideModels(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可读")
		return
	}
	providerID := r.PathValue("id")
	provider, apiKey, err := s.editor.ProviderConnection(providerID)
	if err != nil {
		if errors.Is(err, modelconfig.ErrNotFound) {
			fail(w, 404, "not_found", "提供商不存在")
			return
		}
		fail(w, 503, "config_unavailable", "提供商密钥不可用")
		return
	}
	s.discoverProvider(w, r, current, providerID, provider.Protocol, provider.Vendor, provider.BaseURL, apiKey)
}

type providerDiscoveryInput struct {
	ProviderID string `json:"provider_id"`
	Protocol   string `json:"protocol"`
	Vendor     string `json:"vendor"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, current session) {
	var input providerDiscoveryInput
	if !decode(w, r, &input) {
		return
	}
	input.Protocol = strings.TrimSpace(input.Protocol)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.APIKey = strings.TrimSpace(input.APIKey)
	if len(input.APIKey) > 4096 || strings.ContainsAny(input.APIKey, "\r\n\x00") {
		fail(w, 422, "invalid_request", "模型密钥格式无效")
		return
	}
	if input.ProviderID != "" && input.APIKey == "" {
		if s.editor == nil {
			fail(w, 503, "config_unavailable", "题库配置文件不可读")
			return
		}
		provider, key, err := s.editor.ProviderConnection(input.ProviderID)
		if err != nil {
			providerFailure(w, err)
			return
		}
		if provider.Protocol != input.Protocol || provider.BaseURL != input.BaseURL {
			fail(w, 422, "invalid_request", "更换地址或协议时需要重新填写密钥")
			return
		}
		input.APIKey = key
	}
	if err := config.ValidateProviderConnection(input.Protocol, input.BaseURL, input.APIKey != "", s.cfg.AllowLoopbackUpstreams); err != nil {
		fail(w, 422, "invalid_request", "请检查接入协议、接口地址和 API 密钥")
		return
	}
	s.discoverProvider(w, r, current, input.ProviderID, input.Protocol, input.Vendor, input.BaseURL, input.APIKey)
}

func (s *Server) discoverProvider(w http.ResponseWriter, r *http.Request, current session, providerID, protocol, vendor, baseURL, apiKey string) {
	if err := s.store.PanelRecordAudit(r.Context(), current.Username, "provider.models.request", providerID, map[string]any{}); err != nil {
		fail(w, 503, "audit_unavailable", "暂时无法记录操作，请稍后重试")
		return
	}
	ctx, cancelDiscovery := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelDiscovery()
	items, err := s.svc.DiscoverModels(ctx, protocol, baseURL, apiKey)
	if err != nil {
		code := "model_unavailable"
		var upstream *modelclient.Error
		if errors.As(err, &upstream) {
			code = upstream.Code
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		_ = s.store.PanelRecordAudit(persist, current.Username, "provider.models.result", providerID, map[string]any{"error_code": code})
		providerDiscoveryFailure(w, err)
		return
	}
	items = enrichDiscoveredModels(items, vendor)
	configured := make(map[string]string)
	for _, model := range s.registry.Models() {
		usesProvider := model.ProviderID == providerID ||
			model.ProviderID == "" && model.Protocol == protocol && model.BaseURL == baseURL
		if usesProvider {
			if _, exists := configured[model.Model]; !exists {
				configured[model.Model] = model.ID
			}
		}
	}
	for index := range items {
		items[index].ConfiguredModelID = configured[items[index].ID]
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_ = s.store.PanelRecordAudit(persist, current.Username, "provider.models.result", providerID, map[string]any{"count": len(items)})
	jsonResponse(w, 200, map[string]any{"provider_id": providerID, "items": items})
}

func providerDiscoveryFailure(w http.ResponseWriter, err error) {
	var upstream *modelclient.Error
	if !errors.As(err, &upstream) {
		fail(w, 503, "model_unavailable", "模型服务不可用")
		return
	}
	status := http.StatusBadGateway
	switch upstream.Code {
	case "upstream_rate_limited":
		status = http.StatusTooManyRequests
	case "upstream_timeout":
		status = http.StatusGatewayTimeout
	case "unsupported_protocol":
		status = http.StatusUnprocessableEntity
	}
	fail(w, status, upstream.Code, upstream.Message)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, current session) {
	s.saveProvider(w, r, current, true)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, current session) {
	s.saveProvider(w, r, current, false)
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request, current session, create bool) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写")
		return
	}
	var input providerInput
	if !decode(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	if create {
		id = strings.TrimSpace(input.ID)
		if id == "" {
			id = "provider-" + strings.ReplaceAll(store.NewUUID(), "-", "")
		}
	}
	action := "provider.update"
	status := http.StatusOK
	if create {
		action, status = "provider.create", http.StatusCreated
	}
	if !s.beginConfigChange(w, r, current, action, id) {
		return
	}
	if err := s.editor.UpsertProvider(create, id, input.editorInput()); err != nil {
		providerFailure(w, err)
		return
	}
	recorded := s.finishConfigChange(r, current, action, id)
	jsonResponse(w, status, map[string]any{"id": id, "audit_pending": !recorded})
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写")
		return
	}
	id := r.PathValue("id")
	if !s.beginConfigChange(w, r, current, "provider.delete", id) {
		return
	}
	if err := s.editor.DeleteProvider(id); err != nil {
		providerFailure(w, err)
		return
	}
	recorded := s.finishConfigChange(r, current, "provider.delete", id)
	jsonResponse(w, 200, map[string]any{"id": id, "deleted": true, "audit_pending": !recorded})
}

func providerFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modelconfig.ErrNotFound) {
		fail(w, 404, "not_found", "提供商不存在")
		return
	}
	modelConfigFailure(w, err)
}
