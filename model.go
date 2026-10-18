package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"time"
)

// ModelClient 持有共享的 HTTP 客户端，按服务协议发起模型调用。
type ModelClient struct{ http *http.Client }

func newModelClient() *ModelClient {
	return &ModelClient{http: &http.Client{Timeout: 90 * time.Second}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func textPart(s string) contentPart { return contentPart{Type: "text", Text: s} }

func imagePart(data []byte) contentPart {
	mime := http.DetectContentType(data)
	return contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}
}

// chat 调用模型并返回正文与推理内容。
func (c *ModelClient) chat(ctx context.Context, service ServiceConfig, model ModelConfig, messages []chatMessage, temperature float64, tokens int) (string, string, error) {
	if service.APIKey == "" && service.Protocol != ProtocolOllamaChat {
		return "", "", errors.New("请先在设置中填写模型密钥")
	}
	if err := validateEndpoint(service.BaseURL); err != nil {
		return "", "", err
	}
	client, err := c.newChatModel(ctx, service, model, temperature, tokens)
	if err != nil {
		return "", "", modelError(err)
	}
	input, err := toEinoMessages(messages)
	if err != nil {
		return "", "", err
	}
	output, err := client.Generate(ctx, input)
	if err != nil {
		return "", "", modelError(err)
	}
	if output == nil {
		return "", "", errors.New("模型未返回结果")
	}
	return output.Content, output.ReasoningContent, nil
}

// describe 使用图片转述模型把图片转换为文字描述。
func (c *ModelClient) describe(ctx context.Context, service ServiceConfig, model ModelConfig, prompt string, data []byte) (string, error) {
	content, _, err := c.chat(ctx, service, model,
		[]chatMessage{{Role: "user", Content: []contentPart{imagePart(data), textPart(prompt)}}}, 0.1, 512)
	return content, err
}

// test 对服务条目执行一次最小调用，用于连通性检查。withImage 为真时以图片请求验证图片能力。
func (c *ModelClient) test(ctx context.Context, service ServiceConfig, model ModelConfig, withImage bool) error {
	messages := []chatMessage{{Role: "system", Content: "回复OK"}, {Role: "user", Content: "OK"}}
	if withImage {
		img := image.NewRGBA(image.Rect(0, 0, 100, 30))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			return err
		}
		messages = []chatMessage{{Role: "user", Content: []contentPart{imagePart(buffer.Bytes()), textPart("What text do you see? Reply 'OK' only.")}}}
	}
	content, _, err := c.chat(ctx, service, model, messages, 0.1, 512)
	if err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("模型未返回结果")
	}
	return nil
}
