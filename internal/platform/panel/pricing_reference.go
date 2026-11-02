package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	modelPriceCatalogURL = "https://models.dev/api.json"
	currencyRateURL      = "https://api.frankfurter.dev/v2/rate/usd/cny"
	maxPricingCatalog    = 32 << 20
)

// 参考价查询的失败原因只用于面板状态与接口代码，不直接当作面向用户的说明文案。
const (
	pricingInvalidRequest      = "pricing_invalid_request"
	pricingProviderUnknown     = "pricing_provider_unknown"
	pricingModelUnknown        = "pricing_model_unknown"
	pricingIncomplete          = "pricing_incomplete"
	pricingAmbiguous           = "pricing_ambiguous"
	pricingSourceUnavailable   = "pricing_source_unavailable"
	pricingExchangeUnavailable = "pricing_exchange_unavailable"
)

type pricingLookupError struct {
	Code    string
	Message string
}

func (e *pricingLookupError) Error() string { return e.Message }

func pricingFailure(code, message string) error {
	return &pricingLookupError{Code: code, Message: message}
}

type modelCosts struct {
	Input      json.Number `json:"input"`
	Output     json.Number `json:"output"`
	CacheRead  json.Number `json:"cache_read"`
	CacheWrite json.Number `json:"cache_write"`
}

func (c modelCosts) complete() bool {
	return c.Input != "" && c.Output != ""
}

func (c modelCosts) signature() string {
	return strings.Join([]string{c.Input.String(), c.Output.String(), c.CacheRead.String(), c.CacheWrite.String()}, "|")
}

// pricingModel 保留上游条目自身的名称与规范模型标识，用于按不同命名方式精确匹配。
type pricingModel struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Canonical string      `json:"canonical_model_id"`
	Cost      *modelCosts `json:"cost"`
}

type pricingProvider struct {
	Models map[string]pricingModel `json:"models"`
}

type currencyRate struct {
	Date  string      `json:"date"`
	Base  string      `json:"base"`
	Quote string      `json:"quote"`
	Rate  json.Number `json:"rate"`
}

type pricingReference struct {
	Source            string `json:"source"`
	SourceURL         string `json:"source_url"`
	Exchange          string `json:"exchange_source"`
	ExchangeURL       string `json:"exchange_url"`
	ExchangeDate      string `json:"exchange_date"`
	ExchangeRate      string `json:"exchange_rate"`
	Provider          string `json:"provider_id"`
	Model             string `json:"model_id"`
	ModelName         string `json:"model_name"`
	CatalogFetchedAt  string `json:"catalog_fetched_at,omitempty"`
	ExchangeFetchedAt string `json:"exchange_fetched_at,omitempty"`
	Stale             bool   `json:"stale,omitempty"`
	Rates             struct {
		Input      string  `json:"input_micros_per_million_tokens"`
		Output     string  `json:"output_micros_per_million_tokens"`
		CacheRead  *string `json:"cache_read_micros_per_million_tokens,omitempty"`
		CacheWrite *string `json:"cache_write_micros_per_million_tokens,omitempty"`
	} `json:"rates"`
}

type pricingMatch struct {
	Provider  string
	WireID    string
	Name      string
	Canonical string
	Cost      modelCosts
}

type pricingReferenceService struct {
	mu            sync.Mutex
	client        *http.Client
	catalog       map[string]pricingProvider
	catalogLoaded time.Time
	rate          currencyRate
	rateLoaded    time.Time
	cacheDir      string
	catalogTTL    atomic.Int64
	exchangeTTL   atomic.Int64
	now           func() time.Time
}

const (
	pricingCatalogCacheFile  = "models-dev-api.json"
	pricingExchangeCacheFile = "frankfurter-usd-cny.json"
	maxPricingCacheFile      = maxPricingCatalog + (1 << 20)
	defaultCatalogTTL        = 6 * time.Hour
	defaultExchangeTTL       = 24 * time.Hour
)

type pricingDiskCache struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Payload   json.RawMessage `json:"payload"`
}

