package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/catalog"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/modelconfig"
)

func newServiceSettings(cfg *config.Config) modelconfig.ServiceSettings {
	catalogTTL := cfg.PricingCache.CatalogTTLHours
	if catalogTTL <= 0 {
		catalogTTL = config.DefaultPricingCatalogTTLHours
	}
	exchangeTTL := cfg.PricingCache.ExchangeTTLHours
	if exchangeTTL <= 0 {
		exchangeTTL = config.DefaultPricingExchangeTTLHours
	}
	return modelconfig.ServiceSettings{
		CaptionModelID: cfg.CaptionModelID, Concurrency: cfg.Limits.Concurrency, TimeoutSeconds: cfg.Limits.TimeoutSeconds,
		MaxOutputTokens: cfg.Limits.MaxOutputTokens, CacheTTLDays: cfg.Cache.TTLDays, CacheMaxTTLDays: cfg.Cache.MaxTTLDays,
		SharedCache: cfg.Cache.SharedEnabled, PricingCatalogTTLHours: catalogTTL,
		PricingExchangeTTLHours: exchangeTTL,
	}
}

func (s *Server) settingsSnapshot() modelconfig.ServiceSettings {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.serviceSettings
}

func restartRequired(saved, runtime modelconfig.ServiceSettings) bool {
	return saved.CaptionModelID != runtime.CaptionModelID || saved.Concurrency != runtime.Concurrency ||
		saved.TimeoutSeconds != runtime.TimeoutSeconds || saved.MaxOutputTokens != runtime.MaxOutputTokens ||
		saved.CacheTTLDays != runtime.CacheTTLDays || saved.CacheMaxTTLDays != runtime.CacheMaxTTLDays ||
		saved.SharedCache != runtime.SharedCache
}

func (s *Server) settingsPayload(r *http.Request) (map[string]any, error) {
	offerings, err := s.catalog.List(r.Context())
	if err != nil {
		return nil, err
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
	models := s.registry.Models()
	options := make([]map[string]any, 0, len(models))
	for _, model := range models {
		options = append(options, map[string]any{"id": model.ID, "display_name": model.DisplayName, "vision": model.Vision})
	}
	saved := s.settingsSnapshot()
	payload := map[string]any{
		"cache_ttl_days": s.cfg.Cache.TTLDays, "cache_max_ttl_days": s.cfg.Cache.MaxTTLDays,
		"shared_cache_enabled": s.cfg.Cache.SharedEnabled, "max_output_tokens": s.cfg.Limits.MaxOutputTokens,
		"concurrency": s.cfg.Limits.Concurrency, "timeout_seconds": s.cfg.Limits.TimeoutSeconds,
		"model_count": len(models), "offering_count": len(offerings), "published_offering_count": enabled,
		"default_offering_id": defaultOffering, "caption_model_id": s.registry.CaptionID(),
		"service": saved, "restart_required": restartRequired(saved, newServiceSettings(s.cfg)),
		"models": options, "pricing_cache": s.pricingReference.Status(),
	}
	return payload, nil
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, current session) {
	payload, err := s.settingsPayload(r)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	jsonResponse(w, 200, payload)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request, current session) {
	if s.editor == nil {
		fail(w, 503, "config_unavailable", "题库配置文件不可写")
		return
	}
	var input modelconfig.ServiceSettings
	if !decode(w, r, &input) {
		return
	}
	if err := s.validateServiceSettings(input); err != nil {
		fail(w, 422, "validation_failed", err.Error())
		return
	}
	if !s.beginConfigChange(w, r, current, "settings.update", "service-settings") {
		return
	}
	if err := s.editor.UpdateServiceSettings(input); err != nil {
		modelConfigFailure(w, err)
		return
	}
	s.settingsMu.Lock()
	s.serviceSettings = input
	s.settingsMu.Unlock()
	s.pricingReference.SetTTLs(time.Duration(input.PricingCatalogTTLHours)*time.Hour, time.Duration(input.PricingExchangeTTLHours)*time.Hour)
	recorded := s.finishConfigChange(r, current, "settings.update", "service-settings")
	payload, err := s.settingsPayload(r)
	if err != nil {
		databaseFailure(w, err)
		return
	}
	payload["audit_pending"] = !recorded
	jsonResponse(w, 200, payload)
}

func (s *Server) validateServiceSettings(input modelconfig.ServiceSettings) error {
	if input.Concurrency < 1 || input.Concurrency > 16 {
		return errors.New("同时处理查询必须在 1 到 16 之间")
	}
	if input.TimeoutSeconds < 5 || input.TimeoutSeconds > 300 {
		return errors.New("查询超时必须在 5 到 300 秒之间")
	}
	if input.MaxOutputTokens < 1 || input.MaxOutputTokens > 32768 {
		return errors.New("最大输出 token 必须在 1 到 32768 之间")
	}
	if input.CacheTTLDays < 1 || input.CacheTTLDays > input.CacheMaxTTLDays || input.CacheMaxTTLDays > 365 {
		return errors.New("默认缓存期限不能高于最长缓存期限，且最长不超过 365 天")
	}
	if input.PricingCatalogTTLHours < 1 || input.PricingCatalogTTLHours > 720 ||
		input.PricingExchangeTTLHours < 1 || input.PricingExchangeTTLHours > 720 {
		return errors.New("价格缓存期限必须在 1 到 720 小时之间")
	}
	if input.CaptionModelID != "" {
		found := false
		for _, model := range s.registry.Models() {
			if model.ID == input.CaptionModelID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("图片转述模型未在配置中定义")
		}
	}
	return nil
}

func (s *Server) refreshPricingCache(w http.ResponseWriter, r *http.Request, current session) {
	if !s.beginConfigChange(w, r, current, "pricing_cache.refresh", "service-settings") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	status, err := s.pricingReference.Refresh(ctx)
	recorded := s.finishConfigChange(r, current, "pricing_cache.refresh", "service-settings")
	if err != nil {
		code := pricingSourceUnavailable
		var failure *pricingLookupError
		if errors.As(err, &failure) {
			code = failure.Code
		}
		fail(w, http.StatusBadGateway, code, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{"pricing_cache": status, "audit_pending": !recorded})
}
