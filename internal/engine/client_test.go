package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestChatUsageFieldPresence(t *testing.T) {
	cases := []struct {
		name     string
		usage    string
		present  bool
		complete bool
		input    int64
		output   int64
		total    int64
	}{
		{"完整", `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`, true, true, 10, 5, 15},
		{"缺少 completion 且 prompt 等于 total", `{"prompt_tokens":10,"total_tokens":10}`, false, false, 0, 0, 0},
		{"缺少 prompt", `{"completion_tokens":5,"total_tokens":5}`, false, false, 0, 0, 0},
		{"缺少 total", `{"prompt_tokens":10,"completion_tokens":5}`, false, false, 0, 0, 0},
		{"空对象", `{}`, false, false, 0, 0, 0},
		{"null", `null`, false, false, 0, 0, 0},
		{"字段 null", `{"prompt_tokens":null,"completion_tokens":5,"total_tokens":5}`, false, false, 0, 0, 0},
		{"总量矛盾", `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":1}`, true, false, 10, 5, 1},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			client, baseURL := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"答案"}}],"usage":` + item.usage + `}`))
			})
			result, err := client.Chat(context.Background(), ServiceConfig{
				Protocol: ProtocolOpenAIChat, BaseURL: baseURL, APIKey: "test-key",
			}, ModelConfig{Model: "fixture-model"}, []ChatMessage{{Role: "user", Content: "题目"}}, 0, 32)
			if err != nil {
				t.Fatal(err)
			}
			usage := result.Usage
			if usage.Present != item.present || usage.Complete != item.complete ||
				usage.InputTokens != item.input || usage.OutputTokens != item.output || usage.TotalTokens != item.total {
				t.Fatalf("usage 判定不符: %+v，期望 present=%v complete=%v tokens=%d/%d/%d", usage, item.present, item.complete, item.input, item.output, item.total)
			}
		})
	}
}

func TestChatErrorDoesNotLeakUpstreamBody(t *testing.T) {
	secret := "sk-live-secret-123456"
	question := "敏感题干内容"
	client, baseURL := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"` + secret + ` ` + question + ` https://internal.example/path"}}`))
	})
	_, err := client.Chat(context.Background(), ServiceConfig{
		Protocol: ProtocolOpenAIChat, BaseURL: baseURL, APIKey: "test-key",
	}, ModelConfig{Model: "fixture-model"}, []ChatMessage{{Role: "user", Content: question}}, 0, 32)
	if err == nil {
		t.Fatal("上游 500 应返回错误")
	}
	if err.Error() != "模型服务不可用" {
		t.Fatalf("错误文案应为固定安全文案: %q", err.Error())
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), question) || strings.Contains(err.Error(), "internal.example") {
		t.Fatalf("错误泄露了原始上游内容: %q", err.Error())
	}
	safe := ModelError(errors.New("POST https://internal.example?api_key=" + secret + "&question=" + question))
	if safe.Error() != "模型服务不可用" || strings.Contains(safe.Error(), secret) || strings.Contains(safe.Error(), question) {
		t.Fatalf("ModelError 泄露了原始错误: %q", safe.Error())
	}
}
