package panel

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/answer"
	"github.com/onprs/OCS-Tiku/internal/platform/catalog"
)

// offerings 返回公开条目；面板与主平台读取同一份事实。
func (s *Server) modelPricingReference(w http.ResponseWriter, r *http.Request, _ session) {
	providerID := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	if modelID == "" {
		fail(w, 422, pricingInvalidRequest, "请先选择关联模型")
		return
	}
	vendor := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("vendor")))
	if vendor == "" {
		providers := s.cfg.Providers
		if s.editor != nil {
			providers = s.editor.Providers()
		}
		for _, provider := range providers {
			if provider.ID == providerID {
				vendor = provider.Vendor
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	reference, err := s.pricingReference.Lookup(ctx, vendor, modelID)
	if err != nil {
		status, code := http.StatusBadGateway, "pricing_reference_unavailable"
		var lookup *pricingLookupError
		if errors.As(err, &lookup) {
			code = lookup.Code
			switch code {
			case pricingInvalidRequest:
				status = http.StatusUnprocessableEntity
			case pricingProviderUnknown, pricingModelUnknown, pricingIncomplete, pricingAmbiguous:
				status = http.StatusNotFound
			}
		}
		fail(w, status, code, err.Error())
		return
	}
	jsonResponse(w, 200, reference)
}

func (s *Server) offerings(w http.ResponseWriter, r *http.Request, current session) {
	items, err := s.catalog.List(r.Context())
	if err != nil {
		fail(w, 503, "service_unavailable", "暂时无法读取公开模型条目，请稍后重试")
		return
	}
	jsonResponse(w, 200, map[string]any{"items": items, "cache_max_ttl_days": s.cfg.Cache.MaxTTLDays, "shared_cache_enabled": s.cfg.Cache.SharedEnabled})
}

func (s *Server) offering(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
		return
	}
	item, err := s.catalog.Get(r.Context(), id)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 200, item)
}

func (s *Server) createOffering(w http.ResponseWriter, r *http.Request, current session) {
	var input catalog.Input
	if !decode(w, r, &input) {
		return
	}
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if input.Enabled && !s.checkOfferingModelReachability(w, r, current, input.ProviderRef) {
		return
	}
	created, err := s.catalog.Create(r.Context(), current.Username, input)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 201, created)
}

func (s *Server) updateOffering(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
		return
	}
	var input struct {
		catalog.Input
		Version int64 `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version < 1 {
		fail(w, 422, "validation_failed", "请提供条目版本")
		return
	}
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if input.Enabled && !s.checkOfferingModelReachability(w, r, current, input.ProviderRef) {
		return
	}
	updated, err := s.catalog.Update(r.Context(), current.Username, id, input.Version, input.Input)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 200, updated)
}

func (s *Server) priceVersions(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
		return
	}
	items, err := s.catalog.PriceVersions(r.Context(), id)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"items": items})
}

func (s *Server) deleteOffering(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
		return
	}
	var input struct {
		Version int64 `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version < 1 {
		fail(w, 422, "validation_failed", "请提供条目版本")
		return
	}
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if err := s.catalog.Delete(r.Context(), current.Username, id, input.Version); err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"public_id": id, "deleted": true})
}

