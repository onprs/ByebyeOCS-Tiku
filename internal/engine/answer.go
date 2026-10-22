package engine

import (
	"regexp"
	"strings"
)

var (
	answerPattern = regexp.MustCompile(`(?is)<answer>\s*(.*?)\s*</answer>`)
	letterPattern = regexp.MustCompile(`[A-Za-z]`)
)

// ExtractAnswer 优先读取 <answer> 标签，按题型收敛答案字符串。
// 不生成置信度，也不把无法唯一对应选项的文本当成结构化答案。
func ExtractAnswer(content, reasoning, kind string) string {
	raw := ""
	tagged := false
	for _, value := range []string{content, reasoning} {
		for _, match := range answerPattern.FindAllStringSubmatch(value, -1) {
			tagged = true
			if raw = strings.TrimSpace(match[1]); raw != "" {
				break
			}
		}
		if raw != "" {
			break
		}
	}
	if raw == "" {
		if tagged {
			return ""
		}
		if kind == "shortanswer" {
			// 简答题保留模型返回的完整文本与换行，不折叠为单行。
			return strings.TrimSpace(content)
		}
		return strings.TrimSpace(content)
	}
	if kind == "completion" || kind == "shortanswer" {
		return raw
	}
	if raw == "对" || raw == "错" {
		return raw
	}
	letters := letterPattern.FindAllString(raw, -1)
	if len(letters) > 0 {
		if kind == "multiple" {
			for i := range letters {
				letters[i] = strings.ToUpper(letters[i])
			}
			return strings.Join(letters, "#")
		}
		return strings.ToUpper(letters[0])
	}
	return raw
}