type pricingCacheStatus struct {
	CatalogTTLHours  int    `json:"catalog_ttl_hours"`
	ExchangeTTLHours int    `json:"exchange_ttl_hours"`
	CatalogCachedAt  string `json:"catalog_cached_at,omitempty"`
	ExchangeCachedAt string `json:"exchange_cached_at,omitempty"`
	CatalogEntries   int    `json:"catalog_entries"`
	ExchangeDate     string `json:"exchange_date,omitempty"`
	ExchangeRate     string `json:"exchange_rate,omitempty"`
	Stale            bool   `json:"stale"`
}

func newPricingReferenceService() *pricingReferenceService {
	return newPricingReferenceServiceWithCache("", defaultCatalogTTL, defaultExchangeTTL)
}

func newPricingReferenceServiceWithCache(dir string, catalogTTL, exchangeTTL time.Duration) *pricingReferenceService {
	if catalogTTL <= 0 {
		catalogTTL = defaultCatalogTTL
	}
	if exchangeTTL <= 0 {
		exchangeTTL = defaultExchangeTTL
	}
	service := &pricingReferenceService{
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("价格来源不允许重定向")
		}},
		cacheDir: dir,
		now:      time.Now,
	}
	service.SetTTLs(catalogTTL, exchangeTTL)
	service.loadDiskCache()
	return service
}

func (s *pricingReferenceService) SetTTLs(catalogTTL, exchangeTTL time.Duration) {
	if catalogTTL > 0 {
		s.catalogTTL.Store(int64(catalogTTL / time.Second))
	}
	if exchangeTTL > 0 {
		s.exchangeTTL.Store(int64(exchangeTTL / time.Second))
	}
}

func (s *pricingReferenceService) ttls() (time.Duration, time.Duration) {
	return time.Duration(s.catalogTTL.Load()) * time.Second, time.Duration(s.exchangeTTL.Load()) * time.Second
}

func (s *pricingReferenceService) Lookup(ctx context.Context, vendor, model string) (pricingReference, error) {
	vendor, model = strings.ToLower(strings.TrimSpace(vendor)), strings.TrimSpace(model)
	if model == "" || len(vendor) > 128 || len(model) > 255 {
		return pricingReference{}, pricingFailure(pricingInvalidRequest, "缺少可匹配的模型标识")
	}
	now := s.now()
	catalogTTL, exchangeTTL := s.ttls()
	s.mu.Lock()
	catalog, catalogLoaded, rate, rateLoaded := s.catalog, s.catalogLoaded, s.rate, s.rateLoaded
	s.mu.Unlock()
	needCatalog := catalog == nil || now.Sub(catalogLoaded) >= catalogTTL
	needRate := !validCurrencyRate(rate) || now.Sub(rateLoaded) >= exchangeTTL
	fetchedCatalog, fetchedRate, catalogErr, rateErr := s.load(ctx, needCatalog, needRate)
	catalogFallback := false
	if needCatalog {
		switch {
		case catalogErr == nil && len(fetchedCatalog) > 0:
			catalog, catalogLoaded = fetchedCatalog, now
			s.storeCatalog(catalog, now)
		case catalog != nil:
			catalogFallback = true
		default:
			return pricingReference{}, pricingFailure(pricingSourceUnavailable, "暂时无法读取模型价格来源")
		}
	}
	match, err := resolvePricing(catalog, vendor, model)
	if err != nil {
		return pricingReference{}, err
	}
	rateFallback := false
	if needRate {
		switch {
		case rateErr == nil && validCurrencyRate(fetchedRate):
			rate, rateLoaded = fetchedRate, now
			s.storeRate(rate, now)
		case validCurrencyRate(rate):
			rateFallback = true
		default:
			return pricingReference{}, pricingFailure(pricingExchangeUnavailable, "暂时无法读取参考汇率")
		}
	}
	fx, ok := new(big.Rat).SetString(rate.Rate.String())
	if !ok || fx.Sign() <= 0 {
		return pricingReference{}, pricingFailure(pricingExchangeUnavailable, "参考汇率无效")
	}
	input, err := usdPerMillionToMicros(match.Cost.Input, fx)
	if err != nil {
		return pricingReference{}, pricingFailure(pricingSourceUnavailable, "模型输入价格格式无效")
	}
	output, err := usdPerMillionToMicros(match.Cost.Output, fx)
	if err != nil {
		return pricingReference{}, pricingFailure(pricingSourceUnavailable, "模型输出价格格式无效")
	}
	result := pricingReference{Source: "models.dev", SourceURL: modelPriceCatalogURL,
		Exchange: "Frankfurter", ExchangeURL: currencyRateURL, ExchangeDate: rate.Date,
		ExchangeRate: rate.Rate.String(), Provider: match.Provider, Model: match.WireID, ModelName: match.Name,
		CatalogFetchedAt: catalogLoaded.UTC().Format(time.RFC3339), ExchangeFetchedAt: rateLoaded.UTC().Format(time.RFC3339),
		Stale: catalogFallback || rateFallback}
	result.Rates.Input, result.Rates.Output = input, output
	if match.Cost.CacheRead != "" {
		value, err := usdPerMillionToMicros(match.Cost.CacheRead, fx)
		if err != nil {
			return pricingReference{}, pricingFailure(pricingSourceUnavailable, "模型缓存读取价格格式无效")
		}
		result.Rates.CacheRead = &value
	}
	if match.Cost.CacheWrite != "" {
		value, err := usdPerMillionToMicros(match.Cost.CacheWrite, fx)
		if err != nil {
			return pricingReference{}, pricingFailure(pricingSourceUnavailable, "模型缓存创建价格格式无效")
		}
		result.Rates.CacheWrite = &value
	}
	return result, nil
}

