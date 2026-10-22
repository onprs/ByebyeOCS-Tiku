package main

import (
	"context"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

// ModelClient 是共享模型客户端的薄包装，保留上游的调用入口。
type ModelClient struct{ *engine.Client }

func newModelClient() *ModelClient {
	return &ModelClient{Client: engine.NewClient()}
}

type chatMessage = engine.ChatMessage
type contentPart = engine.ContentPart
type imageURL = engine.ImageURL

func textPart(s string) contentPart { return engine.TextPart(s) }

func imagePart(data []byte) contentPart { return engine.ImagePart(data) }

// chat 调用模型并返回正文与推理内容。
func (c *ModelClient) chat(ctx context.Context, service ServiceConfig, model ModelConfig, messages []chatMessage, temperature float64, tokens int) (string, string, error) {
	return c.ChatText(ctx, service, model, messages, temperature, tokens)
}

// describe 使用图片转述模型把图片转换为文字描述。
func (c *ModelClient) describe(ctx context.Context, service ServiceConfig, model ModelConfig, prompt string, data []byte) (string, error) {
	result, err := c.Describe(ctx, service, model, prompt, data)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// test 对服务条目执行一次最小调用，用于连通性检查。
func (c *ModelClient) test(ctx context.Context, service ServiceConfig, model ModelConfig, withImage bool) error {
	return c.Test(ctx, service, model, withImage)
}
