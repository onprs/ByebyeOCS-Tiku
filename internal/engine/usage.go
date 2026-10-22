package engine

import (
	"context"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
)

// CallInfo 描述一次模型调用的归属，随 context 传给回调。
type CallInfo struct {
	RecordID int64
	Slot     string
	Vendor   string
	Protocol string
	Model    string
}

// CallRecord 是一次模型调用的用量与状态事实。
type CallRecord struct {
	Info             CallInfo
	Status           string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Usage            TokenUsage
	Latency          time.Duration
	Error            string
}

// CallObserver 接收模型调用事实；实现方负责持久化或转发。
type CallObserver func(context.Context, CallRecord)

type callInfoKey struct{}
type callStartKey struct{}

// WithCallInfo 在 context 中附带调用归属信息。
func WithCallInfo(ctx context.Context, info CallInfo) context.Context {
	return context.WithValue(ctx, callInfoKey{}, info)
}

// CallInfoFrom 读取 context 中的调用归属信息。
func CallInfoFrom(ctx context.Context) (CallInfo, bool) {
	info, ok := ctx.Value(callInfoKey{}).(CallInfo)
	return info, ok
}

// UsageHandler 把模型调用的耗时与用量交给 observer。
// 只处理 ChatModel 组件的回调，Agent 与工具节点不产生用量记录。
func UsageHandler(observe CallObserver) callbacks.Handler {
	isModel := func(info *callbacks.RunInfo) bool {
		return info != nil && info.Component == components.ComponentOfChatModel
	}
	record := func(ctx context.Context, output *model.CallbackOutput, err error) {
		if observe == nil {
			return
		}
		info, ok := CallInfoFrom(ctx)
		if !ok {
			return
		}
		now := time.Now()
		start, hasStart := ctx.Value(callStartKey{}).(time.Time)
		if !hasStart {
			start = now
		}
		result := CallRecord{Info: info, Status: "ok", Latency: now.Sub(start)}
		if err != nil {
			result.Status = "error"
			// 只保存固定安全文案，原始上游错误可能包含 URL、密钥或题干。
			result.Error = ModelError(err).Error()
		}
		if output != nil && output.TokenUsage != nil {
			result.PromptTokens = output.TokenUsage.PromptTokens
			result.CompletionTokens = output.TokenUsage.CompletionTokens
			result.TotalTokens = output.TokenUsage.TotalTokens
			result.Usage = TokenUsage{
				Present: true, Complete: result.TotalTokens == result.PromptTokens+result.CompletionTokens,
				InputTokens: int64(result.PromptTokens), OutputTokens: int64(result.CompletionTokens), TotalTokens: int64(result.TotalTokens),
			}
		}
		observe(ctx, result)
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			if !isModel(info) {
				return ctx
			}
			return context.WithValue(ctx, callStartKey{}, time.Now())
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if isModel(info) {
				record(ctx, model.ConvCallbackOutput(output), nil)
			}
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			if isModel(info) {
				record(ctx, nil, err)
			}
			return ctx
		}).
		Build()
}
