package answer

import (
	"strings"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

// ExtractAnswer 使用共享核心解析答案；platform 不维护第二套解析规则。
func ExtractAnswer(content, reasoning, kind string) string {
	return engine.ExtractAnswer(content, reasoning, kind)
}

// extractSubmissionAnswer 保留 v2 单选/判断的完整正文，避免 WHO、HTTP、true
// 等答案被旧字母规则截断；最终映射由平台依据真实选项做确定性核对。
// v1 仍沿用 ExtractAnswer 的既有协议。
func extractSubmissionAnswer(content, reasoning, kind string) string {
	if kind == "single" || kind == "judgement" {
		raw := engine.ExtractAnswerText(content, reasoning)
		if kind == "single" && len(raw) == 1 && (raw[0] >= 'a' && raw[0] <= 'z') {
			return strings.ToUpper(raw)
		}
		return raw
	}
	return ExtractAnswer(content, reasoning, kind)
}
