// Package engine 是上游 OCS-Tiku 与 ByebyeOCS platform v2 共用的模型核心。
//
// 协议选择、消息编码、图片预处理、答案解析、能力画像和用量回调都在这里；
// 上游本地服务与正式 platform 只提供各自的存储和 HTTP 边界。
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/gemini"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
)

// 协议标识，决定使用哪套调用组件与消息编码。
const (
	ProtocolOpenAIChat        = "openai-chat"
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolGeminiGenerate    = "gemini-generate-content"
	ProtocolOllamaChat        = "ollama-chat"
)

// 能力标注取值：auto 表示使用模型库推断结果，on/off 表示强制覆盖。
const (
	CapabilityAuto = "auto"
	CapabilityOn   = "on"
	CapabilityOff  = "off"
)

// Protocols 是全部可用协议。
var Protocols = []string{
	ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolGeminiGenerate, ProtocolOllamaChat,
}

// ServiceConfig 是一个上游服务条目：协议、端点与凭据。
type ServiceConfig struct {
	Name     string `json:"name" yaml:"name"`
	Protocol string `json:"protocol" yaml:"protocol"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
	Vendor   string `json:"vendor,omitempty" yaml:"vendor,omitempty"`
}

// ModelConfig 是一个上游模型选择：引用服务条目并指定模型与能力标注。
type ModelConfig struct {
	Service         string `json:"service" yaml:"service"`
	Model           string `json:"model" yaml:"model"`
	Vision          string `json:"vision" yaml:"vision"`
	ToolCall        string `json:"tool_call" yaml:"tool_call"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
}

// ChatModel 是各协议模型客户端的公共接口。
type ChatModel = model.BaseChatModel

// Error 携带稳定的错误分类和可展示文案；不包含密钥或完整上游响应。
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

var statusCodePattern = regexp.MustCompile(`(?i)\b(4[0-9]{2}|5[0-9]{2})\b`)

// ValidateEndpoint 校验服务地址：必须为 http(s)、无凭据与查询串。
func ValidateEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" ||
		target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Opaque != "" {
		return errors.New("服务地址无效")
	}
	return nil
}

// NewChatModel 按服务协议构造 eino 模型客户端。
func (c *Client) NewChatModel(ctx context.Context, service ServiceConfig, model ModelConfig, temperature float64, tokens int) (ChatModel, error) {
	switch service.Protocol {
	case ProtocolOpenAIChat:
		config := &einoopenai.ChatModelConfig{
			BaseURL: service.BaseURL, APIKey: service.APIKey, Model: model.Model,
			HTTPClient: c.HTTP,
		}
		if service.Vendor == "deepseek" {
			// DeepSeek 在 OpenAI 协议上以 thinking 字段开启思考模式。
			config.ReasoningEffort = einoopenai.ReasoningEffortLevel(model.ReasoningEffort)
			config.ExtraFields = map[string]any{"thinking": map[string]string{"type": "enabled"}}
		} else {
			value := float32(temperature)
			config.Temperature = &value
		}
		if tokens > 0 {
			config.MaxTokens = &tokens
		}
		return einoopenai.NewChatModel(ctx, config)
	case ProtocolAnthropicMessages:
		base := service.BaseURL
		config := &claude.Config{
			APIKey: service.APIKey, Model: model.Model, MaxTokens: 2048, HTTPClient: c.HTTP,
		}
		if base != "" {
			config.BaseURL = &base
		}
		if tokens > 0 {
			config.MaxTokens = tokens
		}
		value := float32(temperature)
		config.Temperature = &value
		return claude.NewChatModel(ctx, config)
	case ProtocolGeminiGenerate:
		client, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: service.APIKey, HTTPClient: c.HTTP, HTTPOptions: genai.HTTPOptions{BaseURL: service.BaseURL},
		})
		if err != nil {
			return nil, err
		}
		config := &gemini.Config{Client: client, Model: model.Model}
		value := float32(temperature)
		config.Temperature = &value
		if tokens > 0 {
			config.MaxTokens = &tokens
		}
		return gemini.NewChatModel(ctx, config)
	case ProtocolOllamaChat:
		httpClient := c.HTTP
		if temperature == 0 {
			copyClient := *httpClient
			base := copyClient.Transport
			if base == nil {
				base = http.DefaultTransport
			}
			copyClient.Transport = ollamaZeroTemperatureTransport{base: base}
			httpClient = &copyClient
		}
		config := &ollama.ChatModelConfig{
			BaseURL: service.BaseURL, Model: model.Model, HTTPClient: httpClient,
			Options: &ollama.Options{Temperature: float32(temperature), NumPredict: tokens},
		}
		return ollama.NewChatModel(ctx, config)
	default:
		return nil, fmt.Errorf("未知协议: %s", service.Protocol)
	}
}

