package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Client 持有共享的 HTTP 客户端，按服务协议发起模型调用。
type Client struct {
	// HTTP 由调用方提供，便于应用超时、TLS 根证书和网络边界策略。
	HTTP *http.Client
}

// NewClient 创建与上游一致的超时客户端。
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// NewClientWithHTTP 使用调用方配置的 HTTP 客户端。
func NewClientWithHTTP(client *http.Client) *Client {
	if client == nil {
		return NewClient()
	}
	return &Client{HTTP: client}
}

// ChatMessage 是一条模型消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ContentPart 是多模态消息的一部分。
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 是 data URL 形式的图片。
type ImageURL struct {
	URL string `json:"url"`
}

// TextPart 构造文本片段。
func TextPart(s string) ContentPart { return ContentPart{Type: "text", Text: s} }

// ImagePart 构造内联图片片段。
func ImagePart(data []byte) ContentPart {
	mime := http.DetectContentType(data)
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}
}

// TokenUsage 是服务端返回的 token 事实。Present 表示上游是否给出 usage；
// Complete 表示字段齐全且 total 等于 input + output。缺失时不补 0。
type TokenUsage struct {
	Present      bool
	Complete     bool
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// Result 是一次模型调用的正文、推理内容和用量。
type Result struct {
	Content   string
	Reasoning string
	Usage     TokenUsage
	Latency   time.Duration
}

// Chat 调用模型并返回正文、推理内容与真实 token 用量。
func (c *Client) Chat(ctx context.Context, service ServiceConfig, model ModelConfig, messages []ChatMessage, temperature float64, tokens int) (Result, error) {
	start := time.Now()
	if service.APIKey == "" && service.Protocol != ProtocolOllamaChat {
		return Result{}, &Error{Code: "model_auth_failed", Message: "请先在设置中填写模型密钥"}
	}
	if err := ValidateEndpoint(service.BaseURL); err != nil {
		return Result{}, &Error{Code: "model_unavailable", Message: err.Error(), Err: err}
	}
	client, err := c.NewChatModel(ctx, service, model, temperature, tokens)
	if err != nil {
		return Result{}, ModelError(err)
	}
	input, err := ToEinoMessages(messages)
	if err != nil {
		return Result{}, &Error{Code: "invalid_request", Message: err.Error(), Err: err}
	}
	output, err := client.Generate(ctx, input, responseOptions(service)...)
	if err != nil {
		return Result{}, ModelError(err)
	}
	if output == nil {
		return Result{}, &Error{Code: "model_unavailable", Message: "模型未返回结果"}
	}
	return Result{
		Content: output.Content, Reasoning: output.ReasoningContent,
		Usage: usageFromMessage(output), Latency: time.Since(start),
	}, nil
}

const usagePresenceKey = "ocs_usage_present"

// responseOptions 让 OpenAI 兼容调用保留 raw body 的 usage 是否存在。
// eino 在缺少 usage 时仍会给出全 0 的非空指针，不能据此认定用量完整。
func responseOptions(service ServiceConfig) []einomodel.Option {
	if service.Protocol != ProtocolOpenAIChat {
		return nil
	}
	return []einomodel.Option{einoopenai.WithResponseMessageModifier(func(_ context.Context, message *schema.Message, rawBody []byte) (*schema.Message, error) {
		if message == nil {
			return message, nil
		}
		present := usageFieldsPresent(rawBody)
		if message.Extra == nil {
			message.Extra = map[string]any{}
		}
		message.Extra[usagePresenceKey] = present
		return message, nil
	})}
}

// usageFieldsPresent 要求 prompt/completion/total 三个字段都存在且非 null。
// eino 会把缺失字段补成 0；只看 usage 对象是否存在会把缺字段误判为完整。
func usageFieldsPresent(rawBody []byte) bool {
	var payload struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil || payload.Usage == nil {
		return false
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		value, ok := payload.Usage[key]
		if !ok || len(value) == 0 || string(value) == "null" {
			return false
		}
	}
	return true
}

// ChatText 保留上游只返回正文与推理内容的调用方式。
func (c *Client) ChatText(ctx context.Context, service ServiceConfig, model ModelConfig, messages []ChatMessage, temperature float64, tokens int) (string, string, error) {
	result, err := c.Chat(ctx, service, model, messages, temperature, tokens)
	if err != nil {
		return "", "", err
	}
	return result.Content, result.Reasoning, nil
}

func usageFromMessage(message *schema.Message) TokenUsage {
	if message == nil {
		return TokenUsage{}
	}
	if present, marked := message.Extra[usagePresenceKey].(bool); marked && !present {
		return TokenUsage{}
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return TokenUsage{}
	}
	usage := message.ResponseMeta.Usage
	prompt, completion, total := int64(usage.PromptTokens), int64(usage.CompletionTokens), int64(usage.TotalTokens)
	if total <= 0 {
		return TokenUsage{}
	}
	return TokenUsage{
		Present: true, Complete: prompt >= 0 && completion >= 0 && total == prompt+completion,
		InputTokens: prompt, OutputTokens: completion, TotalTokens: total,
	}
}

// Describe 使用图片转述模型把图片转换为文字描述。
func (c *Client) Describe(ctx context.Context, service ServiceConfig, model ModelConfig, prompt string, data []byte) (Result, error) {
	return c.Chat(ctx, service, model,
		[]ChatMessage{{Role: "user", Content: []ContentPart{ImagePart(data), TextPart(prompt)}}}, 0.1, 512)
}

// Test 对服务条目执行一次最小调用，用于连通性检查。withImage 为真时以图片请求验证图片能力。
func (c *Client) Test(ctx context.Context, service ServiceConfig, model ModelConfig, withImage bool) error {
	messages := []ChatMessage{{Role: "system", Content: "回复OK"}, {Role: "user", Content: "OK"}}
	if withImage {
		img := image.NewRGBA(image.Rect(0, 0, 100, 30))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			return err
		}
		messages = []ChatMessage{{Role: "user", Content: []ContentPart{ImagePart(buffer.Bytes()), TextPart("What text do you see? Reply 'OK' only.")}}}
	}
	content, _, err := c.ChatText(ctx, service, model, messages, 0.1, 512)
	if err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return &Error{Code: "empty_answer", Message: "模型未返回结果"}
	}
	return nil
}

// Reachability 只检查 OpenAI 兼容端点的网络与认证可达性，不产生回答 token。
// 其他协议没有统一的无 token 探测端点，调用方应改用显式的功能性测试。
func (c *Client) Reachability(ctx context.Context, service ServiceConfig) error {
	if service.Protocol != ProtocolOpenAIChat {
		return &Error{Code: "unsupported_protocol", Message: "该协议不支持无 token 可达性检查"}
	}
	if err := ValidateEndpoint(service.BaseURL); err != nil {
		return &Error{Code: "model_unavailable", Message: err.Error(), Err: err}
	}
	requestURL := strings.TrimRight(service.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return &Error{Code: "model_unavailable", Message: "无法构造可达性请求", Err: err}
	}
	if service.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+service.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Code: "upstream_timeout", Message: "可达性检查超时", Err: err}
		}
		return &Error{Code: "model_unavailable", Message: "模型服务不可达", Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &Error{Code: "model_auth_failed", Message: "模型服务认证失败"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{Code: "model_unavailable", Message: fmt.Sprintf("模型服务未通过可达性检查 (%d)", resp.StatusCode)}
	}
	return nil
}
