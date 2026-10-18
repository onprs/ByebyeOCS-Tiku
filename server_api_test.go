package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCreateServiceRejectsExistingID(t *testing.T) {
	s, web, _ := setupServer(t)
	before := s.config.snapshot().Services["deepseek"]
	service := ServiceConfig{Name: "新增服务", Protocol: ProtocolOpenAIChat, BaseURL: "https://models.example.com/v1", APIKey: "test-key"}
	requestJSON(t, web, "POST", "/api/services/deepseek", service, 409, nil)
	if s.config.snapshot().Services["deepseek"] != before {
		t.Fatal("新增请求覆盖了已有服务")
	}
	requestJSON(t, web, "POST", "/api/services/additional", service, 201, nil)
	if s.config.snapshot().Services["additional"] != service {
		t.Fatal("新增服务未保存")
	}
	service.Name = "编辑后的服务"
	requestJSON(t, web, "PUT", "/api/services/additional", service, 200, nil)
	if s.config.snapshot().Services["additional"].Name != service.Name {
		t.Fatal("编辑服务未保存")
	}
}

func TestConnectivityUsesModelDraftWithoutSaving(t *testing.T) {
	s, web, _ := setupServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "draft-model" || !strings.Contains(string(request.Messages), "image_url") {
			t.Errorf("未测试当前模型及图片能力草稿: %+v", request)
		}
		jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "OK"}}}})
	}))
	defer fake.Close()
	configureAnswer(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}, ModelConfig{Model: "saved-model", Vision: CapabilityOff})
	before := s.config.snapshot().Answer
	draft := ModelConfig{Service: "fake", Model: "draft-model", Vision: CapabilityOn, ToolCall: CapabilityOff}
	for _, kind := range []string{"answer", "caption"} {
		requestJSON(t, web, "POST", "/api/test/"+kind, draft, 200, nil)
	}
	if !reflect.DeepEqual(before, s.config.snapshot().Answer) {
		t.Fatal("连通性测试保存了未提交的模型草稿")
	}
	draft.Service = "missing"
	requestJSON(t, web, "POST", "/api/test/answer", draft, 400, nil)
	requestJSON(t, web, "POST", "/api/test/unknown", draft, 404, nil)
}

func TestRecordListIsBoundedAndDetailsStayComplete(t *testing.T) {
	s, web, _ := setupServer(t)
	ctx := t.Context()
	reasoning := strings.Repeat("推理", 2000)
	content := strings.Repeat("正文", 1000)
	var lastID int64
	for i := range 50 {
		id, err := s.store.pending(ctx, "completion")
		if err != nil {
			t.Fatal(err)
		}
		title, answer := "摘要测试题目", "A"
		if i == 49 {
			title, answer = strings.Repeat("长题目", 1000), strings.Repeat("填空答案", 1000)
		}
		err = s.store.complete(ctx, Record{ID: id, Timestamp: time.Now().Format("2006-01-02T15:04:05"), Type: "completion",
			RawTitle: title, ProcessedTitle: title, Answer: answer, Reasoning: reasoning, RawContent: content,
			FinalPrompt: "完整提示词", ProcessedOptions: "完整选项"})
		if err != nil {
			t.Fatal(err)
		}
		lastID = id
	}
	recorder := httptest.NewRecorder()
	s.handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/records?page=1&limit=50", nil))
	if recorder.Code != 200 || recorder.Body.Len() > 25000 {
		t.Fatalf("摘要响应过大或失败: 状态=%d 字节=%d", recorder.Code, recorder.Body.Len())
	}
	var response struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Records) != 50 {
		t.Fatalf("列表响应异常: %+v %v", response, err)
	}
	for _, record := range response.Records {
		for _, field := range []string{"reasoning", "raw_content", "final_prompt", "processed_options", "images"} {
			if _, found := record[field]; found {
				t.Fatalf("列表包含详情字段: %s", field)
			}
		}
	}
	var detail Record
	requestJSON(t, web, "GET", "/api/records/"+itoa(lastID), nil, 200, &detail)
	if detail.Reasoning != reasoning || detail.RawContent != content || len([]rune(detail.ProcessedTitle)) != 3000 || len([]rune(detail.Answer)) != 4000 {
		t.Fatal("摘要截断影响了完整详情")
	}
	var dashboard map[string]json.RawMessage
	requestJSON(t, web, "GET", "/api/dashboard", nil, 200, &dashboard)
	if _, found := dashboard["records"]; found || dashboard["stats"] == nil || dashboard["daily_counts"] == nil {
		t.Fatalf("看板应只包含统计和趋势: %v", dashboard)
	}
	t.Logf("50 条含长推理记录的摘要响应：%d 字节", recorder.Body.Len())
}
