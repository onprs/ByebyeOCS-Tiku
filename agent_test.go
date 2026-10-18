package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAgentIntermediateTextFallsBack 校验 Agent 最终回复为空时回退普通作答，不采用中间步骤文本。
func TestAgentIntermediateTextFallsBack(t *testing.T) {
	s, web, _ := setupServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []any            `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		toolResult := false
		for _, message := range body.Messages {
			if message["role"] == "tool" {
				toolResult = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if len(body.Tools) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>A</answer>"}, "finish_reason": "stop"}},
			})
			return
		}
		if toolResult {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"content": ""}, "finish_reason": "stop"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "我先查一下历史记录再作答",
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]string{"name": "lookup_history", "arguments": `{"query":"函数"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer fake.Close()
	configureAgent(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "demo"},
		ModelConfig{Model: "agent-model", Vision: CapabilityAuto, ToolCall: CapabilityOn}, 4)

	var result struct{ Question, Answer string }
	requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "函数题目", "options": "A. 一\nB. 二", "type": "single"}, 200, &result)
	if result.Answer != "A" {
		t.Fatalf("应回退到普通作答: %+v", result)
	}
	var item Record
	requestJSON(t, web, "GET", "/api/records/1", nil, 200, &item)
	if !strings.Contains(item.AgentError, "回退") {
		t.Fatalf("未记录 Agent 回退: %+v", item)
	}
	if strings.Contains(item.RawContent, "我先查一下") {
		t.Fatalf("采用了中间步骤文本: %q", item.RawContent)
	}
}

// TestAgentReasoningAnswer 校验 Agent 只有思考内容时按思考内容提取答案并记录。
func TestAgentReasoningAnswer(t *testing.T) {
	s, web, _ := setupServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if len(body.Tools) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>B</answer>"}, "finish_reason": "stop"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": "", "reasoning_content": "推导后得到 <answer>D</answer>"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer fake.Close()
	configureAgent(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "demo"},
		ModelConfig{Model: "reason-model", Vision: CapabilityAuto, ToolCall: CapabilityOn}, 3)

	var result struct{ Question, Answer string }
	requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "推导题目"}, 200, &result)
	if result.Answer != "D" {
		t.Fatalf("思考内容未用于提取答案: %+v", result)
	}
	var item Record
	requestJSON(t, web, "GET", "/api/records/1", nil, 200, &item)
	if item.AgentError != "" || !strings.Contains(item.Reasoning, "<answer>D</answer>") {
		t.Fatalf("思考内容未记录: %+v", item)
	}
	if len(item.Calls) != 1 || item.Calls[0].Slot != "agent" {
		t.Fatalf("调用归属异常: %+v", item.Calls)
	}
}

// TestAgentMaxIterationsMessage 校验步数超限回退时给出可展示原因。
func TestAgentMaxIterationsMessage(t *testing.T) {
	s, web, _ := setupServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if len(body.Tools) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"content": "<answer>C</answer>"}, "finish_reason": "stop"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []any{map[string]any{
						"id": "call_x", "type": "function",
						"function": map[string]string{"name": "lookup_history", "arguments": `{"query":"函数"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer fake.Close()
	configureAgent(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "demo"},
		ModelConfig{Model: "loop-model", Vision: CapabilityAuto, ToolCall: CapabilityOn}, 2)

	var result struct{ Question, Answer string }
	requestJSON(t, web, "POST", "/api/search", map[string]string{"title": "循环题目"}, 200, &result)
	if result.Answer != "C" {
		t.Fatalf("超限未回退: %+v", result)
	}
	var item Record
	requestJSON(t, web, "GET", "/api/records/1", nil, 200, &item)
	if !strings.Contains(item.AgentError, "推理步数超出上限") || strings.Contains(item.AgentError, "node path") {
		t.Fatalf("回退原因不可用: %q", item.AgentError)
	}
}

// TestHistoryToolTruncatesOutput 校验长题目与长答案按字符截断且不破坏 UTF-8。
func TestHistoryToolTruncatesOutput(t *testing.T) {
	store, err := openStore(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	ctx := t.Context()
	id, err := store.pending(ctx, "completion")
	if err != nil {
		t.Fatal(err)
	}
	title := "函数题目" + strings.Repeat("补充题干", 60)
	answer := strings.Repeat("答案内容", 80)
	if err = store.complete(ctx, Record{ID: id, Timestamp: "2026-01-01T00:00:00.000000", Type: "completion",
		Status: "complete", RawTitle: title, Answer: answer}); err != nil {
		t.Fatal(err)
	}
	tool := &historyTool{store: store}
	result, err := tool.InvokableRun(ctx, `{"query":"函数题目"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result) {
		t.Fatal("工具输出不是有效的 UTF-8")
	}
	if strings.Contains(result, answer) || !strings.Contains(result, "…") {
		t.Fatalf("长答案未截断: %d 字节", len(result))
	}
	if len(result) > 500 {
		t.Fatalf("工具输出过长: %d 字节", len(result))
	}
}

// configureAgent 写入服务条目并开启 Agent 模式。
func configureAgent(t *testing.T, s *Server, service ServiceConfig, model ModelConfig, iterations int) {
	t.Helper()
	configureAnswer(t, s, service, model)
	answer := s.config.snapshot().Answer
	answer.Agent = AgentConfig{Enabled: true, MaxIterations: iterations}
	if err := s.config.updateAnswer(answer); err != nil {
		t.Fatal(err)
	}
}
