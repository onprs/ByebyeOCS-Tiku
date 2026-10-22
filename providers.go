package main

import (
	"context"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

// 上游配置类型直接使用共享核心，避免 v1 与 platform v2 各维护一套协议定义。
type ServiceConfig = engine.ServiceConfig
type ModelConfig = engine.ModelConfig

const (
	ProtocolOpenAIChat        = engine.ProtocolOpenAIChat
	ProtocolAnthropicMessages = engine.ProtocolAnthropicMessages
	ProtocolGeminiGenerate    = engine.ProtocolGeminiGenerate
	ProtocolOllamaChat        = engine.ProtocolOllamaChat

	CapabilityAuto = engine.CapabilityAuto
	CapabilityOn   = engine.CapabilityOn
	CapabilityOff  = engine.CapabilityOff
)

// chatModel 是各协议模型客户端的公共接口。
type chatModel = engine.ChatModel

var validateEndpoint = engine.ValidateEndpoint

// newChatModel 按服务协议构造 eino 模型客户端。
func (c *ModelClient) newChatModel(ctx context.Context, service ServiceConfig, model ModelConfig, temperature float64, tokens int) (chatModel, error) {
	return c.Client.NewChatModel(ctx, service, model, temperature, tokens)
}

// toEinoMessages 把内部消息结构转换为 eino 消息。
var toEinoMessages = engine.ToEinoMessages

// modelError 把各家 SDK 的错误整理为统一文案。
var modelError = engine.ModelError
