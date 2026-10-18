package main

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// runAgent 让 agent 分步推理并调用工具完成作答，返回最终输出文本。
// messages 中的用户消息由调用方提供，系统提示词取自配置的答题提示词。
func (s *Solver) runAgent(ctx context.Context, cfg fileConfig, service ServiceConfig, messages []chatMessage) (string, error) {
	base, err := s.models.newChatModel(ctx, service, cfg.Answer.Answer, 0.1, 0)
	if err != nil {
		return "", modelError(err)
	}
	chatModel, ok := base.(model.ToolCallingChatModel)
	if !ok {
		return "", errors.New("当前协议不支持工具调用")
	}
	tools := []tool.BaseTool{}
	if s.store != nil {
		tools = append(tools, &historyTool{store: s.store})
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "答题助手",
		Instruction:   cfg.Answer.SystemPrompt,
		Model:         chatModel,
		MaxIterations: cfg.Answer.Agent.MaxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return "", err
	}
	input, err := toEinoMessages(messages)
	if err != nil {
		return "", err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iterator := runner.Run(ctx, input)
	final := ""
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		if variant.Role == schema.Assistant && strings.TrimSpace(variant.Message.Content) != "" {
			final = variant.Message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return "", errors.New("模型未返回结果")
	}
	return final, nil
}