func (s *pricingReferenceService) storeCatalog(catalog map[string]pricingProvider, fetchedAt time.Time) {
	s.mu.Lock()
	s.catalog, s.catalogLoaded = catalog, fetchedAt
	s.mu.Unlock()
	s.saveCache(pricingCatalogCacheFile, fetchedAt, catalog)
}

func (s *pricingReferenceService) storeRate(rate currencyRate, fetchedAt time.Time) {
	s.mu.Lock()
	s.rate, s.rateLoaded = rate, fetchedAt
	s.mu.Unlock()
	s.saveCache(pricingExchangeCacheFile, fetchedAt, rate)
}

// Warm 在服务启动后预取价格来源；本地缓存仍新鲜时不会发起网络请求，也不阻塞状态查询。
func (s *pricingReferenceService) Warm(ctx context.Context) {
	now := s.now()
	catalogTTL, exchangeTTL := s.ttls()
	s.mu.Lock()
	catalog, catalogLoaded, rate, rateLoaded := s.catalog, s.catalogLoaded, s.rate, s.rateLoaded
	s.mu.Unlock()
	needCatalog := catalog == nil || now.Sub(catalogLoaded) >= catalogTTL
	needRate := !validCurrencyRate(rate) || now.Sub(rateLoaded) >= exchangeTTL
	if !needCatalog && !needRate {
		return
	}
	fetchedCatalog, fetchedRate, catalogErr, rateErr := s.load(ctx, needCatalog, needRate)
	if needCatalog && catalogErr == nil && len(fetchedCatalog) > 0 {
		s.storeCatalog(fetchedCatalog, now)
	}
	if needRate && rateErr == nil && validCurrencyRate(fetchedRate) {
		s.storeRate(fetchedRate, now)
	}
}

// Refresh 跳过缓存期限强制更新；失败时保留原有缓存供后续查询。
func (s *pricingReferenceService) Refresh(ctx context.Context) (pricingCacheStatus, error) {
	now := s.now()
	s.mu.Lock()
	hasCatalog := s.catalog != nil
	hasRate := validCurrencyRate(s.rate)
	s.mu.Unlock()
	catalog, rate, catalogErr, rateErr := s.load(ctx, true, true)
	if catalogErr == nil && len(catalog) > 0 {
		s.storeCatalog(catalog, now)
		hasCatalog = true
	}
	if rateErr == nil && validCurrencyRate(rate) {
		s.storeRate(rate, now)
		hasRate = true
	}
	status := s.Status()
	if catalogErr != nil {
		if !hasCatalog {
			return status, pricingFailure(pricingSourceUnavailable, "暂时无法读取模型价格来源")
		}
		return status, pricingFailure(pricingSourceUnavailable, "模型价格目录刷新未完成")
	}
	if rateErr != nil {
		if !hasRate {
			return status, pricingFailure(pricingExchangeUnavailable, "暂时无法读取参考汇率")
		}
		return status, pricingFailure(pricingExchangeUnavailable, "参考汇率刷新未完成")
	}
	return status, nil
}

