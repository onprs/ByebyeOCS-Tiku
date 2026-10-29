package panel

import (
	"errors"
	"net/http"

	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
)

type upstreamModelInput struct {
	ID              string `json:"id"`
	ProviderID      string `json:"provider_id"`
	DisplayName     string `json:"display_name"`
	Protocol        string `json:"protocol"`
	Vendor          string `json:"vendor"`
	BaseURL         string `json:"base_url"`
	UpstreamModel   string `json:"upstream_model"`
	APIKey          string `json:"api_key"`
	Vision          bool   `json:"vision"`
	ToolCall        bool   `json:"tool_call"`
	Reasoning       bool   `json:"reasoning"`
	Context         int    `json:"context"`
	Disabled        bool   `json:"disabled"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Caption         bool   `json:"is_caption"`
}

func (i upstreamModelInput) editorInput() modelconfig.Input {
	return modelconfig.Input{
		ProviderID: i.ProviderID, DisplayName: i.DisplayName, Protocol: i.Protocol, Vendor: i.Vendor, BaseURL: i.BaseURL,
		UpstreamModel: i.UpstreamModel, APIKey: i.APIKey, Vision: i.Vision, ToolCall: i.ToolCall,
		Reasoning: i.Reasoning, Context: i.Context, Disabled: i.Disabled, MaxOutputTokens: i.MaxOutputTokens,
		Caption: i.Caption,
	}
}

// createModel 新增上游模型；模型元数据写入题库配置，密钥写入私密文件。
func (s *Server) createModel(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写，无法保存上游模型")
		return
	}
	var input upstreamModelInput
	if !decode(w, r, &input) {
		return
	}
	if !s.beginConfigChange(w, r, current, "upstream_model.create", input.ID) {
		return
	}
	if _, err := s.editor.Upsert(true, input.ID, input.editorInput()); err != nil {
		modelConfigFailure(w, err)
		return
	}
	recorded := s.finishConfigChange(r, current, "upstream_model.create", input.ID)
	jsonResponse(w, 201, map[string]any{"id": input.ID, "audit_pending": !recorded})
}

// updateModel 更新上游模型；标识不可修改，空密钥保留原密钥文件。
func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写，无法保存上游模型")
		return
	}
	id := r.PathValue("id")
	var input upstreamModelInput
	if !decode(w, r, &input) {
		return
	}
	if !s.beginConfigChange(w, r, current, "upstream_model.update", id) {
		return
	}
	if _, err := s.editor.Upsert(false, id, input.editorInput()); err != nil {
		modelConfigFailure(w, err)
		return
	}
	recorded := s.finishConfigChange(r, current, "upstream_model.update", id)
	jsonResponse(w, 200, map[string]any{"id": id, "audit_pending": !recorded})
}

// deleteModel 删除未被公开条目引用的上游模型。
func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写，无法删除上游模型")
		return
	}
	id := r.PathValue("id")
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	offerings, err := s.catalog.List(r.Context())
	if err != nil {
		fail(w, 503, "service_unavailable", "暂时无法核对公开条目，请稍后重试")
		return
	}
	for _, offering := range offerings {
		if offering.ProviderRef == id {
			fail(w, 409, "model_in_use", "仍有公开条目引用该模型，请先更换条目的关联模型")
			return
		}
	}
	if !s.beginConfigChange(w, r, current, "upstream_model.delete", id) {
		return
	}
	if _, err := s.editor.Delete(id); err != nil {
		modelConfigFailure(w, err)
		return
	}
	recorded := s.finishConfigChange(r, current, "upstream_model.delete", id)
	jsonResponse(w, 200, map[string]any{"id": id, "deleted": true, "audit_pending": !recorded})
}

func modelConfigFailure(w http.ResponseWriter, err error) {
	var validation *modelconfig.ValidationError
	switch {
	case errors.As(err, &validation):
		fail(w, 422, "validation_failed", validation.Message)
	case errors.Is(err, modelconfig.ErrNotFound):
		fail(w, 404, "not_found", "模型不存在")
	case errors.Is(err, modelconfig.ErrExists):
		fail(w, 409, "config_exists", "标识已存在")
	case errors.Is(err, modelconfig.ErrInUse):
		fail(w, 409, "provider_in_use", "提供商仍被模型引用，请先更换或删除相关模型")
	default:
		fail(w, 503, "config_unavailable", "无法保存题库配置，请检查服务用户对配置目录的写权限")
	}
}
