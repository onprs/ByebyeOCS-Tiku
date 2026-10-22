package answer

import "github.com/onprs/OCS-Tiku/internal/engine"

// ExtractAnswer 使用共享核心解析答案；platform 不维护第二套解析规则。
func ExtractAnswer(content, reasoning, kind string) string {
	return engine.ExtractAnswer(content, reasoning, kind)
}
