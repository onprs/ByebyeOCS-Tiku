package panel

import (
	"errors"
	"net/http"

	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
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
		id = input.ID
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