func (s *pricingReferenceService) Status() pricingCacheStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(s.now())
}

func (s *pricingReferenceService) statusLocked(now time.Time) pricingCacheStatus {
	catalogTTL, exchangeTTL := s.ttls()
	status := pricingCacheStatus{CatalogTTLHours: int(catalogTTL / time.Hour), ExchangeTTLHours: int(exchangeTTL / time.Hour)}
	if s.catalog != nil {
		status.CatalogCachedAt = s.catalogLoaded.UTC().Format(time.RFC3339)
		status.CatalogEntries = len(s.catalog)
		if now.Sub(s.catalogLoaded) >= catalogTTL {
			status.Stale = true
		}
	}
	if validCurrencyRate(s.rate) {
		status.ExchangeCachedAt = s.rateLoaded.UTC().Format(time.RFC3339)
		status.ExchangeDate = s.rate.Date
		status.ExchangeRate = s.rate.Rate.String()
		if now.Sub(s.rateLoaded) >= exchangeTTL {
			status.Stale = true
		}
	}
	return status
}

func (s *pricingReferenceService) loadDiskCache() {
	if s.cacheDir == "" {
		return
	}
	if catalog, fetchedAt, ok := readCatalogCache(filepath.Join(s.cacheDir, pricingCatalogCacheFile)); ok {
		s.catalog, s.catalogLoaded = catalog, fetchedAt
	}
	if rate, fetchedAt, ok := readRateCache(filepath.Join(s.cacheDir, pricingExchangeCacheFile)); ok {
		s.rate, s.rateLoaded = rate, fetchedAt
	}
}

