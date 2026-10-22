package normalize

import "testing"

func TestCacheKeyExactAndOrderSensitive(t *testing.T) {
	images := []Image{{Index: 1, Data: []byte("a"), Digest: "a"}}
	a := CacheKey("m", "p1", "n1", "user", "scope", "题目", "A. 甲\nB. 乙", "single", images)
	b := CacheKey("m", "p1", "n1", "user", "scope", "题目", "A. 甲\nB. 乙", "single", images)
	if a != b {
		t.Fatal("相同输入必须得到相同缓存键")
	}
	c := CacheKey("m", "p1", "n1", "user", "scope", "题目", "B. 乙\nA. 甲", "single", images)
	if a == c {
		t.Fatal("选项顺序不同不能命中同一缓存")
	}
	d := CacheKey("m", "p1", "n1", "user", "other-scope", "题目", "A. 甲\nB. 乙", "single", images)
	if a == d {
		t.Fatal("缓存范围不同不能命中同一缓存")
	}
	e := CacheKey("m2", "p1", "n1", "user", "scope", "题目", "A. 甲\nB. 乙", "single", images)
	if a == e {
		t.Fatal("模型不同不能命中同一缓存")
	}
	f := CacheKey("m", "p1", "n1", "user", "scope", "题目", "A. 甲\nB. 乙", "shortanswer", images)
	if a == f {
		t.Fatal("题型不同不能命中同一缓存")
	}
}

func TestImagesDigestOrder(t *testing.T) {
	one := ImagesDigest([]Image{{Index: 1, Data: []byte("a")}, {Index: 2, Data: []byte("b")}})
	two := ImagesDigest([]Image{{Index: 1, Data: []byte("b")}, {Index: 2, Data: []byte("a")}})
	if one == two {
		t.Fatal("图片顺序不同必须有不同摘要")
	}
}

func TestCanonicalText(t *testing.T) {
	if got := CanonicalText(" <p>你&nbsp;好</p>\n  世界 "); got != "你 好 世界" {
		t.Fatalf("规范化结果不符：%q", got)
	}
	if got := CanonicalOptions("A. 甲 \r\nB. 乙\r\n"); got != "A. 甲\nB. 乙" {
		t.Fatalf("选项规范化结果不符：%q", got)
	}
}

func TestPrepareShortAnswer(t *testing.T) {
	question, err := Prepare(" 简述光合作用的条件。 ", "", "ShortAnswer", nil, Limits{MaxImages: 8, MaxImageBytes: 1 << 20, MaxImagePixels: 4_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if question.Type != "shortanswer" || question.TitleText != "简述光合作用的条件。" {
		t.Fatalf("简答题规范化异常: %+v", question)
	}
	if _, err := Prepare("未知题型", "", "essay", nil, Limits{MaxImages: 8, MaxImageBytes: 1 << 20, MaxImagePixels: 4_000_000}); err == nil {
		t.Fatal("未知题型应被拒绝")
	}
}
