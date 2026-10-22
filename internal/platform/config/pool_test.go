package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseConnectionBudget(t *testing.T) {
	for _, value := range []struct {
		content string
		want    int32
		valid   bool
	}{
		{``, 4, true}, {`,"database_max_connections":2`, 2, true},
		{`,"database_max_connections":-1`, 0, false}, {`,"database_max_connections":33`, 0, false},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		content := `{"listen":"127.0.0.1:8092","database_dsn_file":"database.dsn","service_token_file":"service-token"` + value.content + `}`
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if !value.valid {
			if err == nil {
				t.Fatal("连接预算越界仍被接受")
			}
			continue
		}
		if err != nil || cfg.DatabaseMaxConnections != value.want {
			t.Fatalf("题库连接池预算不符: %v", err)
		}
		if cfg.DatabaseDSNFile != filepath.Join(filepath.Dir(path), "database.dsn") || cfg.ServiceTokenFile != filepath.Join(filepath.Dir(path), "service-token") {
			t.Fatal("题库私有文件没有相对配置目录解析")
		}
	}
}
