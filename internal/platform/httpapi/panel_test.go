package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/panel"
	"golang.org/x/crypto/bcrypt"
)

func TestPostgresPanelAuthenticationPermissionsAndAudit(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	web := httptest.NewUnstartedServer(nil)
	origin := "http://" + web.Listener.Addr().String()
	password := "Panel-Fixture-2026!"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		t.Fatal(err)
	}
	hashPath := filepath.Join(t.TempDir(), "panel.hash")
	if err := os.WriteFile(hashPath, hash, 0600); err != nil {
		t.Fatal(err)
	}
	f.cfg.Panel = config.Panel{Enabled: true, Origin: origin, Users: []config.PanelUser{{Username: "maintainer", Role: "maintainer", PasswordHashFile: hashPath}, {Username: "viewer", Role: "viewer", PasswordHashFile: hashPath}}}
	writeFixtureConfig(t, f.cfg)
	handler, err := panel.New(f.cfg, f.svc, f.store, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = panel.Mount(f.handler, handler)
	web.Start()
	t.Cleanup(web.Close)
	newClient := func() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }
	client, viewer := newClient(), newClient()
	t.Cleanup(client.CloseIdleConnections)
	t.Cleanup(viewer.CloseIdleConnections)
	request := func(client *http.Client, method, path, csrf string, value any, headers map[string]string) (int, http.Header, []byte) {
		t.Helper()
		var input io.Reader
		if value != nil {
			data, _ := json.Marshal(value)
			input = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, origin+path, input)
		if err != nil {
			t.Fatal(err)
		}
		if value != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method != "GET" {
			req.Header.Set("Origin", origin)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data
	}
	login := func(client *http.Client, username string) string {
		t.Helper()
		status, header, data := request(client, "POST", "/panel/api/login", "", map[string]string{"username": username, "password": password}, nil)
		if status != 200 {
			t.Fatal("维护账号登录失败", status, string(data))
		}
		cookie := header.Get("Set-Cookie")
		if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") || !strings.Contains(cookie, "Path=/panel/") {
			t.Fatal("维护 Cookie 缺少保护", cookie)
		}
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		if json.Unmarshal(data, &session) != nil || session.CSRF == "" {
			t.Fatal("会话缺少CSRF")
		}
		return session.CSRF
	}
	for _, path := range []string{"/panel/api/session", "/panel/api/models", "/panel/api/requests", "/panel/api/cache", "/panel/api/settings", "/panel/api/audit"} {
		status, _, _ := request(client, "GET", path, "", nil, map[string]string{"Authorization": "Bearer " + testToken})
		if status != 401 {
			t.Fatal("服务令牌绕过了面板独立登录", path, status)
		}
	}
	csrf := login(client, "maintainer")
	viewerCSRF := login(viewer, "viewer")
	for _, path := range []string{"/panel/", "/panel/app.js", "/panel/styles.css"} {
		status, header, _ := request(client, "GET", path, "", nil, nil)
		if status != 200 || !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("正式面板静态页面或安全头不可用", path, status)
		}
	}
	for _, path := range []string{"/panel/api/overview", "/panel/api/models", "/panel/api/offerings", "/panel/api/requests", "/panel/api/cache", "/panel/api/settings", "/panel/api/audit"} {
		status, _, data := request(viewer, "GET", path, "", nil, nil)
		if status != 200 {
			t.Fatal("只读维护接口不可用", path, status, string(data))
		}
		for _, secret := range []string{testToken, "test-api-key", password, string(hash), "api_key_file", "service_token_file", "password_hash"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("维护响应泄漏私密配置", path)
			}
		}
	}
	for _, headers := range []map[string]string{{"Origin": "https://cross-site.invalid"}, {"X-CSRF-Token": "invalid"}, {"X-CSRF-Token": ""}} {
		status, _, _ := request(client, "PATCH", "/panel/api/models/standard", csrf, map[string]bool{"enabled": false}, headers)
		if status != 403 {
			t.Fatal("来源或CSRF校验被绕过", status)
		}
	}
	if status, _, _ := request(viewer, "PATCH", "/panel/api/models/standard", viewerCSRF, map[string]bool{"enabled": false}, nil); status != 403 {
		t.Fatal("只读账号修改了模型")
	}
	if status, _, _ := request(client, "PATCH", "/panel/api/models/standard", csrf, map[string]bool{"enabled": false}, nil); status != 200 {
		t.Fatal("维护员停用模型失败", status)
	}
	input := v2Request("停用状态核对题", "A. 第一项", "always_llm", "panel-disabled-request", "unused", nil)
	if result := answerV2(t, f, input, "panel-disabled-request"); result.Code != 409 {
		t.Fatal("面板状态没有影响实际查询")
	}
	if status, _, _ := request(client, "PATCH", "/panel/api/models/standard", csrf, map[string]bool{"enabled": true}, nil); status != 200 {
		t.Fatal("维护员启用模型失败", status)
	}
	if status, _, _ := request(client, "POST", "/panel/api/models/standard/tests", csrf, map[string]any{"kind": "functional", "confirm_cost": false}, nil); status != 428 {
		t.Fatal("付费功能测试没有确认用量")
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/models/standard/tests", viewerCSRF, map[string]any{"kind": "functional", "confirm_cost": true}, nil); status != 403 {
		t.Fatal("只读账号发起了模型测试")
	}
	one := decodeResponse(t, answerV2(t, f, input, "panel-live-request"))
	var entryID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM tiku.cache_entries WHERE source_request_id=$1`, one.RequestID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	status, _, data := request(client, "GET", "/panel/api/requests?limit=1", "", nil, nil)
	if status != 200 || !bytes.Contains(data, []byte(one.RequestID)) {
		t.Fatal("维护记录没有显示实际查询", status, string(data))
	}
	if status, _, _ := request(client, "GET", "/panel/api/requests?cursor=bad", "", nil, nil); status != 422 {
		t.Fatal("损坏的分页游标没有被拒绝")
	}
	if status, _, _ := request(client, "GET", "/panel/api/requests/"+one.RequestID, "", nil, nil); status != 200 {
		t.Fatal("实际请求详情不可用", status)
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/cache/"+entryID+"/invalidations", viewerCSRF, map[string]string{"reason": "答案需核对"}, nil); status != 403 {
		t.Fatal("只读账号使缓存失效")
	}
	if status, _, _ := request(client, "POST", "/panel/api/cache/"+entryID+"/invalidations", csrf, map[string]string{"reason": "答案需核对"}, nil); status != 200 {
		t.Fatal("维护员使缓存失效失败", status)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.panel_audit WHERE action='cache.invalidate' AND actor='maintainer'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("缓存操作缺少审计", count, err)
	}
	if status, _, _ := request(client, "GET", "/panel/api/offerings", "", nil, nil); status != 200 {
		t.Fatal("公开条目列表不可用", status)
	}
	offeringBody := func(rate string) map[string]any {
		return map[string]any{
			"display_name": "公开标准模型", "description": "面板发布条目", "provider_ref": "standard",
			"capabilities": []string{"text"}, "enabled": true, "is_default": true, "sort_order": 3,
			"pricing": map[string]any{"version": "panel-price-v1", "input_micros_per_million_tokens": rate,
				"output_micros_per_million_tokens": "2000000", "micros_per_minor": "10000",
				"rounding": "ceil", "rule": "tokens_x_micros_per_million_v1"},
			"cache_policy": map[string]any{"allow": true, "scope": "user", "ttl_days": 7},
		}
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/offerings", viewerCSRF, offeringBody("1000000"), nil); status != 403 {
		t.Fatal("只读账号发布了公开条目")
	}
	if status, _, data := request(client, "POST", "/panel/api/offerings", csrf, map[string]any{"display_name": "无效引用", "provider_ref": "missing-model", "capabilities": []string{"text"}, "enabled": true, "is_default": false, "cache_policy": map[string]any{"allow": true, "scope": "user", "ttl_days": 7}}, nil); status != 422 {
		t.Fatal("不存在的模型引用没有被拒绝", status, string(data))
	}
	createdStatus, _, createdData := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil)
	if createdStatus != 201 {
		t.Fatal("维护员发布公开条目失败", createdStatus, string(createdData))
	}
	var created struct {
		ID           string `json:"id"`
		PublicID     string `json:"public_id"`
		Version      int64  `json:"version"`
		PriceVersion string `json:"price_version"`
		Health       string `json:"health"`
		Enabled      bool   `json:"enabled"`
		Default      bool   `json:"is_default"`
	}
	if json.Unmarshal(createdData, &created) != nil || created.PublicID == "" || created.Version != 1 || created.PriceVersion != "panel-price-v1" || created.Health != "configured" || !created.Enabled || !created.Default {
		t.Fatal("公开条目创建响应不完整", string(createdData))
	}
	if status, _, _ := request(client, "GET", "/panel/api/offerings/"+created.PublicID, "", nil, nil); status != 200 {
		t.Fatal("公开条目详情不可用", status)
	}
	updated := offeringBody("1000000")
	updated["display_name"] = "公开标准模型（更新）"
	updated["version"] = float64(created.Version)
	if status, _, data := request(client, "PATCH", "/panel/api/offerings/"+created.PublicID, csrf, updated, nil); status != 200 {
		t.Fatal("维护员更新公开条目失败", status, string(data))
	}
	updated["version"] = float64(created.Version)
	if status, _, _ := request(client, "PATCH", "/panel/api/offerings/"+created.PublicID, csrf, updated, nil); status != 409 {
		t.Fatal("过期条目版本没有被拒绝")
	}
	changedRate := offeringBody("3000000")
	changedRate["version"] = float64(created.Version + 1)
	if status, _, data := request(client, "PATCH", "/panel/api/offerings/"+created.PublicID, csrf, changedRate, nil); status != 409 {
		t.Fatal("同一价格版本改写费率没有被拒绝", status, string(data))
	}
	if status, _, _ := request(client, "POST", "/panel/api/offerings/"+created.PublicID+"/tests", csrf, map[string]any{"kind": "functional", "confirm_cost": false}, nil); status != 428 {
		t.Fatal("公开条目功能测试没有确认用量")
	}
	if status, _, _ := request(client, "POST", "/panel/api/offerings/"+created.PublicID+"/tests", csrf, map[string]any{"kind": "functional", "confirm_cost": true}, nil); status != 200 {
		t.Fatal("公开条目功能测试失败")
	}
	if status, _, _ := request(client, "GET", "/panel/api/offerings/"+created.PublicID+"/price-versions", "", nil, nil); status != 200 {
		t.Fatal("价格版本记录不可用", status)
	}
	if status, _, _ := request(client, "POST", "/panel/api/offerings/"+created.PublicID+"/retirements", csrf, map[string]any{"version": 1}, nil); status != 409 {
		t.Fatal("过期版本停用条目没有被拒绝", status)
	}
	if status, _, data := request(client, "POST", "/panel/api/offerings/"+created.PublicID+"/retirements", csrf, map[string]any{"version": created.Version + 1}, nil); status != 200 || !strings.Contains(string(data), `"enabled":false`) || !strings.Contains(string(data), `"is_default":false`) {
		t.Fatal("停用公开条目失败", status, string(data))
	}
	upstream := map[string]any{
		"id": "panel-model", "display_name": "面板新增模型", "protocol": "openai-chat",
		"base_url": f.llm.server.URL + "/v1", "upstream_model": "fake-answer", "api_key": "panel-secret-key",
		"context": 128000, "max_output_tokens": 512,
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/upstream-models", viewerCSRF, upstream, nil); status != 403 {
		t.Fatal("只读账号新增了上游模型", status)
	}
	if status, _, data := request(client, "POST", "/panel/api/upstream-models", csrf, upstream, nil); status != 201 {
		t.Fatal("维护员新增上游模型失败", status, string(data))
	}
	status, _, data = request(client, "GET", "/panel/api/models", "", nil, nil)
	if status != 200 || !strings.Contains(string(data), "panel-model") || !strings.Contains(string(data), `"key_configured":true`) || strings.Contains(string(data), "panel-secret-key") {
		t.Fatal("上游模型列表不完整或泄漏密钥", status, string(data))
	}
	if result := decodeResponse(t, answerV2(t, f, v2Request("热加载模型题", "A. 甲", "always_llm", "panel-hot-model", "panel-hot-model-key", nil), "panel-hot-model-key")); result.Answer != "A" {
		t.Fatal("新增模型未热加载到作答服务")
	}
	edited := map[string]any{"display_name": "面板更新模型", "protocol": "openai-chat", "base_url": f.llm.server.URL + "/v1",
		"upstream_model": "fake-answer", "context": 128000, "max_output_tokens": 512}
	if status, _, data := request(client, "PATCH", "/panel/api/upstream-models/panel-model", csrf, edited, nil); status != 200 {
		t.Fatal("更新上游模型失败", status, string(data))
	}
	status, _, data = request(client, "GET", "/panel/api/models", "", nil, nil)
	if status != 200 || !strings.Contains(string(data), "面板更新模型") {
		t.Fatal("更新后的模型未生效", status, string(data))
	}
	if status, _, data := request(client, "DELETE", "/panel/api/upstream-models/panel-model", csrf, nil, nil); status != 200 {
		t.Fatal("删除上游模型失败", status, string(data))
	}
	if status, _, data := request(client, "GET", "/panel/api/models", "", nil, nil); status != 200 || strings.Contains(string(data), "panel-model") {
		t.Fatal("删除后的模型仍在列表", status, string(data))
	}
	provider := map[string]any{"id": "panel-provider", "name": "测试提供商", "protocol": "openai-chat", "base_url": f.llm.server.URL + "/v1", "api_key": "provider-private-key"}
	if status, _, _ := request(viewer, "POST", "/panel/api/providers", viewerCSRF, provider, nil); status != 403 {
		t.Fatal("只读账号新增了提供商")
	}
	if status, _, data := request(client, "POST", "/panel/api/providers", csrf, provider, nil); status != 201 {
		t.Fatal("新增提供商失败", status, string(data))
	}
	for _, id := range []string{"shared-model-one", "shared-model-two"} {
		model := map[string]any{"id": id, "provider_id": "panel-provider", "display_name": id, "upstream_model": "fake-answer"}
		if status, _, data := request(client, "POST", "/panel/api/upstream-models", csrf, model, nil); status != 201 {
			t.Fatal("新增关联模型失败", status, string(data))
		}
	}
	if status, _, data := request(client, "GET", "/panel/api/providers", "", nil, nil); status != 200 || !bytes.Contains(data, []byte(`"model_count":2`)) || bytes.Contains(data, []byte("provider-private-key")) || bytes.Contains(data, []byte("api_key_file")) {
		t.Fatal("提供商列表或密钥保护异常", status, string(data))
	}
	if status, _, _ := request(client, "DELETE", "/panel/api/providers/panel-provider", csrf, nil, nil); status != 409 {
		t.Fatal("仍被引用的提供商应拒绝删除")
	}
	retiredBody := offeringBody("1000000")
	retiredBody["provider_ref"], retiredBody["enabled"], retiredBody["is_default"] = "shared-model-one", false, false
	status, _, retiredData := request(client, "POST", "/panel/api/offerings", csrf, retiredBody, nil)
	var retired struct {
		PublicID string `json:"public_id"`
		Version  int64  `json:"version"`
	}
	if status != 201 || json.Unmarshal(retiredData, &retired) != nil {
		t.Fatal("创建已下架条目失败", status, string(retiredData))
	}
	if status, _, _ := request(client, "DELETE", "/panel/api/upstream-models/shared-model-one", csrf, nil, nil); status != 409 {
		t.Fatal("已下架条目仍在引用时不得删除模型")
	}
	retiredBody["provider_ref"], retiredBody["version"] = "standard", retired.Version
	if status, _, data := request(client, "PATCH", "/panel/api/offerings/"+retired.PublicID, csrf, retiredBody, nil); status != 200 {
		t.Fatal("重新关联公开条目失败", status, string(data))
	}
	for _, id := range []string{"shared-model-one", "shared-model-two"} {
		queryID := "provider-query-" + id
		input := v2Request("提供商模型题", "A. 甲", "always_llm", queryID, queryID, nil)
		input.ModelOfferingID = id
		if result := decodeResponse(t, answerV2(t, f, input, queryID)); result.Answer != "A" {
			t.Fatal("提供商模型没有正常作答")
		}
		if status, _, data := request(client, "DELETE", "/panel/api/upstream-models/"+id, csrf, nil, nil); status != 200 {
			t.Fatal("删除关联模型失败", status, string(data))
		}
	}
	if status, _, data := request(client, "DELETE", "/panel/api/providers/panel-provider", csrf, nil, nil); status != 200 {
		t.Fatal("删除空提供商失败", status, string(data))
	}
	// 新默认项会改变原默认项的版本，避免过期表单覆盖并发修改。
	firstStatus, _, firstData := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil)
	if firstStatus != 201 {
		t.Fatal("新增默认条目失败", firstStatus, string(firstData))
	}
	var first struct {
		PublicID string `json:"public_id"`
		Version  int64  `json:"version"`
	}
	if err := json.Unmarshal(firstData, &first); err != nil {
		t.Fatal(err)
	}
	if status, _, data := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil); status != 201 {
		t.Fatal("切换默认条目失败", status, string(data))
	}
	stale := offeringBody("1000000")
	stale["version"] = first.Version
	if status, _, _ := request(client, "PATCH", "/panel/api/offerings/"+first.PublicID, csrf, stale, nil); status != 409 {
		t.Fatal("旧默认条目的过期版本没有被拒绝")
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.panel_audit WHERE action='offering.create'`).Scan(&count); err != nil || count != 4 {
		t.Fatal("公开条目与审计未一起保存", count, err)
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE tiku.panel_audit ADD CONSTRAINT fixture_reject_audit CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	blocked := map[string]any{"id": "blocked-provider", "name": "不可写入", "protocol": "openai-chat", "base_url": f.llm.server.URL + "/v1", "api_key": "blocked-key"}
	if status, _, _ := request(client, "POST", "/panel/api/providers", csrf, blocked, nil); status != 503 {
		t.Fatal("审计不可用时不应改写提供商", status)
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE tiku.panel_audit DROP CONSTRAINT fixture_reject_audit`); err != nil {
		t.Fatal(err)
	}
	if status, _, data := request(client, "GET", "/panel/api/providers", "", nil, nil); status != 200 || bytes.Contains(data, []byte("blocked-provider")) {
		t.Fatal("审计失败后提供商配置仍被修改", status, string(data))
	}
	if status, _, _ := request(client, "POST", "/panel/api/logout", csrf, map[string]any{}, nil); status != 200 {
		t.Fatal("无法退出维护会话")
	}
	if status, _, _ := request(client, "GET", "/panel/api/session", "", nil, nil); status != 401 {
		t.Fatal("退出后会话仍可用")
	}
}
