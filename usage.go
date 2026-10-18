package main

import (
	"context"
	"log"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
)

// callInfo 描述一次模型调用的归属，随 context 传给回调。
type callInfo struct {
	RecordID int64
	Slot     string
	Vendor   string
	Protocol string
	Model    string
}

type callInfoKey struct{}
type callStartKey struct{}
type recordIDKey struct{}

// withRecordID 在 context 中附带答题记录标识。
func withRecordID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, recordIDKey{}, id)
}

// recordIDFrom 读取 context 中的答题记录标识。
func recordIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(recordIDKey{}).(int64)
	return id
}

// withCallInfo 在 context 中附带调用归属信息。
func withCallInfo(ctx context.Context, info callInfo) context.Context {
	return context.WithValue(ctx, callInfoKey{}, info)
}

// modelContext 为一次模型调用准备带用量回调的 context。
func (s *Solver) modelContext(ctx context.Context, slot string, service ServiceConfig, model ModelConfig) context.Context {
	ctx = withCallInfo(ctx, callInfo{
		RecordID: recordIDFrom(ctx), Slot: slot, Vendor: service.Vendor,
		Protocol: service.Protocol, Model: model.Model,
	})
	// RunInfo 传 nil，让各组件在调用时填充自身标识，回调据此区分组件类型。
	return callbacks.InitCallbacks(ctx, nil, s.usageHandler())
}

// usageHandler 把模型调用的耗时与用量写入数据库。
// 只处理 ChatModel 组件的回调，Agent 与工具节点不产生用量记录。
func (s *Solver) usageHandler() callbacks.Handler {
	isModel := func(info *callbacks.RunInfo) bool {
		return info != nil && info.Component == components.ComponentOfChatModel
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
				s.saveCall(ctx, model.ConvCallbackOutput(output), "")
			}
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			if isModel(info) {
				s.saveCall(ctx, nil, err.Error())
			}
			return ctx
		}).
		Build()
}

// saveCall 记录一次模型调用结果，失败不影响答题流程。
func (s *Solver) saveCall(ctx context.Context, output *model.CallbackOutput, errText string) {
	info, ok := ctx.Value(callInfoKey{}).(callInfo)
	if !ok || info.RecordID == 0 || s.store == nil {
		return
	}
	now := time.Now()
	start, hasStart := ctx.Value(callStartKey{}).(time.Time)
	if !hasStart {
		start = now
	}
	call := ModelCall{
		RecordID: info.RecordID, Slot: info.Slot, Vendor: info.Vendor,
		Protocol: info.Protocol, Model: info.Model, Status: "ok",
		LatencyMS: now.Sub(start).Milliseconds(),
		Timestamp: start.Format("2006-01-02T15:04:05.000000"),
	}
	if errText != "" {
		call.Status = "error"
		call.Error = truncateText(errText, 500)
	}
	if output != nil && output.TokenUsage != nil {
		call.PromptTokens = output.TokenUsage.PromptTokens
		call.CompletionTokens = output.TokenUsage.CompletionTokens
		call.TotalTokens = output.TokenUsage.TotalTokens
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.addModelCall(saveCtx, call); err != nil {
		log.Printf("记录模型调用失败: %v", err)
	}
}
