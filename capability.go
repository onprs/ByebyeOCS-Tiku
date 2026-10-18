package main

import (
	"github.com/onprs/OCS-Tiku/internal/catalog"
)

// Capability 是模型能力的最终判定结果。
type Capability struct {
	Vision    bool   `json:"vision"`
	ToolCall  bool   `json:"tool_call"`
	Reasoning bool   `json:"reasoning"`
	Context   int    `json:"context"`
	Matched   bool   `json:"matched"` // 是否在模型库中找到条目
	Method    string `json:"method"`  // 匹配方式
	Entry     string `json:"entry"`   // 命中的条目标识
}

// capability 合并模型库推断与用户标注，用户标注优先。
func (s *Solver) capability(service ServiceConfig, model ModelConfig) Capability {
	result := Capability{}
	if index := s.catalogOf(); index != nil {
		if match := index.Lookup(service.Vendor, model.Model); match.Found {
			result = Capability{
				Vision: match.Entry.Vision, ToolCall: match.Entry.ToolCall,
				Reasoning: match.Entry.Reasoning, Context: match.Entry.Context,
				Matched: true, Method: match.Method, Entry: match.Entry.ID,
			}
		}
	}
	switch model.Vision {
	case CapabilityOn:
		result.Vision = true
	case CapabilityOff:
		result.Vision = false
	}
	switch model.ToolCall {
	case CapabilityOn:
		result.ToolCall = true
	case CapabilityOff:
		result.ToolCall = false
	}
	return result
}

// catalogOf 返回当前模型库索引，未初始化时返回空。
func (s *Solver) catalogOf() *catalog.Catalog {
	if s.catalog == nil {
		return nil
	}
	return s.catalog.Catalog()
}

// resolveCapability 供设置接口使用，返回指定模型的能力判定。
func resolveCapability(store *catalog.Store, service ServiceConfig, model ModelConfig) Capability {
	solver := &Solver{catalog: store}
	return solver.capability(service, model)
}
