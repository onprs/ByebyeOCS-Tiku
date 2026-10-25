package panel

import (
	"errors"
	"net/http"

	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
)

type upstreamModelInput struct {
	ID              string `json:"id"`
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
		DisplayName: i.DisplayName, Protocol: i.Protocol, Vendor: i.Vendor, BaseURL: i.BaseURL,
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
	if _, err := s.editor.Upsert(true, input.ID, input.editorInput()); err != nil {
		modelConfigFailure(w, err)
		return
	}
	_ = s.store.PanelRecordAudit(r.Context(), current.Username, "upstream_model.create", input.ID, map[string]any{
		"protocol": input.Protocol, "display_name": input.DisplayName,
	})
	jsonResponse(w, 201, map[string]any{"id": input.ID})
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
	if _, err := s.editor.Upsert(false, id, input.editorInput()); err != nil {
		modelConfigFailure(w, err)
		return
	}
	_ = s.store.PanelRecordAudit(r.Context(), current.Username, "upstream_model.update", id, map[string]any{
		"protocol": input.Protocol, "display_name": input.DisplayName,
	})
	jsonResponse(w, 200, map[string]any{"id": id})
}

// deleteModel 删除未被公开条目引用的上游模型。
func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写，无法删除上游模型")
		return
	}
	id := r.PathValue("id")
	offerings, err := s.catalog.List(r.Context())
	if err != nil {
		fail(w, 503, "service_unavailable", "暂时无法核对公开条目，请稍后重试")
		return
	}
	for _, offering := range offerings {
		if offering.ProviderRef == id && offering.Enabled {
			fail(w, 409, "model_in_use", "仍有已发布公开条目引用该模型，请先停用该条目")
			return
		}
	}
	if _, err := s.editor.Delete(id); err != nil {
		modelConfigFailure(w, err)
		return
	}
	_ = s.store.PanelRecordAudit(r.Context(), current.Username, "upstream_model.delete", id, map[string]any{})
	jsonResponse(w, 200, map[string]any{"id": id, "deleted": true})
}

func modelConfigFailure(w http.ResponseWriter, err error) {
	var validation *modelconfig.ValidationError
	switch {
	case errors.As(err, &validation):
		fail(w, 422, "validation_failed", validation.Message)
	case errors.Is(err, modelconfig.ErrNotFound):
		fail(w, 404, "not_found", "模型不存在")
	case errors.Is(err, modelconfig.ErrExists):
		fail(w, 409, "model_exists", "模型标识已存在")
	default:
		fail(w, 503, "config_unavailable", "无法保存题库配置，请检查服务用户对配置目录的写权限")
	}
}
