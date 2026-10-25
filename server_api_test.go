package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOCSConnectionProbe(t *testing.T) {
	s, web, _ := setupServer(t)
	answer := s.config.snapshot().Answer
	answer.Answer = ModelConfig{Vision: CapabilityAuto, ToolCall: CapabilityAuto}
	if err := s.config.updateAnswer(answer); err != nil {
		t.Fatal(err)
	}
	s.address = strings.TrimPrefix(web.URL, "http://")
	var config []struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	requestJSON(t, web, http.MethodGet, "/api/config", nil, http.StatusOK, &config)
	if len(config) != 1 || config[0].Method != "post" {
		t.Fatalf("题库配置应使用 POST 搜索: %+v", config)
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			// OCS 在题库地址上追加时间戳，并优先使用 HEAD 探测连接。
			req, err := http.NewRequest(method, config[0].URL+"?t=1", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := web.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("连接探测失败: 状态=%d，错误=%v，内容=%s", resp.StatusCode, err, body)
			}
			if method == http.MethodHead && len(body) != 0 {
				t.Fatal("HEAD 响应不应包含正文")
			}
			if method == http.MethodGet {
				var status struct {
					OK bool `json:"ok"`
				}
				if err := json.Unmarshal(body, &status); err != nil || !status.OK {
					t.Fatalf("探测状态异常: %s，错误=%v", body, err)
				}
			}
		})
	}
	// 连接探测不需要模型配置；POST 仍执行答题校验。
	requestJSON(t, web, http.MethodPost, "/api/search", map[string]string{"title": "连接探测后的题目"}, http.StatusBadRequest, nil)
	var records struct {
		Total int `json:"total"`
	}
	requestJSON(t, web, http.MethodGet, "/api/records", nil, http.StatusOK, &records)
	if records.Total != 0 {
		t.Fatalf("连接探测不应产生答题记录: %d", records.Total)
	}
}

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
	localRequest := httptest.NewRequest("GET", "/api/records?page=1&limit=50", nil)
	localRequest.Host = "127.0.0.1"
	s.handler().ServeHTTP(recorder, localRequest)
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