// retireOffering 关闭公开条目；允许处理配置中已移除模型的遗留条目。
func (s *Server) retireOffering(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
		return
	}
	var input struct {
		Version int64 `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version < 1 {
		fail(w, 422, "validation_failed", "请提供条目版本")
		return
	}
	updated, err := s.catalog.Retire(r.Context(), current.Username, id, input.Version)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	jsonResponse(w, 200, updated)
}

func (s *Server) checkOfferingModelReachability(w http.ResponseWriter, r *http.Request, current session, modelID string) bool {
	models, err := s.svc.Models(r.Context())
	if err != nil {
		e := answer.AsError(err)
		fail(w, e.HTTPStatus, e.Code, e.Message)
		return false
	}
	found := false
	for _, model := range models {
		if model.ID != modelID {
			continue
		}
		found = true
		if !model.Enabled {
			fail(w, 409, "model_unavailable", "题库模型已停用，不能发布为可见条目")
			return false
		}
		break
	}
	if !found {
		fail(w, 422, "validation_failed", "请选择已配置的题库模型")
		return false
	}
	if err := s.store.PanelRecordAudit(r.Context(), current.Username, "offering.publish_check.request", modelID, map[string]any{"kind": "reachability"}); err != nil {
		fail(w, 503, "audit_unavailable", "暂时无法记录发布检查，请稍后重试")
		return false
	}
	result, testErr := s.svc.TestModel(r.Context(), modelID, "reachability", false)
	code := result.ErrorCode
	if testErr != nil {
		code = answer.AsError(testErr).Code
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	status := result.Status
	if testErr != nil {
		status = "failed"
	}
	if err := s.store.PanelRecordAudit(persist, current.Username, "offering.publish_check.result", modelID, map[string]any{"kind": "reachability", "status": status, "error_code": code}); err != nil {
		fail(w, 503, "audit_unavailable", "发布检查已结束，记录暂时不可用，请检查最近测试结果")
		return false
	}
	if testErr != nil {
		e := answer.AsError(testErr)
		fail(w, e.HTTPStatus, e.Code, "模型连通检查未通过，公开条目未保存："+e.Message)
		return false
	}
	if result.Status != "ok" {
		if code == "" {
			code = "model_unavailable"
		}
		statusCode := http.StatusBadGateway
		switch code {
		case "upstream_rate_limited":
			statusCode = http.StatusTooManyRequests
		case "upstream_timeout":
			statusCode = http.StatusGatewayTimeout
		case "unsupported_protocol":
			statusCode = http.StatusUnprocessableEntity
		}
		fail(w, statusCode, code, "模型连通检查未通过，公开条目未保存")
		return false
	}
	return true
}

// testOffering 用公开条目引用的模型做连通/功能测试；测试不会改写价格或可见状态。
func (s *Server) testOffering(w http.ResponseWriter, r *http.Request, current session) {
	id := r.PathValue("id")
	if !catalog.ValidPublicID(id) {
		fail(w, 422, "validation_failed", "公开条目编号无效")
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
	item, err := s.catalog.Get(r.Context(), id)
	if err != nil {
		offeringFailure(w, err)
		return
	}
	if err := s.store.PanelRecordAudit(r.Context(), current.Username, "offering.test.request", id, map[string]any{
		"kind": input.Kind, "cost_confirmed": input.ConfirmCost, "provider_ref": item.ProviderRef,
	}); err != nil {
		fail(w, 503, "audit_unavailable", "暂时无法记录测试请求，请稍后重试")
		return
	}
	result, err := s.svc.TestModel(r.Context(), item.ProviderRef, input.Kind, input.ConfirmCost)
	if err != nil {
		e := answer.AsError(err)
		fail(w, e.HTTPStatus, e.Code, e.Message)
		return
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.store.PanelRecordAudit(persist, current.Username, "offering.test.result", id, map[string]any{
		"kind": input.Kind, "status": result.Status, "error_code": result.ErrorCode, "provider_ref": item.ProviderRef,
	}); err != nil {
		fail(w, 503, "audit_unavailable", "测试已结束，记录暂时不可用，请查看最近测试结果")
		return
	}
	jsonResponse(w, 200, result)
}

// offeringFailure 区分输入校验、并发冲突和数据不可用；不把数据库文本返回给面板。
func offeringFailure(w http.ResponseWriter, err error) {
	var validation *catalog.ValidationError
	switch {
	case errors.As(err, &validation):
		fail(w, 422, "validation_failed", validation.Message)
	case errors.Is(err, catalog.ErrNotFound):
		fail(w, 404, "not_found", "公开条目不存在")
	case errors.Is(err, catalog.ErrModelAlreadyPublished):
		fail(w, 409, "model_already_published", "该上游模型已有公开条目，请编辑现有条目")
	case errors.Is(err, catalog.ErrVersionConflict):
		fail(w, 409, "version_conflict", catalog.ErrVersionConflict.Error())
	case errors.Is(err, catalog.ErrPriceVersionConflict):
		fail(w, 409, "price_version_conflict", catalog.ErrPriceVersionConflict.Error())
	default:
		fail(w, 503, "service_unavailable", "暂时无法保存公开条目，请稍后重试")
	}
}
