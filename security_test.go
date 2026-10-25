package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLegacySettingsMaskCredentialsAndPreserveExistingKey(t *testing.T) {
	s, web, _ := setupServer(t)
	service := ServiceConfig{Name: "本地模型服务", Protocol: ProtocolOpenAIChat, BaseURL: "https://models.example.invalid/v1", APIKey: "isolated-legacy-model-key"}
	if err := s.config.updateService("private-model", service); err != nil {
		t.Fatal(err)
	}
	var settings settingsView
	requestJSON(t, web, "GET", "/api/settings", nil, 200, &settings)
	view := settings.Services["private-model"]
	if view.APIKey != "" || !view.APIKeyConfigured || s.config.snapshot().Services["private-model"].APIKey != service.APIKey {
		t.Fatal("设置响应泄漏密钥或清除了真实凭据")
	}
	service.Name = "更新后的名称"
	service.APIKey = ""
	requestJSON(t, web, "PUT", "/api/services/private-model", service, 200, nil)
	if s.config.snapshot().Services["private-model"].APIKey != "isolated-legacy-model-key" {
		t.Fatal("遮蔽后的编辑意外清除了凭据")
	}
	requestJSON(t, web, "PUT", "/api/services/private-model", map[string]any{"name": service.Name, "protocol": service.Protocol, "base_url": service.BaseURL, "clear_api_key": true}, 200, nil)
	if s.config.snapshot().Services["private-model"].APIKey != "" {
		t.Fatal("明确移除凭据没有生效")
	}
	service.APIKey = "replacement-fixture-key"
	if err := s.config.updateService("private-model", service); err != nil {
		t.Fatal(err)
	}
	service.APIKey = ""
	service.BaseURL = "https://other-models.example.invalid/v1"
	requestJSON(t, web, "PUT", "/api/services/private-model", service, 200, nil)
	if s.config.snapshot().Services["private-model"].APIKey != "" {
		t.Fatal("更换端点时把旧密钥交给了新服务")
	}
}

func TestLegacyManagementRejectsForeignHostsAndCrossSiteRequests(t *testing.T) {
	_, web, _ := setupServer(t)
	for _, scenario := range []struct{ path, method, host, origin, site string }{
		{path: "/api/settings", method: "GET", host: "foreign.invalid"},
		{path: "/api/settings", method: "GET", origin: "https://foreign.invalid"},
		{path: "/api/test/answer", method: "GET", site: "cross-site"},
		{path: "/api/services/private-model", method: "PUT", origin: "https://foreign.invalid"},
	} {
		request, err := http.NewRequest(scenario.method, web.URL+scenario.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if scenario.host != "" {
			request.Host = scenario.host
		}
		request.Header.Set("Origin", scenario.origin)
		request.Header.Set("Sec-Fetch-Site", scenario.site)
		response, err := web.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("本地管理入口接受了外部主机或跨站请求", scenario.path, response.StatusCode)
		}
	}
	request, _ := http.NewRequest("GET", web.URL+"/api/search", nil)
	request.Host = "compatible-client.invalid"
	response, err := web.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		t.Fatal("v1搜索探测兼容性被破坏")
	}
}
