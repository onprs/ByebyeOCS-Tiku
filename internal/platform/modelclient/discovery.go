package modelclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

const (
	maxDiscoveryResponseBytes int64 = 2 << 20
	maxDiscoveryPages               = 20
	maxDiscoveredModels             = 10000
)

var errInvalidDiscovery = errors.New("上游模型目录无效或不完整")

// DiscoveredModel 是上游模型目录中的公开元数据，不包含任何密钥或请求内容。
type DiscoveredModel struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name"`
	OwnedBy           string   `json:"owned_by,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	Context           int      `json:"context,omitempty"`
	MetadataMatched   bool     `json:"metadata_matched,omitempty"`
	ConfiguredModelID string   `json:"configured_model_id,omitempty"`
}

// DiscoverModels 从提供商的模型目录读取模型标识。
// 不同协议使用各自的只读目录接口；调用不会产生模型回答用量。
func (c *Client) DiscoverModels(ctx context.Context, protocol, baseURL, apiKey string) ([]DiscoveredModel, error) {
	if strings.TrimSpace(protocol) == "" {
		protocol = engine.ProtocolOpenAIChat
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
	}
	switch protocol {
	case engine.ProtocolOpenAIChat:
		return c.discoverOpenAI(ctx, baseURL, apiKey)
	case engine.ProtocolAnthropicMessages:
		return c.discoverAnthropic(ctx, baseURL, apiKey)
	case engine.ProtocolGeminiGenerate:
		return c.discoverGemini(ctx, baseURL, apiKey)
	case engine.ProtocolOllamaChat:
		return c.discoverOllama(ctx, baseURL, apiKey)
	default:
		return nil, &Error{Code: "unsupported_protocol", Message: safeMessage("unsupported_protocol")}
	}
}

type openAIModelPage struct {
	Data []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

type anthropicModelPage struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

type geminiModelPage struct {
	Models []struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
	NextPageToken string `json:"nextPageToken"`
}

type ollamaModelPage struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func (c *Client) discoverOpenAI(ctx context.Context, baseURL, apiKey string) ([]DiscoveredModel, error) {
	items := make([]DiscoveredModel, 0)
	after := ""
	seenPages := make(map[string]bool)
	for pageNumber := 0; pageNumber < maxDiscoveryPages; pageNumber++ {
		endpoint, err := appendPath(baseURL, "models")
		if err != nil {
			return nil, discoveryError(err)
		}
		endpoint, err = addQuery(endpoint, "limit", "100")
		if err == nil && after != "" {
			endpoint, err = addQuery(endpoint, "after", after)
		}
		if err != nil {
			return nil, discoveryError(err)
		}
		var page openAIModelPage
		if err := c.getJSON(ctx, endpoint, bearerHeaders(apiKey), &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, discoveryError(errInvalidDiscovery)
		}
		for _, model := range page.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = id
			}
			items = append(items, DiscoveredModel{ID: id, DisplayName: name, OwnedBy: strings.TrimSpace(model.OwnedBy)})
		}
		if len(items) > maxDiscoveredModels {
			return nil, discoveryError(errInvalidDiscovery)
		}
		if !page.HasMore {
			return normalizeDiscoveredModels(items, apiKey), nil
		}
		next := strings.TrimSpace(page.LastID)
		if next == "" && len(page.Data) > 0 {
			next = strings.TrimSpace(page.Data[len(page.Data)-1].ID)
		}
		if next == "" || seenPages[next] {
			return nil, discoveryError(errInvalidDiscovery)
		}
		seenPages[next] = true
		after = next
	}
	return nil, discoveryError(errInvalidDiscovery)
}

func (c *Client) discoverAnthropic(ctx context.Context, baseURL, apiKey string) ([]DiscoveredModel, error) {
	items := make([]DiscoveredModel, 0)
	lastID := ""
	seenPages := make(map[string]bool)
	for pageNumber := 0; pageNumber < maxDiscoveryPages; pageNumber++ {
		endpoint, err := appendVersionedPath(baseURL, "v1", "models")
		if err != nil {
			return nil, discoveryError(err)
		}
		if lastID != "" {
			endpoint, err = addQuery(endpoint, "after_id", lastID)
			if err != nil {
				return nil, discoveryError(err)
			}
		}
		var page anthropicModelPage
		if err := c.getJSON(ctx, endpoint, map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
			"accept":            "application/json",
		}, &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, discoveryError(errInvalidDiscovery)
		}
		for _, model := range page.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = id
			}
			items = append(items, DiscoveredModel{ID: id, DisplayName: name})
		}
		if len(items) > maxDiscoveredModels {
			return nil, discoveryError(errInvalidDiscovery)
		}
		if !page.HasMore {
			return normalizeDiscoveredModels(items, apiKey), nil
		}
		if strings.TrimSpace(page.LastID) == "" || seenPages[page.LastID] {
			return nil, discoveryError(errInvalidDiscovery)
		}
		seenPages[page.LastID] = true
		lastID = page.LastID
	}
	return nil, discoveryError(errInvalidDiscovery)
}

func (c *Client) discoverGemini(ctx context.Context, baseURL, apiKey string) ([]DiscoveredModel, error) {
	items := make([]DiscoveredModel, 0)
	pageToken := ""
	seenPages := make(map[string]bool)
	for pageNumber := 0; pageNumber < maxDiscoveryPages; pageNumber++ {
		endpoint, err := geminiModelsEndpoint(baseURL)
		if err != nil {
			return nil, discoveryError(err)
		}
		if pageToken != "" {
			endpoint, err = addQuery(endpoint, "pageToken", pageToken)
			if err != nil {
				return nil, discoveryError(err)
			}
		}
		var page geminiModelPage
		if err := c.getJSON(ctx, endpoint, map[string]string{
			"x-goog-api-key": apiKey,
			"accept":         "application/json",
		}, &page); err != nil {
			return nil, err
		}
		if page.Models == nil {
			return nil, discoveryError(errInvalidDiscovery)
		}
		for _, model := range page.Models {
			if len(model.SupportedGenerationMethods) > 0 && !containsMethod(model.SupportedGenerationMethods, "generateContent") {
				continue
			}
			id := strings.TrimSpace(model.Name)
			id = strings.TrimPrefix(id, "models/")
			if id == "" {
				continue
			}
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = id
			}
			items = append(items, DiscoveredModel{ID: id, DisplayName: name})
		}
		if len(items) > maxDiscoveredModels {
			return nil, discoveryError(errInvalidDiscovery)
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			return normalizeDiscoveredModels(items, apiKey), nil
		}
		if seenPages[page.NextPageToken] {
			return nil, discoveryError(errInvalidDiscovery)
		}
		seenPages[page.NextPageToken] = true
		pageToken = page.NextPageToken
	}
	return nil, discoveryError(errInvalidDiscovery)
}

func (c *Client) discoverOllama(ctx context.Context, baseURL, apiKey string) ([]DiscoveredModel, error) {
	endpoint, err := ollamaTagsEndpoint(baseURL)
	if err != nil {
		return nil, discoveryError(err)
	}
	var page ollamaModelPage
	if err := c.getJSON(ctx, endpoint, bearerHeaders(apiKey), &page); err != nil {
		return nil, err
	}
	if page.Models == nil || len(page.Models) > maxDiscoveredModels {
		return nil, discoveryError(errInvalidDiscovery)
	}
	items := make([]DiscoveredModel, 0, len(page.Models))
	for _, model := range page.Models {
		id := strings.TrimSpace(model.Name)
		if id == "" {
			continue
		}
		items = append(items, DiscoveredModel{ID: id, DisplayName: id})
	}
	return normalizeDiscoveredModels(items, apiKey), nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, headers map[string]string, target any) error {
	client := c.http
	if client == nil {
		return discoveryError(errInvalidDiscovery)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return discoveryError(err)
	}
	for name, value := range headers {
		if strings.TrimSpace(value) != "" {
			request.Header.Set(name, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Code: "upstream_timeout", Message: safeMessage("upstream_timeout")}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return &Error{Code: "request_cancelled", Message: safeMessage("request_cancelled")}
		}
		if errors.Is(err, engine.ErrResponseTooLarge) {
			return &Error{Code: "response_too_large", Message: safeMessage("response_too_large")}
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return &Error{Code: "upstream_timeout", Message: safeMessage("upstream_timeout")}
		}
		return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return discoveryHTTPError(response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDiscoveryResponseBytes+1))
	if err != nil {
		if errors.Is(err, engine.ErrResponseTooLarge) || int64(len(data)) > maxDiscoveryResponseBytes {
			return &Error{Code: "response_too_large", Message: safeMessage("response_too_large")}
		}
		return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
	}
	if int64(len(data)) > maxDiscoveryResponseBytes {
		return &Error{Code: "response_too_large", Message: safeMessage("response_too_large")}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
	}
	return nil
}

func discoveryHTTPError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Code: "model_auth_failed", Message: safeMessage("model_auth_failed")}
	case http.StatusTooManyRequests:
		return &Error{Code: "upstream_rate_limited", Message: safeMessage("upstream_rate_limited")}
	default:
		return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
	}
}

func discoveryError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "upstream_timeout", Message: safeMessage("upstream_timeout")}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: "request_cancelled", Message: safeMessage("request_cancelled")}
	}
	return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
}

func bearerHeaders(apiKey string) map[string]string {
	if strings.TrimSpace(apiKey) == "" {
		return map[string]string{}
	}
	return map[string]string{"authorization": "Bearer " + apiKey, "accept": "application/json"}
}

func appendPath(raw, suffix string) (string, error) {
	if err := engine.ValidateEndpoint(raw); err != nil || strings.TrimSpace(raw) == "" {
		return "", errors.New("服务地址无效")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func geminiModelsEndpoint(raw string) (string, error) {
	return appendVersionedPath(raw, "v1beta", "models")
}

func ollamaTagsEndpoint(raw string) (string, error) {
	if err := engine.ValidateEndpoint(raw); err != nil || strings.TrimSpace(raw) == "" {
		return "", errors.New("服务地址无效")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/api") {
		parsed.Path = path + "/tags"
	} else {
		parsed.Path = path + "/api/tags"
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func appendVersionedPath(raw, version, suffix string) (string, error) {
	if err := engine.ValidateEndpoint(raw); err != nil || strings.TrimSpace(raw) == "" {
		return "", errors.New("服务地址无效")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	versionPath := "/" + strings.Trim(version, "/")
	if !strings.HasSuffix(path, versionPath) {
		path += versionPath
	}
	parsed.Path = path + "/" + strings.TrimLeft(suffix, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func addQuery(raw, key, value string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func containsMethod(methods []string, target string) bool {
	for _, method := range methods {
		if method == target {
			return true
		}
	}
	return false
}

func normalizeDiscoveredModels(items []DiscoveredModel, apiKey string) []DiscoveredModel {
	seen := make(map[string]bool, len(items))
	result := make([]DiscoveredModel, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || len(item.ID) > 512 || len(item.DisplayName) > 512 || len(item.OwnedBy) > 512 || seen[item.ID] {
			continue
		}
		if apiKey != "" && (strings.Contains(item.ID, apiKey) || strings.Contains(item.DisplayName, apiKey) || strings.Contains(item.OwnedBy, apiKey)) {
			continue
		}
		if strings.TrimSpace(item.DisplayName) == "" {
			item.DisplayName = item.ID
		}
		seen[item.ID] = true
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i].ID) < strings.ToLower(result[j].ID) })
	return result
}
