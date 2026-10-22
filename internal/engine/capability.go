package engine

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

// MergeCapability 合并模型库推断与人工标注，用户标注优先。
func MergeCapability(base Capability, model ModelConfig) Capability {
	switch model.Vision {
	case CapabilityOn:
		base.Vision = true
	case CapabilityOff:
		base.Vision = false
	}
	switch model.ToolCall {
	case CapabilityOn:
		base.ToolCall = true
	case CapabilityOff:
		base.ToolCall = false
	}
	return base
}