func readCatalogCache(path string) (map[string]pricingProvider, time.Time, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxPricingCacheFile {
		return nil, time.Time{}, false
	}
	var envelope pricingDiskCache
	if json.Unmarshal(data, &envelope) != nil || envelope.FetchedAt.IsZero() || len(envelope.Payload) == 0 {
		return nil, time.Time{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.UseNumber()
	var catalog map[string]pricingProvider
	if decoder.Decode(&catalog) != nil || len(catalog) == 0 {
		return nil, time.Time{}, false
	}
	return catalog, envelope.FetchedAt.UTC(), true
}

func readRateCache(path string) (currencyRate, time.Time, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxPricingCacheFile {
		return currencyRate{}, time.Time{}, false
	}
	var envelope pricingDiskCache
	if json.Unmarshal(data, &envelope) != nil || envelope.FetchedAt.IsZero() || len(envelope.Payload) == 0 {
		return currencyRate{}, time.Time{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.UseNumber()
	var rate currencyRate
	if decoder.Decode(&rate) != nil || !validCurrencyRate(rate) {
		return currencyRate{}, time.Time{}, false
	}
	return rate, envelope.FetchedAt.UTC(), true
}

func (s *pricingReferenceService) saveCache(name string, fetchedAt time.Time, payload any) {
	if s.cacheDir == "" {
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	document, err := json.Marshal(pricingDiskCache{FetchedAt: fetchedAt.UTC(), Payload: encoded})
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return
	}
	temporary, err := os.CreateTemp(s.cacheDir, ".pricing-*.tmp")
	if err != nil {
		return
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if temporary.Chmod(0o600) != nil {
		temporary.Close()
		return
	}
	if _, err := temporary.Write(document); err != nil {
		temporary.Close()
		return
	}
	if temporary.Sync() != nil || temporary.Close() != nil {
		return
	}
	_ = os.Rename(tempName, filepath.Join(s.cacheDir, name))
}

func (s *pricingReferenceService) load(ctx context.Context, needCatalog, needRate bool) (catalog map[string]pricingProvider, rate currencyRate, catalogErr, rateErr error) {
	var group sync.WaitGroup
	if needCatalog {
		group.Add(1)
		go func() {
			defer group.Done()
			catalogErr = s.fetchJSONRetry(ctx, modelPriceCatalogURL, &catalog)
		}()
	}
	if needRate {
		group.Add(1)
		go func() {
			defer group.Done()
			rateErr = s.fetchJSONRetry(ctx, currencyRateURL, &rate)
		}()
	}
	group.Wait()
	return catalog, rate, catalogErr, rateErr
}

func validCurrencyRate(rate currencyRate) bool {
	return rate.Base == "USD" && rate.Quote == "CNY" && rate.Rate != "" && rate.Date != ""
}

// resolvePricing 先使用提供商自身条目，其次使用规范模型标识，最后才在价格一致的跨提供商记录中取值。
func resolvePricing(catalog map[string]pricingProvider, vendor, model string) (pricingMatch, error) {
	vendors := pricingVendors(vendor, model)
	if len(vendors) == 0 {
		return pricingMatch{}, pricingFailure(pricingInvalidRequest, "缺少可匹配的提供商标识")
	}
	known := false
	for _, id := range vendors {
		if _, ok := catalog[id]; ok {
			known = true
			break
		}
	}
	if !known {
		return pricingMatch{}, pricingFailure(pricingProviderUnknown, "模型价格来源未收录该提供商")
	}
	wires := pricingWireNames(model, vendors)
	canonicals := pricingCanonicalNames(model, wires, vendors)
	scoped := pricingRows(catalog, vendors...)
	if match, ok := firstCosted(scoped, func(row pricingMatch) bool {
		return matchesAnyName(wires, row.WireID)
	}); ok {
		return match, nil
	}
	if match, found, ambiguous := uniqueCosted(scoped, func(row pricingMatch) bool {
		return matchesAnyName(canonicals, row.Canonical)
	}); found {
		return match, nil
	} else if ambiguous {
		return pricingMatch{}, pricingFailure(pricingAmbiguous, "同一模型存在多个不同价格的参考条目")
	}
	if match, found, ambiguous := uniqueCosted(scoped, func(row pricingMatch) bool {
		return snapshotEquivalent(canonicals, row.Canonical)
	}); found {
		return match, nil
	} else if ambiguous {
		return pricingMatch{}, pricingFailure(pricingAmbiguous, "同一模型的快照价格不一致")
	}
	if match, found, ambiguous := uniqueCosted(scoped, func(row pricingMatch) bool {
		return separatedEquivalent(canonicals, row.Canonical) || separatedEquivalent(wires, row.WireID)
	}); found {
		return match, nil
	} else if ambiguous {
		return pricingMatch{}, pricingFailure(pricingAmbiguous, "等价模型名的价格不一致")
	}
	all := pricingRows(catalog, allProviderIDs(catalog)...)
	if match, found, ambiguous := uniqueCosted(all, func(row pricingMatch) bool {
		return matchesAnyName(canonicals, row.Canonical)
	}); found {
		return match, nil
	} else if ambiguous {
		return pricingMatch{}, pricingFailure(pricingAmbiguous, "不同提供商的价格不一致")
	}
	candidate := func(row pricingMatch) bool {
		return matchesAnyName(wires, row.WireID) || matchesAnyName(canonicals, row.Canonical) ||
			snapshotEquivalent(canonicals, row.Canonical) || separatedEquivalent(canonicals, row.Canonical) ||
			separatedEquivalent(wires, row.WireID)
	}
	for _, group := range [][]pricingMatch{scoped, all} {
		for _, row := range group {
			if candidate(row) {
				return pricingMatch{}, pricingFailure(pricingIncomplete, "模型价格来源没有完整的输入与输出价格")
			}
		}
	}
	return pricingMatch{}, pricingFailure(pricingModelUnknown, "模型价格来源未收录该模型")
}

func pricingVendors(vendor, model string) []string {
	result := make([]string, 0, 2)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > 128 {
			return
		}
		for _, existing := range result {
			if existing == value {
				return
			}
		}
		result = append(result, value)
	}
	add(vendor)
	if index := strings.IndexByte(model, '/'); index > 0 {
		add(model[:index])
	}
	return result
}

func pricingWireNames(model string, vendors []string) []string {
	result := []string{model}
	lower := strings.ToLower(model)
	for _, vendor := range vendors {
		if strings.HasPrefix(lower, vendor+"/") {
			if trimmed := strings.TrimSpace(model[len(vendor)+1:]); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return uniqueNames(result)
}

func pricingCanonicalNames(model string, wires, vendors []string) []string {
	result := []string{model}
	for _, vendor := range vendors {
		prefix := vendor + "/"
		for _, wire := range wires {
			if strings.HasPrefix(strings.ToLower(wire), prefix) {
				result = append(result, wire)
				continue
			}
			result = append(result, prefix+wire)
		}
	}
	return uniqueNames(result)
}

func uniqueNames(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func matchesAnyName(names []string, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, name := range names {
		if value == strings.ToLower(name) {
			return true
		}
	}
	return false
}

// snapshotEquivalent 允许不带日期后缀的规范模型名匹配同一厂商的日期快照，例如 -1017。
func snapshotEquivalent(names []string, canonical string) bool {
	base, stripped := stripSnapshotSuffix(canonical)
	if !stripped {
		return false
	}
	for _, name := range names {
		if strings.ToLower(strings.TrimSpace(name)) == base {
			return true
		}
	}
	return false
}

// separatedEquivalent 兼容网关用点号表达版本的模型名，例如 claude-sonnet-4.5 与 claude-sonnet-4-5。
func separatedEquivalent(names []string, value string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), ".", "-")
	if normalized == "" {
		return false
	}
	for _, name := range names {
		if strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), ".", "-") == normalized {
			return true
		}
	}
	return false
}

func stripSnapshotSuffix(value string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(value))
	index := strings.LastIndexByte(lower, '-')
	if index < 0 || index == len(lower)-1 {
		return lower, false
	}
	suffix := lower[index+1:]
	switch len(suffix) {
	case 4, 6, 8:
	default:
		return lower, false
	}
	for _, digit := range suffix {
		if digit < '0' || digit > '9' {
			return lower, false
		}
	}
	return lower[:index], true
}

func pricingRows(catalog map[string]pricingProvider, providers ...string) []pricingMatch {
	rows := make([]pricingMatch, 0, 16)
	for _, providerID := range providers {
		provider, ok := catalog[providerID]
		if !ok {
			continue
		}
		wires := make([]string, 0, len(provider.Models))
		for wire := range provider.Models {
			wires = append(wires, wire)
		}
		sort.Strings(wires)
		for _, wire := range wires {
			item := provider.Models[wire]
			row := pricingMatch{Provider: providerID, WireID: wire, Name: item.Name, Canonical: item.Canonical}
			if item.Cost != nil {
				row.Cost = *item.Cost
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func allProviderIDs(catalog map[string]pricingProvider) []string {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func firstCosted(rows []pricingMatch, accept func(pricingMatch) bool) (pricingMatch, bool) {
	for _, row := range rows {
		if accept(row) && row.Cost.complete() {
			return row, true
		}
	}
	return pricingMatch{}, false
}

func uniqueCosted(rows []pricingMatch, accept func(pricingMatch) bool) (pricingMatch, bool, bool) {
	byCost := make(map[string]pricingMatch, 2)
	for _, row := range rows {
		if !accept(row) || !row.Cost.complete() {
			continue
		}
		if _, exists := byCost[row.Cost.signature()]; exists {
			continue
		}
		byCost[row.Cost.signature()] = row
	}
	switch len(byCost) {
	case 0:
		return pricingMatch{}, false, false
	case 1:
		for _, row := range byCost {
			return row, true, false
		}
	}
	return pricingMatch{}, false, true
}

func (s *pricingReferenceService) fetchJSONRetry(ctx context.Context, url string, target any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return err
			case <-timer.C:
			}
		}
		if err = s.fetchJSON(ctx, url, target); err == nil {
			return nil
		}
	}
	return err
}

func (s *pricingReferenceService) fetchJSON(ctx context.Context, url string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return errors.New("价格来源请求失败")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPricingCatalog+1))
	if err != nil || len(data) > maxPricingCatalog {
		return errors.New("价格来源响应超出限制")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func usdPerMillionToMicros(price json.Number, exchange *big.Rat) (string, error) {
	amount, ok := new(big.Rat).SetString(price.String())
	if !ok || amount.Sign() < 0 {
		return "", errors.New("模型价格无效")
	}
	amount.Mul(amount, exchange)
	amount.Mul(amount, big.NewRat(1_000_000, 1))
	numerator, denominator := amount.Num(), amount.Denom()
	numerator.Add(numerator, new(big.Int).Quo(denominator, big.NewInt(2)))
	return new(big.Int).Quo(numerator, denominator).String(), nil
}
