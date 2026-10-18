package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type modelTransportFunc func(*http.Request) (*http.Response, error)

func (f modelTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOllamaTemperatureIncludesZero(t *testing.T) {
	for _, temperature := range []float64{0, 0.7} {
		t.Run(fmt.Sprintf("%g", temperature), func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Options struct {
						Temperature *float64 `json:"temperature"`
						NumPredict  int      `json:"num_predict"`
					} `json:"options"`
					Messages []any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Options.Temperature == nil || *body.Options.Temperature != temperature || body.Options.NumPredict != 32 || len(body.Messages) != 1 {
					t.Errorf("Ollama 请求参数异常: %+v", body)
				}
				jsonResponse(w, 200, map[string]any{"model": "answer-model", "message": map[string]string{"role": "assistant", "content": "OK"}, "done": true})
			}))
			defer fake.Close()
			content, _, err := newModelClient().chat(t.Context(), ServiceConfig{Protocol: ProtocolOllamaChat, BaseURL: fake.URL},
				ModelConfig{Model: "answer-model"}, []chatMessage{{Role: "user", Content: "参数测试"}}, temperature, 32)
			if err != nil || content != "OK" {
				t.Fatalf("Ollama 调用失败: %q %v", content, err)
			}
		})
	}
}

func TestGeminiUsesSharedHTTPTimeout(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]string{"text": "OK"}}}, "finishReason": "STOP"}}})
	}))
	defer fake.Close()
	var called atomic.Bool
	client := newModelClient()
	client.http = &http.Client{Timeout: 20 * time.Millisecond, Transport: modelTransportFunc(func(request *http.Request) (*http.Response, error) {
		called.Store(true)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	_, _, err := client.chat(t.Context(), ServiceConfig{Protocol: ProtocolGeminiGenerate, BaseURL: fake.URL, APIKey: "test-key"},
		ModelConfig{Model: "answer-model"}, []chatMessage{{Role: "user", Content: "超时测试"}}, 0.1, 32)
	if !called.Load() || err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Gemini 未使用共享客户端的超时: called=%v error=%v", called.Load(), err)
	}
}
