package modelconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
)

func newEditorFixture(t *testing.T) (*Editor, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ocs-tiku-fork.json")
	cfg := &config.Config{
		SourcePath: path, Listen: "127.0.0.1:8092", DatabaseDSNFile: filepath.Join(dir, "db.dsn"),
		ServiceTokenFile: filepath.Join(dir, "token"), AllowLoopbackUpstreams: true,
		Limits: config.Limits{MaxRequestBytes: 1 << 20, MaxImages: 2, MaxImageBytes: 1 << 20, MaxImagePixels: 1000,
			MaxOutputTokens: 256, Concurrency: 1, TimeoutSeconds: 30, IdempotencyRetentionHours: 168},
		Cache: config.Cache{TTLDays: 7, MaxTTLDays: 30},
	}
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:8092","database_dsn_file":"`+cfg.DatabaseDSNFile+`","service_token_file":"`+cfg.ServiceTokenFile+`","allow_loopback_upstreams":true,"limits":{"max_request_bytes":1048576,"max_images":2,"max_image_bytes":1048576,"max_image_pixels":1000,"max_output_tokens":256,"concurrency":1,"timeout_seconds":30,"idempotency_retention_hours":168},"cache":{"ttl_days":7,"max_ttl_days":30},"models":[]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := models.New(nil, nil)
	editor, err := NewEditor(cfg, registry)
	if err != nil {
		t.Fatal(err)
	}
	return editor, cfg, path
}

func TestEditorPersistsModelsAndPrivateKeys(t *testing.T) {
	editor, _, path := newEditorFixture(t)
	input := Input{DisplayName: "标准模型", Protocol: "openai-chat", BaseURL: "https://models.example.com/v1",
		UpstreamModel: "upstream-1", APIKey: "editor-secret-key", Context: 128000, MaxOutputTokens: 512}
	if _, err := editor.Upsert(true, "standard-model", input); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil || len(loaded.Models) != 1 || loaded.Models[0].ID != "standard-model" {
		t.Fatalf("模型未写入配置：%+v/%v", loaded, err)
	}
	keyPath := loaded.Models[0].APIKeyFile
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("密钥文件权限错误：%v/%v", info, err)
	}
	key, _ := os.ReadFile(keyPath)
	if string(key) != "editor-secret-key\n" {
		t.Fatal("密钥文件内容不正确")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("配置文件权限错误：%v/%v", info, err)
	}

	update := input
	update.DisplayName = "更新模型"
	update.APIKey = ""
	update.Caption = true
	if _, err := editor.Upsert(false, "standard-model", update); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.Load(path)
	if err != nil || loaded.Models[0].DisplayName != "更新模型" || loaded.CaptionModelID != "standard-model" {
		t.Fatalf("更新未生效：%+v/%v", loaded, err)
	}
	keyAfter, _ := os.ReadFile(keyPath)
	if string(keyAfter) != "editor-secret-key\n" {
		t.Fatal("空密钥更新覆盖了原密钥")
	}
	if _, err := editor.Delete("standard-model"); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.Load(path)
	if err != nil || len(loaded.Models) != 0 || loaded.CaptionModelID != "" {
		t.Fatalf("删除未清理配置：%+v/%v", loaded, err)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("删除模型后密钥文件仍存在")
	}
}

func TestEditorRejectsInvalidModelWithoutRewritingConfig(t *testing.T) {
	editor, _, path := newEditorFixture(t)
	before, _ := os.ReadFile(path)
	_, err := editor.Upsert(true, "bad-model", Input{DisplayName: "错误模型", Protocol: "openai-chat",
		BaseURL: "http://example.com/v1", UpstreamModel: "x", APIKey: "key", Context: 1, MaxOutputTokens: 1})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("应返回校验错误：%T %v", err, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("校验失败仍改写了配置文件")
	}
}
