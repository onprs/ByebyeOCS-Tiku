package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := `{"listen":"127.0.0.1:8093","database_dsn_file":"db","service_token_file":"token","models":[]} {}`
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("多余的 JSON 内容应被拒绝")
	}
}

func TestReadSecretFileIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := ReadSecretFile(path); err != nil || len(value) != 4096 {
		t.Fatal("合法长度的密钥无法读取")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretFile(path); err == nil {
		t.Fatal("超长密钥应被拒绝")
	}
}

func TestLocalAutoLoginRequiresLoopbackOrigin(t *testing.T) {
	base := Config{
		Listen: "127.0.0.1:8093", DatabaseDSNFile: "db", ServiceTokenFile: "token", UpstreamCAFile: "ca",
		Limits: Limits{MaxRequestBytes: 1 << 20, MaxImages: 1, MaxImageBytes: 1 << 20, MaxImagePixels: 1 << 20,
			MaxOutputTokens: 256, Concurrency: 1, TimeoutSeconds: 5, IdempotencyRetentionHours: 168},
		Cache: Cache{TTLDays: 1, MaxTTLDays: 30},
		Panel: Panel{Enabled: true, Origin: "http://127.0.0.1:5193", LocalAutoLogin: true,
			Users: []PanelUser{{Username: "maintainer", Role: "maintainer", PasswordHashFile: "hash"}}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("回环入口应允许本地自动登录：%v", err)
	}
	public := base
	public.Panel.Origin = "https://admin.example.com"
	if err := public.Validate(); err == nil {
		t.Fatal("公开入口不应允许本地自动登录")
	}
	public.Panel.LocalAutoLogin = false
	if err := public.Validate(); err != nil {
		t.Fatalf("公开入口关闭本地自动登录后应正常：%v", err)
	}
}
