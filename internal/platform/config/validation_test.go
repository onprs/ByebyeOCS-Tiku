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
