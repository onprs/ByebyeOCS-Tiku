package answer

import "testing"

func TestExtractAnswer(t *testing.T) {
	cases := []struct{ content, kind, want string }{
		{"思考后 <answer>B</answer>", "single", "B"},
		{"<answer>a,c</answer>", "multiple", "A#C"},
		{"<answer>对</answer>", "judgement", "对"},
		{"<answer>光合作用</answer>", "completion", "光合作用"},
		{"<answer>光合作用需要光能、水和叶绿体。\n温度也会影响。</answer>", "shortanswer", "光合作用需要光能、水和叶绿体。\n温度也会影响。"},
		{"答案包含 A 和 B，不能只保留字母。", "shortanswer", "答案包含 A 和 B，不能只保留字母。"},
		{"没有标签 A", "single", "没有标签 A"},
		{"<answer></answer>", "single", ""},
	}
	for _, item := range cases {
		if got := ExtractAnswer(item.content, "", item.kind); got != item.want {
			t.Errorf("ExtractAnswer(%q,%q)=%q，期望 %q", item.content, item.kind, got, item.want)
		}
	}
}
