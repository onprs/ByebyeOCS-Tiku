package modelclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

const discoveryTestKey = "discovery-test-secret"

func discoveryClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewWithOptions(Options{AllowLoopback: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeDiscoveryJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestDiscoverModelsProtocolEndpointsAndAuthentication(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		path     string
		check    func(*testing.T, *http.Request)
		body     string
	}{
		{
			name: "openai", protocol: engine.ProtocolOpenAIChat, path: "/v1/models",
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if r.Header.Get("Authorization") != "Bearer "+discoveryTestKey || r.URL.Query().Get("limit") != "100" {
					t.Fatalf("OpenAI 请求头或分页参数不正确: headers=%v query=%v", r.Header, r.URL.Query())
				}
			}, body: `{"data":[{"id":"chat-model","owned_by":"vendor"}]}`,
		},
		{
			name: "anthropic", protocol: engine.ProtocolAnthropicMessages, path: "/v1/models",
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if r.Header.Get("x-api-key") != discoveryTestKey || r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Fatalf("Anthropic 认证头不正确: %v", r.Header)
				}
			}, body: `{"data":[{"id":"claude-model","display_name":"Claude Model"}],"has_more":false}`,
		},
		{
			name: "gemini", protocol: engine.ProtocolGeminiGenerate, path: "/v1beta/models",
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if r.Header.Get("x-goog-api-key") != discoveryTestKey {
					t.Fatalf("Gemini 认证头不正确: %v", r.Header)
				}
			}, body: `{"models":[{"name":"models/gemini-model","displayName":"Gemini Model","supportedGenerationMethods":["generateContent"]}]}`,
		},
		{
			name: "ollama", protocol: engine.ProtocolOllamaChat, path: "/api/tags",
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if r.Header.Get("Authorization") != "Bearer "+discoveryTestKey {
					t.Fatalf("Ollama 认证头不正确: %v", r.Header)
				}
			}, body: `{"models":[{"name":"qwen:latest"}]}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tt.path {
					t.Errorf("协议目录路径不正确: %s %s", r.Method, r.URL.String())
				}
				tt.check(t, r)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			baseURL := server.URL
			if tt.protocol == engine.ProtocolOpenAIChat {
				baseURL += "/v1"
			}
			items, err := discoveryClient(t).DiscoverModels(context.Background(), tt.protocol, baseURL, discoveryTestKey)
			if err != nil {
				t.Fatalf("发现模型失败: %v", err)
			}
			if len(items) != 1 || strings.Contains(fmt.Sprint(items), discoveryTestKey) {
				t.Fatalf("发现结果不正确或泄漏密钥: %+v", items)
			}
		})
	}
}

func TestDiscoverModelsPaginationAndChatCapabilityFilter(t *testing.T) {
	var openAICalls, anthropicCalls, geminiCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openai/v1/models":
			openAICalls++
			if openAICalls == 1 {
				writeDiscoveryJSON(w, map[string]any{"data": []any{map[string]string{"id": "z-model"}}, "has_more": true})
				return
			}
			if r.URL.Query().Get("after") != "z-model" {
				t.Errorf("OpenAI after 游标错误: %v", r.URL.Query())
			}
			writeDiscoveryJSON(w, map[string]any{"data": []any{map[string]string{"id": "a-model"}}, "has_more": false})
		case "/anthropic/v1/models":
			anthropicCalls++
			if anthropicCalls == 1 {
				writeDiscoveryJSON(w, map[string]any{"data": []any{map[string]string{"id": "claude-a"}}, "has_more": true, "last_id": "claude-a"})
				return
			}
			if r.URL.Query().Get("after_id") != "claude-a" {
				t.Errorf("Anthropic after_id 游标错误: %v", r.URL.Query())
			}
			writeDiscoveryJSON(w, map[string]any{"data": []any{map[string]string{"id": "claude-b"}}, "has_more": false})
		case "/gemini/v1beta/models":
			geminiCalls++
			if geminiCalls == 1 {
				if r.URL.Query().Get("pageToken") != "" {
					t.Errorf("Gemini 首次请求带有游标: %v", r.URL.Query())
				}
				writeDiscoveryJSON(w, map[string]any{
					"models": []any{
						map[string]any{"name": "models/gemini-chat", "supportedGenerationMethods": []string{"generateContent"}},
						map[string]any{"name": "models/gemini-embed", "supportedGenerationMethods": []string{"embedContent"}},
					}, "nextPageToken": "next-page",
				})
				return
			}
			if r.URL.Query().Get("pageToken") != "next-page" {
				t.Errorf("Gemini pageToken 游标错误: %v", r.URL.Query())
			}
			writeDiscoveryJSON(w, map[string]any{"models": []any{map[string]any{"name": "models/gemini-next", "displayName": "Gemini Next"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := discoveryClient(t)

	openAI, err := client.DiscoverModels(context.Background(), engine.ProtocolOpenAIChat, server.URL+"/openai/v1", discoveryTestKey)
	if err != nil || len(openAI) != 2 || openAI[0].ID != "a-model" || openAICalls != 2 {
		t.Fatalf("OpenAI 分页或排序错误: items=%+v calls=%d err=%v", openAI, openAICalls, err)
	}
	anthropic, err := client.DiscoverModels(context.Background(), engine.ProtocolAnthropicMessages, server.URL+"/anthropic", discoveryTestKey)
	if err != nil || len(anthropic) != 2 || anthropicCalls != 2 {
		t.Fatalf("Anthropic 分页错误: items=%+v calls=%d err=%v", anthropic, anthropicCalls, err)
	}
	gemini, err := client.DiscoverModels(context.Background(), engine.ProtocolGeminiGenerate, server.URL+"/gemini", discoveryTestKey)
	if err != nil || len(gemini) != 2 || gemini[0].ID != "gemini-chat" || gemini[1].ID != "gemini-next" || geminiCalls != 2 {
		t.Fatalf("Gemini 分页或能力过滤错误: items=%+v calls=%d err=%v", gemini, geminiCalls, err)
	}
}

func TestDiscoverModelsErrorMappingAndResponseLimit(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCode  string
		wantCalls int
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: `{"error":"` + discoveryTestKey + `"}`, wantCode: "model_auth_failed", wantCalls: 1},
		{name: "rate limit", status: http.StatusTooManyRequests, body: `{"error":"internal host and ` + discoveryTestKey + `"}`, wantCode: "upstream_rate_limited", wantCalls: 1},
		{name: "malformed response", status: http.StatusOK, body: "not-json", wantCode: "model_unavailable", wantCalls: 1},
		{name: "response too large", status: http.StatusOK, body: strings.Repeat("x", int(maxDiscoveryResponseBytes+1)), wantCode: "response_too_large", wantCalls: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := discoveryClient(t).DiscoverModels(context.Background(), engine.ProtocolOpenAIChat, server.URL, discoveryTestKey)
			var mapped *Error
			if !errors.As(err, &mapped) || mapped.Code != tt.wantCode || calls != tt.wantCalls {
				t.Fatalf("错误分类或请求次数不正确: err=%v calls=%d", err, calls)
			}
			if strings.Contains(err.Error(), discoveryTestKey) || strings.Contains(err.Error(), "internal host") {
				t.Fatalf("错误包含上游内容或密钥: %v", err)
			}
		})
	}
}

func TestDiscoverModelsStopsRepeatedPaginationAndRejectsRedirects(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeDiscoveryJSON(w, map[string]any{"data": []any{}, "has_more": true, "last_id": "same-cursor"})
	}))
	defer server.Close()
	_, err := discoveryClient(t).DiscoverModels(context.Background(), engine.ProtocolOpenAIChat, server.URL, "")
	var mapped *Error
	if !errors.As(err, &mapped) || mapped.Code != "model_unavailable" || calls != 2 {
		t.Fatalf("重复分页未及时终止: calls=%d err=%v", calls, err)
	}

	calls = 0
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer redirectTarget.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/v1/models", http.StatusFound)
	}))
	defer redirector.Close()
	_, err = discoveryClient(t).DiscoverModels(context.Background(), engine.ProtocolOpenAIChat, redirector.URL, "")
	if !errors.As(err, &mapped) || mapped.Code != "model_unavailable" || calls != 0 {
		t.Fatalf("模型发现跟随了重定向: calls=%d err=%v", calls, err)
	}
}

func TestDiscoverModelsFiltersMetadataThatContainsCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(w, map[string]any{"data": []any{
			map[string]string{"id": "safe-model", "owned_by": "public"},
			map[string]string{"id": "leaked-" + discoveryTestKey},
		}})
	}))
	defer server.Close()
	items, err := discoveryClient(t).DiscoverModels(context.Background(), engine.ProtocolOpenAIChat, server.URL, discoveryTestKey)
	if err != nil || len(items) != 1 || items[0].ID != "safe-model" {
		t.Fatalf("包含密钥的目录条目未被过滤: items=%+v err=%v", items, err)
	}
	encoded, _ := json.Marshal(items)
	if bytes.Contains(encoded, []byte(discoveryTestKey)) {
		t.Fatalf("模型目录响应泄漏密钥: %s", encoded)
	}
}

func TestDiscoverModelsRejectsUnsupportedProtocol(t *testing.T) {
	_, err := discoveryClient(t).DiscoverModels(context.Background(), "unknown", "http://127.0.0.1", "")
	var mapped *Error
	if !errors.As(err, &mapped) || mapped.Code != "unsupported_protocol" {
		t.Fatalf("不支持的协议错误分类不正确: %v", err)
	}
}
