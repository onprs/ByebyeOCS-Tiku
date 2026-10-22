package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalFrontend(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadFrontend(dir); err == nil {
		t.Fatal("缺少首页时应返回错误")
	}
	indexPath := filepath.Join(dir, "index.html")
	if err := os.Mkdir(indexPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFrontend(dir); err == nil {
		t.Fatal("首页为目录时应返回错误")
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	want := []byte("<html>自定义网页</html>")
	if err := os.WriteFile(indexPath, want, 0600); err != nil {
		t.Fatal(err)
	}
	assets, err := loadFrontend(dir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(assets, "index.html")
	if err != nil || !bytes.Equal(body, want) {
		t.Fatalf("未使用指定目录的网页: %v", err)
	}
}
