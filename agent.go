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

// runAgent 让 agent 分步推理并调用工具完成作答，返回最终输出文本与思考内容。
// messages 中的用户消息由调用方提供，系统提示词取自配置的答题提示词。
func (s *Solver) runAgent(ctx context.Context, cfg fileConfig, service ServiceConfig, messages []chatMessage) (string, string, error) {
	base, err := s.models.newChatModel(ctx, service, cfg.Answer.Answer, cfg.Answer.Temperature, 0)
	if err != nil {
		return "", "", modelError(err)
	}
	chatModel, ok := base.(model.ToolCallingChatModel)
	if !ok {
		return "", "", errors.New("当前协议不支持工具调用")
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
		return "", "", err
	}
	input, err := toEinoMessages(messages)
	if err != nil {
		return "", "", err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iterator := runner.Run(ctx, input)
	final, reasoning := "", ""
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		if variant.Role != schema.Assistant {
			continue
		}
		message, err := variant.GetMessage()
		if err != nil {
			return "", "", err
		}
		if message == nil || len(message.ToolCalls) > 0 {
			// 请求工具的消息属于中间步骤，不作为最终回答。
			continue
		}
		// 最后一轮无工具调用的消息覆盖此前内容，空白回复会触发回退。
		final, reasoning = message.Content, message.ReasoningContent
	}
	if strings.TrimSpace(final) == "" && strings.TrimSpace(reasoning) == "" {
		return "", "", errors.New("模型未返回结果")
	}
	return final, reasoning, nil
}

// agentErrorText 生成可展示的 agent 回退原因，折叠换行并截断内部细节。
func agentErrorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, adk.ErrExceedMaxIterations) {
		return "推理步数超出上限"
	}
	return truncateText(strings.Join(strings.Fields(err.Error()), " "), 200)
}
