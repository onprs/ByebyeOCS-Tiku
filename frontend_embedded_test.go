//go:build ocs_frontend

package main

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedFrontend(t *testing.T) {
	assets, err := loadFrontend("")
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer((&Server{assets: assets}).handler())
	t.Cleanup(web.Close)
	for _, route := range []string{"/", "/records", "/settings"} {
		t.Run(route, func(t *testing.T) {
			resp, err := http.Get(web.URL + route)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, index) {
				t.Fatalf("内置页面响应异常: 状态=%d，错误=%v", resp.StatusCode, err)
			}
			if resp.Header.Get("Cache-Control") != "no-cache, no-store, must-revalidate" {
				t.Fatal("页面缺少缓存控制")
			}
		})
	}

	files, err := fs.Glob(assets, "assets/*")
	if err != nil {
		t.Fatal(err)
	}
	var script string
	var hasStyle bool
	for _, name := range files {
		if strings.HasSuffix(name, ".js") {
			script = name
		}
		if strings.HasSuffix(name, ".css") {
			hasStyle = true
		}
		t.Run(name, func(t *testing.T) {
			want, err := fs.ReadFile(assets, name)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Get(web.URL + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) {
				t.Fatalf("内置资源响应异常: 状态=%d，错误=%v", resp.StatusCode, err)
			}
			contentType := resp.Header.Get("Content-Type")
			if strings.HasSuffix(name, ".js") && !strings.Contains(contentType, "javascript") {
				t.Fatalf("脚本类型异常: %s", contentType)
			}
			if strings.HasSuffix(name, ".css") && !strings.HasPrefix(contentType, "text/css") {
				t.Fatalf("样式类型异常: %s", contentType)
			}
		})
	}
	if script == "" || !hasStyle {
		t.Fatal("内置网页缺少脚本或样式")
	}

	t.Run("资源分段与HEAD请求", func(t *testing.T) {
		want, err := fs.ReadFile(assets, script)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodGet, web.URL+"/"+script, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", "bytes=0-9")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, want[:10]) {
			t.Fatalf("分段请求异常: 状态=%d，错误=%v", resp.StatusCode, err)
		}
		resp, err = http.Head(web.URL + "/" + script)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || len(body) != 0 || resp.ContentLength != int64(len(want)) {
			t.Fatalf("HEAD 请求异常: 状态=%d，长度=%d，错误=%v", resp.StatusCode, resp.ContentLength, err)
		}
	})

	for _, route := range []string{"/missing.js", "/assets/missing", "/api/missing"} {
		t.Run(route, func(t *testing.T) {
			resp, err := http.Get(web.URL + route)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("缺失资源状态=%d，期望 404", resp.StatusCode)
			}
		})
	}
}
