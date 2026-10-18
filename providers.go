package main

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

// chatModel 是各协议模型客户端的公共接口。
type chatModel = model.BaseChatModel

var statusCodePattern = regexp.MustCompile(`(?i)\b(4[0-9]{2}|5[0-9]{2})\b`)

// validateEndpoint 校验服务地址：必须为 http(s)、无凭据与查询串。
func validateEndpoint(raw string) error {
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

// newChatModel 按服务协议构造 eino 模型客户端。
func (c *ModelClient) newChatModel(ctx context.Context, service ServiceConfig, model ModelConfig, temperature float64, tokens int) (chatModel, error) {
	switch service.Protocol {
	case ProtocolOpenAIChat:
		config := &einoopenai.ChatModelConfig{
			BaseURL: service.BaseURL, APIKey: service.APIKey, Model: model.Model,
			HTTPClient: c.http,
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
			APIKey: service.APIKey, Model: model.Model, MaxTokens: 2048, HTTPClient: c.http,
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
			APIKey: service.APIKey, HTTPClient: c.http, HTTPOptions: genai.HTTPOptions{BaseURL: service.BaseURL},
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
		httpClient := c.http
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

// toEinoMessages 把内部消息结构转换为 eino 消息。
func toEinoMessages(messages []chatMessage) ([]*schema.Message, error) {
	result := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		switch content := message.Content.(type) {
		case string:
			result = append(result, &schema.Message{Role: schema.RoleType(message.Role), Content: content})
		case []contentPart:
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

// splitDataURL 拆分 data URL 为 MIME 类型与 base64 数据。
func splitDataURL(raw string) (string, string, error) {
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

// modelError 把各家 SDK 的错误整理为统一文案。
func modelError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *einoopenai.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return fmt.Errorf("模型请求失败 (%d): %s", apiErr.HTTPStatusCode, apiErr.Message)
		}
		return fmt.Errorf("模型请求失败 (%d)", apiErr.HTTPStatusCode)
	}
	if match := statusCodePattern.FindString(err.Error()); match != "" {
		return fmt.Errorf("模型请求失败 (%s): %s", match, err.Error())
	}
	return fmt.Errorf("模型请求失败: %w", err)
}
