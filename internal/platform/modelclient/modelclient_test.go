package modelclient

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

func tlsClient(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "upstream-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewWithOptions(Options{AllowLoopback: true, CAFile: caPath, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client, server.URL + "/v1"
}

func TestMapErrorDoesNotLeakRawError(t *testing.T) {
	secret := "sk-live-secret-123456"
	question := "敏感题干内容"
	cases := []error{
		errors.New("POST https://internal.example?api_key=" + secret + "&question=" + question + ": 500"),
		&engine.Error{Code: "model_unavailable", Message: secret + " " + question},
		engine.ModelError(errors.New("upstream body " + secret + " " + question)),
	}
	for _, raw := range cases {
		mapped := mapError(raw)
		if mapped == nil {
			t.Fatal("错误不应为空")
		}
		if strings.Contains(mapped.Error(), secret) || strings.Contains(mapped.Error(), question) || strings.Contains(mapped.Error(), "internal.example") {
			t.Fatalf("错误文案泄露原始内容: %q", mapped.Error())
		}
		if mapped.Error() != "模型服务不可用" {
			t.Fatalf("错误文案不是固定安全文案: %q", mapped.Error())
		}
	}
}

func TestUsageFromEngineRejectsNegativeCounts(t *testing.T) {
	usage := usageFromEngine(engine.TokenUsage{Present: true, Complete: true, InputTokens: -1, OutputTokens: 2, TotalTokens: 1})
	if usage.Complete || usage.InputTokens != nil || usage.OutputTokens == nil || usage.TotalTokens == nil {
		t.Fatalf("无效用量不应作为完整事实：%+v", usage)
	}
}

func TestLargeResponseIsBounded(t *testing.T) {
	client, baseURL := tlsClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		prefix := []byte(`{"choices":[{"message":{"content":"`)
		suffix := []byte(`"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		_, _ = w.Write(prefix)
		remaining := maxUpstreamResponseBytes + 1 - int64(len(prefix)) - int64(len(suffix))
		chunk := bytes.Repeat([]byte("A"), 32<<10)
		for remaining > 0 {
			n := int64(len(chunk))
			if n > remaining {
				n = remaining
			}
			_, _ = w.Write(chunk[:n])
			remaining -= n
		}
		_, _ = w.Write(suffix)
	})
	_, err := client.Chat(context.Background(), Call{
		Protocol: engine.ProtocolOpenAIChat, BaseURL: baseURL, APIKey: "test-key",
		Model: "fixture-model", Prompt: "题目", MaxOutputTokens: 16,
	})
	if err == nil {
		t.Fatal("超大响应应被拒绝")
	}
	var mapped *Error
	if !errors.As(err, &mapped) {
		t.Fatalf("错误类型不符: %T %v", err, err)
	}
	if mapped.Code != "response_too_large" || mapped.Message != "模型响应超过大小上限" {
		t.Fatalf("超大响应错误不稳定: %+v", mapped)
	}
	if strings.Contains(mapped.Error(), "AAAA") || len(mapped.Error()) > 64 {
		t.Fatalf("超大响应错误包含原始内容: %q", mapped.Error())
	}
}
