package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnswerTemperature(t *testing.T) {
	for _, agent := range []bool{false, true} {
		for _, temperature := range []float64{0, 1.3} {
			t.Run(map[bool]string{false: "普通", true: "Agent"}[agent]+"/"+map[bool]string{true: "零温度", false: "自定义温度"}[temperature == 0], func(t *testing.T) {
				s, web, _ := setupServer(t)
				var actual atomic.Int64
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Temperature *float64 `json:"temperature"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Temperature == nil {
						t.Errorf("请求缺少温度: %v", err)
					} else {
						actual.Store(int64(*body.Temperature * 1000))
					}
					jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>A</answer>"}, "finish_reason": "stop"}}})
				}))
				defer fake.Close()
				service := ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}
				model := ModelConfig{Model: "answer-model", Vision: CapabilityOff, ToolCall: CapabilityOn}
				if agent {
					configureAgent(t, s, service, model, 3)
				} else {
					configureAnswer(t, s, service, model)
				}
				answer := s.config.snapshot().Answer
				answer.Temperature = temperature
				if err := s.config.updateAnswer(answer); err != nil {
					t.Fatal(err)
				}
				requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "温度配置题目"}, 200, nil)
				if math.Abs(float64(actual.Load())/1000-temperature) > 0.001 {
					t.Fatalf("实际温度 %v，预期 %v", float64(actual.Load())/1000, temperature)
				}
			})
		}
	}
}

func TestEmptyAnswersAreFailures(t *testing.T) {
	for _, content := range []string{"", "<answer> </answer>"} {
		t.Run(content, func(t *testing.T) {
			s, web, _ := setupServer(t)
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content, "reasoning_content": "还需要进一步推导"}, "finish_reason": "length"}}})
			}))
			defer fake.Close()
			configureAnswer(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}, ModelConfig{Model: "answer-model", Vision: CapabilityOff})
			requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "空答案题目"}, 500, nil)
			stats, err := s.store.stats(t.Context())
			if err != nil || stats.Total != 1 || stats.SuccessRate != 0 {
				t.Fatalf("空答案不应计为成功: %+v %v", stats, err)
			}
			record, err := s.store.get(t.Context(), 1)
			if err != nil || record.Status != "complete" || !strings.HasPrefix(record.Answer, "ERROR:") || record.Reasoning == "" {
				t.Fatalf("失败记录未正确保存: %+v %v", record, err)
			}
		})
	}
}

func TestAgentReasoningWithoutAnswerFallsBack(t *testing.T) {
	s, web, _ := setupServer(t)
	var agentCalls, answerCalls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		message := map[string]string{"content": "<answer>B</answer>"}
		if len(body.Tools) > 0 {
			agentCalls.Add(1)
			message = map[string]string{"content": "", "reasoning_content": "推理尚未得出答案"}
		} else {
			answerCalls.Add(1)
		}
		jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
	}))
	defer fake.Close()
	configureAgent(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}, ModelConfig{Model: "answer-model", Vision: CapabilityOff, ToolCall: CapabilityOn}, 3)
	var result struct{ Answer string }
	requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "推理未完成题目"}, 200, &result)
	if result.Answer != "B" || agentCalls.Load() != 1 || answerCalls.Load() != 1 {
		t.Fatalf("未正确回退: %+v，Agent=%d，普通=%d", result, agentCalls.Load(), answerCalls.Load())
	}
	record, err := s.store.get(t.Context(), 1)
	if err != nil || !strings.Contains(record.AgentError, "回退") {
		t.Fatalf("缺少回退原因: %+v %v", record, err)
	}
}

func TestMultilineOptionImageLabels(t *testing.T) {
	for _, options := range []string{
		"A. 第一项\n附图 https://images.example.com/a.png\n\nB. 第二项\n附图 https://images.example.com/b.png",
		"第一项 https://images.example.com/a.png\n\n第二项 https://images.example.com/b.png",
	} {
		parts := directContent("图片归属题目", options, "single", map[string][]byte{
			"https://images.example.com/a.png": []byte("第一张图"),
			"https://images.example.com/b.png": []byte("第二张图"),
		}).([]contentPart)
		labels := []string{}
		for _, part := range parts {
			if strings.HasPrefix(part.Text, "[选项") {
				labels = append(labels, part.Text)
			}
		}
		if strings.Join(labels, ",") != "[选项 A 的图片],[选项 B 的图片]" {
			t.Fatalf("选项图片标记异常: %v", labels)
		}
	}
}

func TestImageProcessingFailures(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"download", "caption", "empty-caption", "partial"} {
		t.Run(failure, func(t *testing.T) {
			processor, err := newImageProcessor(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			processor.Fetch = func(_ context.Context, url string) ([]byte, error) {
				if failure == "download" || failure == "partial" && strings.HasSuffix(url, "b.png") {
					return nil, errors.New("HTTP 404")
				}
				return imageData.Bytes(), nil
			}
			caption := &captionTask{Run: func(context.Context, []byte) (string, error) {
				if failure == "caption" {
					return "", errors.New("转述模型失败")
				}
				return "  ", nil
			}}
			direct := failure == "download" || failure == "partial"
			_, _, infos, _, err := processor.Process(t.Context(), "https://images.example.com/a.png", "https://images.example.com/b.png", caption, direct, nil)
			if err == nil || len(infos) != 2 || infos[1].Status != "failed" {
				t.Fatalf("图片失败未传播: %+v %v", infos, err)
			}
		})
	}
}

func TestMissingQuestionImageDoesNotCallAnswerModel(t *testing.T) {
	s, web, _ := setupServer(t)
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>A</answer>"}}}})
	}))
	defer fake.Close()
	configureAnswer(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}, ModelConfig{Model: "answer-model", Vision: CapabilityOn})
	s.solver.images.Fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("HTTP 404") }
	requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "识别图形 https://images.example.com/question.png"}, 500, nil)
	if calls.Load() != 0 {
		t.Fatal("题图缺失时仍调用了回答模型")
	}
	record, err := s.store.get(t.Context(), 1)
	if err != nil || len(record.Images) != 1 || record.Images[0].Status != "failed" || !strings.HasPrefix(record.Answer, "ERROR:") {
		t.Fatalf("未保存图片失败详情: %+v %v", record, err)
	}
}
