package catalog

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	snapshot := Snapshot{
		Generated: "2026-01-01T00:00:00Z",
		Vendors: map[string]string{
			"openai": "OpenAI", "anthropic": "Anthropic", "relay": "中转",
		},
		Models: map[string]SnapshotEntry{
			"openai/gpt-4o":                        {Name: "GPT-4o", Vision: true, ToolCall: true, Context: 128000},
			"openai/gpt-4o-mini":                   {Name: "GPT-4o mini", Vision: true},
			"relay/gpt-4o":                         {Name: "GPT-4o", Vision: true, ToolCall: true},
			"anthropic/claude-3-5-sonnet":          {Name: "Claude 3.5 Sonnet", Vision: true, Canonical: "anthropic/claude-3-5-sonnet-20241022"},
			"anthropic/claude-3-5-sonnet-20241022": {Name: "Claude 3.5 Sonnet", Vision: true, ToolCall: true},
			"anthropic/claude-3-5-haiku":           {Name: "Claude 3.5 Haiku", ToolCall: true},
			"relay/deepseek-chat":                  {Name: "DeepSeek Chat", ToolCall: true},
		},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err = writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := Load(compressed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLookup(t *testing.T) {
	c := testCatalog(t)
	cases := []struct {
		name    string
		vendor  string
		model   string
		found   bool
		method  string
		entryID string
		vision  bool
	}{
		{"厂商内精确", "openai", "gpt-4o", true, "vendor", "openai/gpt-4o", true},
		{"厂商内未命中不跨厂商", "openai", "deepseek-chat", false, "", "", false},
		{"全局精确优先权威厂商", "", "gpt-4o", true, "exact", "openai/gpt-4o", true},
		{"全局精确单个候选", "", "deepseek-chat", true, "exact", "relay/deepseek-chat", false},
		{"大小写不敏感", "", "GPT-4O", true, "exact", "openai/gpt-4o", true},
		{"带前缀完整标识", "", "relay/deepseek-chat", true, "exact", "relay/deepseek-chat", false},
		{"规范标识重定向", "anthropic", "claude-3-5-sonnet", true, "canonical", "anthropic/claude-3-5-sonnet-20241022", true},
		{"日期后缀归一化", "anthropic", "claude-3-5-haiku-20241022", true, "normalized", "anthropic/claude-3-5-haiku", false},
		{"未匹配", "openai", "no-such-model", false, "", "", false},
		{"模型为空", "openai", "", false, "", "", false},
		{"厂商为空且无同名条目", "", "no-such-model", false, "", "", false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			match := c.Lookup(item.vendor, item.model)
			if match.Found != item.found {
				t.Fatalf("Found = %v，期望 %v（命中 %s）", match.Found, item.found, match.Entry.ID)
			}
			if !item.found {
				return
			}
			if match.Method != item.method {
				t.Errorf("Method = %q，期望 %q", match.Method, item.method)
			}
			if match.Entry.ID != item.entryID {
				t.Errorf("Entry.ID = %q，期望 %q", match.Entry.ID, item.entryID)
			}
			if match.Entry.Vision != item.vision {
				t.Errorf("Vision = %v，期望 %v", match.Entry.Vision, item.vision)
			}
		})
	}
}

func TestVendorModels(t *testing.T) {
	c := testCatalog(t)
	models := c.VendorModels("openai")
	if len(models) != 2 {
		t.Fatalf("openai 模型数 = %d，期望 2", len(models))
	}
	if models[0].Name != "GPT-4o" || models[1].Name != "GPT-4o mini" {
		t.Errorf("排序结果 = %q, %q", models[0].Name, models[1].Name)
	}
	if name := c.VendorName("openai"); name != "OpenAI" {
		t.Errorf("VendorName = %q", name)
	}
	if name := c.VendorName("unknown"); name != "unknown" {
		t.Errorf("未知厂商应回落为标识本身，实际 %q", name)
	}
	if generated := c.Generated(); generated != "2026-01-01T00:00:00Z" {
		t.Errorf("Generated = %q", generated)
	}
	if vendors := c.Vendors(); len(vendors) != 3 {
		t.Errorf("厂商数 = %d，期望 3", len(vendors))
	}
}

// TestBuiltinSnapshot 校验内置快照可加载且能识别常见模型，数据变化时只做宽松断言。
func TestBuiltinSnapshot(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if c.Generated() == "" {
		t.Error("内置快照缺少生成时间")
	}
	vision, tool := 0, 0
	for vendor, models := range map[string][]string{
		"openai":    {"gpt-4o"},
		"anthropic": {"claude-sonnet-4-5"},
		"google":    {"gemini-2.5-flash"},
	} {
		for _, model := range models {
			match := c.Lookup(vendor, model)
			if !match.Found {
				t.Errorf("内置快照未命中 %s/%s", vendor, model)
				continue
			}
			if match.Entry.Vision {
				vision++
			}
			if match.Entry.ToolCall {
				tool++
			}
		}
	}
	if vision == 0 || tool == 0 {
		t.Errorf("内置快照能力字段异常：vision=%d tool=%d", vision, tool)
	}
}
