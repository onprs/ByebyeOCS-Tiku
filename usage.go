package main

import (
	"context"
	"log"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/onprs/OCS-Tiku/internal/engine"
)

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

// modelContext 为一次模型调用准备带用量回调的 context。
func (s *Solver) modelContext(ctx context.Context, slot string, service ServiceConfig, model ModelConfig) context.Context {
	ctx = engine.WithCallInfo(ctx, engine.CallInfo{
		RecordID: recordIDFrom(ctx), Slot: slot, Vendor: service.Vendor,
		Protocol: service.Protocol, Model: model.Model,
	})
	// RunInfo 传 nil，让各组件在调用时填充自身标识，回调据此区分组件类型。
	return callbacks.InitCallbacks(ctx, nil, engine.UsageHandler(func(_ context.Context, record engine.CallRecord) {
		s.saveCall(record)
	}))
}

// saveCall 记录一次模型调用结果，失败不影响答题流程。
func (s *Solver) saveCall(record engine.CallRecord) {
	info := record.Info
	if info.RecordID == 0 || s.store == nil {
		return
	}
	start := time.Now().Add(-record.Latency)
	call := ModelCall{
		RecordID: info.RecordID, Slot: info.Slot, Vendor: info.Vendor,
		Protocol: info.Protocol, Model: info.Model, Status: record.Status,
		LatencyMS: record.Latency.Milliseconds(),
		Timestamp: start.Format("2006-01-02T15:04:05.000000"),
	}
	if record.Error != "" {
		call.Status = "error"
		call.Error = truncateText(record.Error, 500)
	}
	if record.Usage.Present {
		call.PromptTokens = int(record.Usage.InputTokens)
		call.CompletionTokens = int(record.Usage.OutputTokens)
		call.TotalTokens = int(record.Usage.TotalTokens)
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.addModelCall(saveCtx, call); err != nil {
		log.Printf("记录模型调用失败: %v", err)
	}
}
