package engine

import "testing"

func TestExtractAnswerShortAnswerKeepsFullContent(t *testing.T) {
	cases := []struct{ content, want string }{
		{"<answer>光合作用需要光能、水和叶绿体。\n温度也会影响。</answer>", "光合作用需要光能、水和叶绿体。\n温度也会影响。"},
		{"答案包含 A 和 B，不能只保留字母。", "答案包含 A 和 B，不能只保留字母。"},
		{"<answer>A 和 B 都是正确答案</answer>", "A 和 B 都是正确答案"},
	}
	for _, item := range cases {
		if got := ExtractAnswer(item.content, "", "shortanswer"); got != item.want {
			t.Errorf("ExtractAnswer(%q, shortanswer)=%q，期望 %q", item.content, got, item.want)
		}
	}
	// 单选仍按字母收敛，简答题不套用该规则。
	if got := ExtractAnswer("<answer>A 和 B</answer>", "", "single"); got != "A" {
		t.Fatalf("单选答案解析异常: %q", got)
	}
}