// ollamaZeroTemperatureTransport 补上 SDK 因 omitempty 丢失的零温度。
type ollamaZeroTemperatureTransport struct{ base http.RoundTripper }

func (t ollamaZeroTemperatureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/api/chat") || request.Body == nil {
		return t.base.RoundTrip(request)
	}
	defer request.Body.Close()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	options := map[string]json.RawMessage{}
	if raw := payload["options"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, err
		}
	}
	options["temperature"] = json.RawMessage("0")
	rawOptions, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	payload["options"] = rawOptions
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	copyRequest := request.Clone(request.Context())
	copyRequest.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	copyRequest.Body, _ = copyRequest.GetBody()
	copyRequest.ContentLength = int64(len(body))
	return t.base.RoundTrip(copyRequest)
}

// ToEinoMessages 把内部消息结构转换为 eino 消息。
func ToEinoMessages(messages []ChatMessage) ([]*schema.Message, error) {
	result := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		switch content := message.Content.(type) {
		case string:
			result = append(result, &schema.Message{Role: schema.RoleType(message.Role), Content: content})
		case []ContentPart:
			parts := make([]schema.MessageInputPart, 0, len(content))
			for _, part := range content {
				switch part.Type {
				case "text":
					parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: part.Text})
				case "image_url":
					if part.ImageURL == nil {
						continue
					}
					mime, data, err := splitDataURL(part.ImageURL.URL)
					if err != nil {
						return nil, err
					}
					parts = append(parts, schema.MessageInputPart{
						Type: schema.ChatMessagePartTypeImageURL,
						Image: &schema.MessageInputImage{
							MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: mime},
						},
					})
				}
			}
			result = append(result, &schema.Message{Role: schema.RoleType(message.Role), UserInputMultiContent: parts})
		default:
			return nil, errors.New("消息格式无效")
		}
	}
	return result, nil
}

// SplitDataURL 拆分 data URL 为 MIME 类型与 base64 数据。
func SplitDataURL(raw string) (string, string, error) {
	rest, ok := strings.CutPrefix(raw, "data:")
	if !ok {
		return "", "", errors.New("图片数据无效")
	}
	mime, data, ok := strings.Cut(rest, ";base64,")
	if !ok {
		return "", "", errors.New("图片数据无效")
	}
	return mime, data, nil
}

func splitDataURL(raw string) (string, string, error) { return SplitDataURL(raw) }

// ErrResponseTooLarge 表示上游响应超过 platform 的有界读取上限。
var ErrResponseTooLarge = errors.New("模型响应超过大小上限")

// ModelError 把各家 SDK 的错误整理为统一分类和安全文案。
// 原始错误只保留在 Err 中供内部排查，不进入 Error() 或用户响应。
func ModelError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrResponseTooLarge) {
		return &Error{Code: "response_too_large", Message: safeModelMessage("response_too_large"), Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "upstream_timeout", Message: safeModelMessage("upstream_timeout"), Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: "request_cancelled", Message: safeModelMessage("request_cancelled"), Err: err}
	}
	var apiErr *einoopenai.APIError
	if errors.As(err, &apiErr) {
		code := codeForStatus(apiErr.HTTPStatusCode)
		return &Error{Code: code, Message: safeModelMessage(code), Err: err}
	}
	if match := statusCodePattern.FindString(err.Error()); match != "" {
		code := codeForStatusText(match)
		return &Error{Code: code, Message: safeModelMessage(code), Err: err}
	}
	return &Error{Code: "model_unavailable", Message: safeModelMessage("model_unavailable"), Err: err}
}

// safeModelMessage 只返回固定文案，不回显上游响应、URL、密钥或题干。
func safeModelMessage(code string) string {
	switch code {
	case "model_auth_failed":
		return "模型服务认证失败"
	case "upstream_rate_limited":
		return "模型服务限流，请稍后人工重试"
	case "upstream_timeout":
		return "模型调用超时，结果未知"
	case "request_cancelled":
		return "请求已取消，模型结果未知"
	case "response_too_large":
		return "模型响应超过大小上限"
	case "invalid_request":
		return "模型请求参数无效"
	case "model_unavailable":
		return "模型服务不可用"
	default:
		return "模型调用失败"
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "model_auth_failed"
	case http.StatusTooManyRequests:
		return "upstream_rate_limited"
	default:
		return "model_unavailable"
	}
}

func codeForStatusText(status string) string {
	switch status {
	case "401", "403":
		return "model_auth_failed"
	case "429":
		return "upstream_rate_limited"
	default:
		return "model_unavailable"
	}
}
