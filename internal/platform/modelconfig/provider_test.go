package modelconfig

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProviderModelsShareConnectionAndRotateSafely(t *testing.T) {
	editor, _, path := newEditorFixture(t)
	provider := ProviderInput{Name: "测试提供商", Protocol: "openai-chat", BaseURL: "https://models.example.com/v1", APIKey: "first-key"}
	if err := editor.UpsertProvider(true, "example", provider); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first-model", "second-model"} {
		if _, err := editor.Upsert(true, id, Input{ProviderID: "example", DisplayName: id, UpstreamModel: id}); err != nil {
			t.Fatal(err)
		}
	}
	first, firstKey := editor.registry.Resolve("first-model")
	second, secondKey := editor.registry.Resolve("second-model")
	if firstKey != "first-key" || secondKey != firstKey || first.APIKeyFile != second.APIKeyFile {
		t.Fatal("两个模型未共享同一提供商配置")
	}
	originalPath := first.APIKeyFile
	if err := editor.DeleteProvider("example"); !errors.Is(err, ErrInUse) {
		t.Fatalf("被引用的提供商应拒绝删除：%v", err)
	}

	invalid := provider
	invalid.BaseURL = "http://public.example.com/v1"
	invalid.APIKey = "invalid-new-key"
	if err := editor.UpsertProvider(false, "example", invalid); err == nil {
		t.Fatal("无效地址应被拒绝")
	}
	content, err := os.ReadFile(originalPath)
	if err != nil || string(content) != "first-key\n" {
		t.Fatal("校验失败改变了原密钥")
	}
	_, key := editor.registry.Resolve("first-model")
	if key != "first-key" {
		t.Fatal("校验失败改变了运行时密钥")
	}
	invalid.APIKey = ""
	if err := editor.UpsertProvider(false, "example", invalid); err == nil {
		t.Fatal("更换连接时不得沿用旧密钥")
	}

	provider.APIKey = "rotated-key"
	if err := editor.UpsertProvider(false, "example", provider); err != nil {
		t.Fatal(err)
	}
	first, firstKey = editor.registry.Resolve("first-model")
	second, secondKey = editor.registry.Resolve("second-model")
	if firstKey != "rotated-key" || secondKey != firstKey || first.APIKeyFile != second.APIKeyFile {
		t.Fatal("提供商更新未同时应用到所有模型")
	}
	if _, err := os.Stat(originalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("旧托管密钥未清理")
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "rotated-key") || !strings.Contains(string(raw), `"provider_id": "example"`) {
		t.Fatal("持久配置缺少提供商引用或包含明文密钥")
	}
	for _, id := range []string{"first-model", "second-model"} {
		if _, err := editor.Delete(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := editor.DeleteProvider("example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.APIKeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("删除提供商后托管密钥仍存在")
	}
}

func TestLegacyModelRejectedUpdateDoesNotOverwriteKey(t *testing.T) {
	editor, _, _ := newEditorFixture(t)
	input := Input{DisplayName: "旧模型", Protocol: "openai-chat", BaseURL: "https://models.example.com/v1", UpstreamModel: "model", APIKey: "old-key"}
	if _, err := editor.Upsert(true, "legacy-model", input); err != nil {
		t.Fatal(err)
	}
	model, _ := editor.registry.Resolve("legacy-model")
	input.APIKey, input.BaseURL = "new-key", "http://public.example.com/v1"
	if _, err := editor.Upsert(false, model.ID, input); err == nil {
		t.Fatal("无效模型更新应失败")
	}
	key, err := os.ReadFile(model.APIKeyFile)
	if err != nil || string(key) != "old-key\n" {
		t.Fatal("失败的更新覆盖了旧密钥")
	}
	input.APIKey = ""
	input.BaseURL = "https://other.example.com/v1"
	if _, err := editor.Upsert(false, model.ID, input); err == nil {
		t.Fatal("更换端点不得隐式发送原密钥")
	}
}
