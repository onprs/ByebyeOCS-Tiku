package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tlsFixture 用本地 TLS 服务验证真实 HTTP 调用与 usage 解析，不连接外部模型。
func tlsFixture(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})) {
		t.Fatal("无法加载测试 CA")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
		},
	}
	return NewClientWithHTTP(client), server.URL + "/v1"
}

func TestChatReturnsRealUsageOverTLS(t *testing.T) {
	var requests int
	client, baseURL := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("缺少 Authorization: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "<answer>A</answer>"}}},
			"usage":   map[string]int{"prompt_tokens": 123, "completion_tokens": 7, "total_tokens": 130},
		})
	})
	result, err := client.Chat(context.Background(), ServiceConfig{
		Protocol: ProtocolOpenAIChat, BaseURL: baseURL, APIKey: "test-key",
	}, ModelConfig{Model: "fixture-model"}, []ChatMessage{{Role: "user", Content: "题目"}}, 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.Content != "<answer>A</answer>" {
		t.Fatalf("调用事实不符: requests=%d content=%q", requests, result.Content)
	}
	if !result.Usage.Present || !result.Usage.Complete || result.Usage.InputTokens != 123 || result.Usage.OutputTokens != 7 || result.Usage.TotalTokens != 130 {
		t.Fatalf("真实 usage 未返回: %+v", result.Usage)
	}
}

func TestChatMissingUsageIsIncomplete(t *testing.T) {
	var requests int
	client, baseURL := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "答案"}}},
		})
	})
	result, err := client.Chat(context.Background(), ServiceConfig{
		Protocol: ProtocolOpenAIChat, BaseURL: baseURL, APIKey: "test-key",
	}, ModelConfig{Model: "fixture-model"}, []ChatMessage{{Role: "user", Content: "题目"}}, 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.Usage.Complete || result.Usage.TotalTokens != 0 || result.Usage.Present {
		t.Fatalf("缺失 usage 应标记不完整且不补 0: %+v", result.Usage)
	}
}
