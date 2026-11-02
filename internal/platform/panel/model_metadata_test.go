package panel

import (
	"reflect"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
)

func TestEnrichDiscoveredModelsUsesBuiltinVendorMetadata(t *testing.T) {
	items := enrichDiscoveredModels([]modelclient.DiscoveredModel{{ID: "gpt-4o", DisplayName: "gpt-4o"}}, " OpenAI ")
	if len(items) != 1 {
		t.Fatalf("模型目录长度变化：%+v", items)
	}
	item := items[0]
	if !item.MetadataMatched || item.DisplayName != "GPT-4o" || item.Context != 128000 {
		t.Fatalf("模型元数据未匹配：%+v", item)
	}
	if !reflect.DeepEqual(item.Capabilities, []string{"text", "vision", "tool_call"}) {
		t.Fatalf("模型能力未回填：%+v", item.Capabilities)
	}
}

func TestEnrichDiscoveredModelsKeepsDistinctDeepSeekNames(t *testing.T) {
	items := enrichDiscoveredModels([]modelclient.DiscoveredModel{
		{ID: "deepseek-v4-flash", DisplayName: "deepseek-v4-flash"},
		{ID: "deepseek-v4-flash-vision-exp", DisplayName: "deepseek-v4-flash-vision-exp"},
	}, "deepseek")
	if items[0].DisplayName != "DeepSeek V4 Flash" || items[1].DisplayName != "DeepSeek V4 Flash Vision Exp" {
		t.Fatalf("不同 DeepSeek 模型被错误合并：%+v", items)
	}
	if items[0].DisplayName == items[1].DisplayName {
		t.Fatalf("视觉实验模型名称与普通模型重复：%+v", items)
	}
}

func TestEnrichDiscoveredModelsDoesNotInventMetadata(t *testing.T) {
	original := []modelclient.DiscoveredModel{{ID: "private-model-unknown", DisplayName: "private-model-unknown"}}
	items := enrichDiscoveredModels(original, "unknown-vendor")
	if !reflect.DeepEqual(items, original) {
		t.Fatalf("未匹配模型的目录元数据被改写：got=%+v want=%+v", items, original)
	}
}
