package answer

import "testing"

func TestSubmissionAnswerPreservesOptionWordsAndJudgements(t *testing.T) {
	for _, test := range []struct{ content, reasoning, kind, want string }{
		{"<answer>WHO</answer>", "", "single", "WHO"},
		{"<answer>HTTP</answer>", "", "single", "HTTP"},
		{"<answer>CPU 是中央处理器</answer>", "", "single", "CPU 是中央处理器"},
		{"<answer>A 和 B</answer>", "", "single", "A 和 B"},
		{"<answer>c</answer>", "", "single", "C"},
		{"", "<answer>WHO</answer>", "single", "WHO"},
		{"<answer>true</answer>", "", "judgement", "true"},
		{"<answer>false</answer>", "", "judgement", "false"},
		{"<answer>a,c</answer>", "", "multiple", "A#C"},
		{"<answer></answer>", "", "single", ""},
	} {
		if got := extractSubmissionAnswer(test.content, test.reasoning, test.kind); got != test.want {
			t.Errorf("%s/%q: 得到 %q，期望 %q", test.kind, test.content, got, test.want)
		}
	}
	if got := ExtractAnswer("<answer>A 和 B</answer>", "", "single"); got != "A" {
		t.Fatalf("v1 兼容输出变化: %q", got)
	}
}
