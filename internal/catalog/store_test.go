package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const buildSource = `{
  "providers": {
    "openai": {"name": "OpenAI", "models": {
      "gpt-4o": {"name": "GPT-4o", "tool_call": true, "reasoning": true, "modalities": {"input": ["text", "image"]}, "limit": {"context": 128000}},
      "gpt-4.1": {"name": "GPT-4.1", "tool_call": true, "modalities": {"input": ["text"]}}
    }},
    "relay": {"name": "中转", "models": {
      "gpt-4o": {"name": "GPT-4o 转发", "modalities": {"input": ["text", "image"]}, "canonical_model_id": "openai/gpt-4o"}
    }}
  },
  "models": {
    "openai/gpt-4o": {"name": "GPT-4o", "tool_call": true, "reasoning": true, "modalities": {"input": ["text", "image"]}, "limit": {"context": 128000}},
    "tencent/hy3": {"name": "混元 3", "modalities": {"input": ["text"]}}
  }
}`

func TestBuild(t *testing.T) {
	snapshot, err := Build([]byte(buildSource), "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generated != "2026-01-01T00:00:00Z" {
		t.Errorf("Generated = %q", snapshot.Generated)
	}
	entry, ok := snapshot.Models["openai/gpt-4o"]
	if !ok {
		t.Fatal("缺少 openai/gpt-4o")
	}
	if !entry.Vision || !entry.ToolCall || !entry.Reasoning || entry.Context != 128000 {
		t.Errorf("openai/gpt-4o 能力异常: %+v", entry)
	}
	if relay := snapshot.Models["relay/gpt-4o"]; relay.Canonical != "openai/gpt-4o" {
		t.Errorf("relay/gpt-4o 规范标识 = %q", relay.Canonical)
	}
	if _, ok = snapshot.Models["tencent/hy3"]; !ok {
		t.Error("规范条目 tencent/hy3 未合并")
	}
	if snapshot.Vendors["tencent"] != "tencent" {
		t.Errorf("规范条目厂商未登记: %q", snapshot.Vendors["tencent"])
	}
	if plain := snapshot.Models["openai/gpt-4.1"]; plain.Vision {
		t.Error("纯文本模型被判定为视觉模型")
	}
	if _, err = Build([]byte(`{}`), ""); err == nil {
		t.Error("空数据应返回错误")
	}
	if _, err = Build([]byte(`{`), ""); err == nil {
		t.Error("非法数据应返回错误")
	}
}

func TestStoreFallsBackToBuiltin(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := store.Status()
	if status.Origin != "builtin" {
		t.Errorf("Origin = %q，期望 builtin", status.Origin)
	}
	if status.Entries == 0 || status.Vendors == 0 {
		t.Errorf("统计异常: %+v", status)
	}
}

func TestStoreLoadsCache(t *testing.T) {
	dir := t.TempDir()
	snapshot, err := Build([]byte(buildSource), "2026-02-02T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writeSnapshot(filepath.Join(dir, "models.json.gz"), snapshot); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	status := store.Status()
	if status.Origin != "cache" {
		t.Errorf("Origin = %q，期望 cache", status.Origin)
	}
	if status.Generated != "2026-02-02T00:00:00Z" {
		t.Errorf("Generated = %q", status.Generated)
	}
	if !store.Catalog().Lookup("openai", "gpt-4o").Found {
		t.Error("缓存快照查询失败")
	}
}

func TestStoreUpdate(t *testing.T) {
	dir := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(buildSource))
	}))
	defer server.Close()

	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.source = server.URL
	changed, err := store.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("首次更新应返回变化")
	}
	status := store.Status()
	if status.Origin != "cache" || status.Checked == "" {
		t.Errorf("更新后状态异常: %+v", status)
	}
	if status.Entries != 4 {
		t.Errorf("更新后条目数 = %d，期望 4", status.Entries)
	}
	changed, err = store.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("304 响应不应视为变化")
	}
	if requests != 2 {
		t.Errorf("请求次数 = %d，期望 2", requests)
	}
	// 重新打开时应读取缓存而不是内置快照。
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status().Origin != "cache" {
		t.Error("重新打开未使用缓存")
	}
}

func TestStoreUpdateFailureKeepsCache(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.source = server.URL
	if _, err = store.Update(context.Background()); err == nil {
		t.Error("HTTP 500 应返回错误")
	}
	if store.Status().Origin != "builtin" {
		t.Error("更新失败不应改变来源")
	}
	if _, err = os.Stat(filepath.Join(dir, "models.json.gz")); !os.IsNotExist(err) {
		t.Error("更新失败不应写入缓存文件")
	}
}

func writeSnapshot(path string, snapshot *Snapshot) (int, error) {
	data, err := encodeSnapshot(snapshot)
	if err != nil {
		return 0, err
	}
	return len(data), writeFileAtomic(path, data, 0600)
}
