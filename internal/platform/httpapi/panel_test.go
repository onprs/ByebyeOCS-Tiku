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
	status, _, overviewData := request(client, "GET", "/panel/api/overview", "", nil, nil)
	var overview struct {
		Daily []struct {
			Date      string `json:"date"`
			Queries   int    `json:"queries"`
			CacheHits int    `json:"cache_hits"`
		} `json:"daily"`
	}
	if status != 200 || json.Unmarshal(overviewData, &overview) != nil || len(overview.Daily) != 14 ||
		overview.Daily[0].Date == "" || overview.Daily[13].Date == "" {
		t.Fatal("运行总览缺少连续 14 天趋势数据", status, string(overviewData))
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
				"output_micros_per_million_tokens": "2000000", "cache_read_micros_per_million_tokens": "500000",
				"cache_write_micros_per_million_tokens": "1500000",
				"reference": map[string]any{"source": "models.dev", "source_url": "https://models.dev/api.json",
					"exchange_source": "Frankfurter", "exchange_url": "https://api.frankfurter.dev/v2/rate/usd/cny",
					"exchange_date": "2026-10-30", "exchange_rate": "6.6572"},
				"micros_per_minor": "10000", "rounding": "ceil", "rule": "tokens_x_micros_per_million_v1"},
			"cache_policy": map[string]any{"allow": true, "scope": "user", "ttl_days": 7},
		}
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/offerings", viewerCSRF, offeringBody("1000000"), nil); status != 403 {
		t.Fatal("只读账号发布了公开条目")
	}
	if status, _, data := request(client, "POST", "/panel/api/offerings", csrf, map[string]any{"display_name": "无效引用", "provider_ref": "missing-model", "capabilities": []string{"text"}, "enabled": true, "is_default": false, "cache_policy": map[string]any{"allow": true, "scope": "user", "ttl_days": 7}}, nil); status != 422 {
		t.Fatal("不存在的模型引用没有被拒绝", status, string(data))
	}
	f.llm.setMode("discovery_rate_limited")
	status, _, data = request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil)
	f.llm.setMode("")
	if status != http.StatusTooManyRequests || !bytes.Contains(data, []byte(`"code":"upstream_rate_limited"`)) {
		t.Fatal("上游连通检查失败时仍允许发布", status, string(data))
	}
	createdStatus, _, createdData := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil)
	if createdStatus != 201 {
		t.Fatal("维护员发布公开条目失败", createdStatus, string(createdData))
	}
	var created struct {
		ID           string          `json:"id"`
		PublicID     string          `json:"public_id"`
		Version      int64           `json:"version"`
		PriceVersion string          `json:"price_version"`
		Health       string          `json:"health"`
		Enabled      bool            `json:"enabled"`
		Default      bool            `json:"is_default"`
		Pricing      json.RawMessage `json:"pricing"`
	}
	if json.Unmarshal(createdData, &created) != nil || created.PublicID == "" || created.Version != 1 || created.PriceVersion != "panel-price-v1" || created.Health != "configured" || !created.Enabled || !created.Default ||
		!bytes.Contains(created.Pricing, []byte(`"cache_read_micros_per_million_tokens":"500000"`)) ||
		!bytes.Contains(created.Pricing, []byte(`"cache_write_micros_per_million_tokens":"1500000"`)) ||
		!bytes.Contains(created.Pricing, []byte(`"exchange_date":"2026-10-30"`)) {
		t.Fatal("公开条目创建响应或缓存价格快照不完整", string(createdData))
	}
	if status, _, data := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil); status != http.StatusConflict || !bytes.Contains(data, []byte(`"code":"model_already_published"`)) {
		t.Fatal("同一上游模型被允许发布第二个公开条目", status, string(data))
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
	if status, _, data := request(viewer, "DELETE", "/panel/api/offerings/"+created.PublicID, viewerCSRF, map[string]any{"version": created.Version + 2}, nil); status != 403 {
		t.Fatal("只读账号删除了公开条目", status, string(data))
	}
	if status, _, _ := request(client, "DELETE", "/panel/api/offerings/"+created.PublicID, csrf, map[string]any{"version": created.Version + 1}, nil); status != 409 {
		t.Fatal("过期版本删除条目没有被拒绝", status)
	}
	if status, _, data := request(client, "DELETE", "/panel/api/offerings/"+created.PublicID, csrf, map[string]any{"version": created.Version + 2}, nil); status != 200 || !bytes.Contains(data, []byte(`"deleted":true`)) {
		t.Fatal("删除公开条目失败", status, string(data))
	}
	if status, _, _ := request(client, "GET", "/panel/api/offerings/"+created.PublicID, "", nil, nil); status != 404 {
		t.Fatal("删除后的公开条目仍可从当前目录读取", status)
	}
	if status, _, data := request(client, "GET", "/panel/api/offerings/"+created.PublicID+"/price-versions", "", nil, nil); status != 200 || !bytes.Contains(data, []byte("panel-price-v1")) {
		t.Fatal("删除后历史价格版本不可读取", status, string(data))
	}
	if status, _, data := request(client, "GET", "/panel/api/offerings", "", nil, nil); status != 200 || bytes.Contains(data, []byte(created.PublicID)) {
		t.Fatal("删除后的条目仍出现在当前目录", status, string(data))
	}
	var deleted, enabled, isDefault bool
	var priceVersions int
	if err := f.pool.QueryRow(context.Background(), `SELECT deleted_at IS NOT NULL, enabled, is_default FROM tiku.offerings WHERE public_id=$1`, created.PublicID).Scan(&deleted, &enabled, &isDefault); err != nil || !deleted || enabled || isDefault {
		t.Fatal("删除标记或公开状态未原子保存", deleted, enabled, isDefault, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.offering_price_versions WHERE offering_id=(SELECT id FROM tiku.offerings WHERE public_id=$1)`, created.PublicID).Scan(&priceVersions); err != nil || priceVersions != 1 {
		t.Fatal("删除公开条目影响了不可变价格版本", priceVersions, err)
	}
	var deletionAuditCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.panel_audit WHERE action='offering.delete' AND resource_id=$1 AND actor='maintainer'`, created.PublicID).Scan(&deletionAuditCount); err != nil || deletionAuditCount != 1 {
		t.Fatal("公开条目删除缺少审计记录", deletionAuditCount, err)
	}
	republishedStatus, _, republishedData := request(client, "POST", "/panel/api/offerings", csrf, offeringBody("1000000"), nil)
	var republished struct {
		PublicID string `json:"public_id"`
	}
	if republishedStatus != 201 || json.Unmarshal(republishedData, &republished) != nil || republished.PublicID == created.PublicID {
		t.Fatal("删除条目后模型绑定没有释放", republishedStatus, string(republishedData))
	}
	created.PublicID = republished.PublicID
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
	if status, _, data := request(client, "PATCH", "/panel/api/upstream-models/standard", csrf, map[string]any{
		"display_name": "测试模型", "protocol": "openai-chat", "base_url": f.llm.server.URL + "/v1",
		"upstream_model": "baseline-other", "context": 128000, "max_output_tokens": 512,
	}, nil); status != 200 {
		t.Fatal("隔离模型目录状态失败", status, string(data))
	}
	provider := map[string]any{"name": "测试提供商", "protocol": "openai-chat", "base_url": f.llm.server.URL + "/v1", "api_key": "provider-private-key"}
	var createdProvider struct {
		ID string `json:"id"`
	}
	temporary := map[string]any{"protocol": "openai-chat", "vendor": "fixture", "base_url": f.llm.server.URL + "/v1", "api_key": "temporary-discovery-secret"}
	if status, _, data := request(client, "POST", "/panel/api/providers/discovery", csrf, temporary, nil); status != 200 || bytes.Contains(data, []byte("temporary-discovery-secret")) {
		t.Fatal("临时密钥目录发现失败或泄漏密钥", status, string(data))
	}
	for _, test := range []struct {
		client  *http.Client
		csrf    string
		headers map[string]string
	}{
		{client: client, csrf: csrf, headers: map[string]string{"Origin": "https://cross-site.invalid"}},
		{client: viewer, csrf: viewerCSRF},
		{client: client},
	} {
		if status, _, _ := request(test.client, "POST", "/panel/api/providers/discovery", test.csrf, temporary, test.headers); status != 403 {
			t.Fatal("模型发现绕过 CSRF、来源或维护员权限", status)
		}
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/providers/panel-provider/models", viewerCSRF, map[string]any{}, nil); status != 403 {
		t.Fatal("只读账号读取上游模型目录")
	}
	if status, _, _ := request(client, "POST", "/panel/api/providers/panel-provider/models", "", map[string]any{}, nil); status != 403 {
		t.Fatal("模型目录请求没有校验 CSRF")
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/providers", viewerCSRF, provider, nil); status != 403 {
		t.Fatal("只读账号新增了提供商")
	}
	createdProviderStatus, _, createdProviderData := request(client, "POST", "/panel/api/providers", csrf, provider, nil)
	if createdProviderStatus != 201 || json.Unmarshal(createdProviderData, &createdProvider) != nil || !strings.HasPrefix(createdProvider.ID, "provider-") {
		t.Fatal("新增提供商未自动生成标识", createdProviderStatus, string(createdProviderData))
	}
	providerID := createdProvider.ID
	if status, _, data := request(client, "POST", "/panel/api/providers/"+providerID+"/models", csrf, map[string]any{}, nil); status != 200 || bytes.Contains(data, []byte("provider-private-key")) {
		t.Fatal("已保存提供商模型发现失败或泄漏密钥", status, string(data))
	}
	f.llm.setMode("discovery_rate_limited")
	status, _, data = request(client, "POST", "/panel/api/providers/"+providerID+"/models", csrf, map[string]any{}, nil)
	f.llm.setMode("")
	if status != http.StatusTooManyRequests || !bytes.Contains(data, []byte(`"code":"upstream_rate_limited"`)) || bytes.Contains(data, []byte("provider-private-key")) || bytes.Contains(data, []byte("internal.example")) {
		t.Fatal("上游限流未被稳定分类或错误响应泄漏内容", status, string(data))
	}
	var discoveryAudit string
	if err := f.pool.QueryRow(context.Background(), `SELECT string_agg(detail::text, ' ') FROM tiku.panel_audit WHERE action IN ('provider.models.request','provider.models.result')`).Scan(&discoveryAudit); err != nil || strings.Contains(discoveryAudit, "temporary-discovery-secret") || strings.Contains(discoveryAudit, "provider-private-key") || !strings.Contains(discoveryAudit, "upstream_rate_limited") {
		t.Fatal("模型发现审计缺少安全的结果事实", discoveryAudit, err)
	}
	for _, id := range []string{"shared-model-one", "shared-model-two"} {
		model := map[string]any{"id": id, "provider_id": providerID, "display_name": id, "upstream_model": "fake-answer"}
		if status, _, data := request(client, "POST", "/panel/api/upstream-models", csrf, model, nil); status != 201 {
			t.Fatal("新增关联模型失败", status, string(data))
		}
	}
	configuredModels := func() map[string]string {
		status, _, data := request(client, "POST", "/panel/api/providers/"+providerID+"/models", csrf, map[string]any{}, nil)
		var result struct {
			Items []struct {
				ID                string `json:"id"`
				ConfiguredModelID string `json:"configured_model_id"`
			} `json:"items"`
		}
		if status != 200 || json.Unmarshal(data, &result) != nil {
			t.Fatal("已配置模型目录读取失败", status, string(data))
		}
		configured := make(map[string]string, len(result.Items))
		for _, item := range result.Items {
			configured[item.ID] = item.ConfiguredModelID
		}
		return configured
	}
	if configuredModels()["fake-answer"] == "" {
		t.Fatal("引用提供商的已配置模型没有在目录中标记")
	}
	if status, _, data := request(client, "GET", "/panel/api/providers", "", nil, nil); status != 200 || !bytes.Contains(data, []byte(`"model_count":2`)) || bytes.Contains(data, []byte("provider-private-key")) || bytes.Contains(data, []byte("api_key_file")) {
		t.Fatal("提供商列表或密钥保护异常", status, string(data))
	}
	if status, _, _ := request(client, "DELETE", "/panel/api/providers/"+providerID, csrf, nil, nil); status != 409 {
		t.Fatal("仍被引用的提供商应拒绝删除")
	}
	retiredBody := offeringBody("1000000")
	retiredBody["provider_ref"], retiredBody["enabled"], retiredBody["is_default"] = "shared-model-one", true, false
	status, _, retiredData := request(client, "POST", "/panel/api/offerings", csrf, retiredBody, nil)
	var retired struct {
		PublicID string `json:"public_id"`
		Version  int64  `json:"version"`
	}
	if status != 201 || json.Unmarshal(retiredData, &retired) != nil {
		t.Fatal("创建已下架条目失败", status, string(retiredData))
	}
	deleteStatus, _, deleteData := request(client, "DELETE", "/panel/api/upstream-models/shared-model-one", csrf, nil, nil)
	var deletion struct {
		RemovedOfferings int `json:"removed_offerings"`
	}
	if deleteStatus != 200 || json.Unmarshal(deleteData, &deletion) != nil || deletion.RemovedOfferings != 1 {
		t.Fatal("删除模型应移除关联条目", deleteStatus, string(deleteData))
	}
	status, _, retiredData = request(client, "GET", "/panel/api/offerings/"+retired.PublicID, "", nil, nil)
	if status != 404 {
		t.Fatal("删除模型后关联条目仍在当前目录", status, string(retiredData))
	}
	if status, _, retiredData = request(client, "GET", "/panel/api/offerings/"+retired.PublicID+"/price-versions", "", nil, nil); status != 200 || !bytes.Contains(retiredData, []byte("panel-price-v1")) {
		t.Fatal("删除模型影响了关联条目的价格历史", status, string(retiredData))
	}
	if configured := configuredModels(); configured["fake-answer"] == "" {
		t.Fatal("删除一个引用模型错误地抹除了仍存在模型的目录状态", configured)
	}
	for _, id := range []string{"shared-model-two"} {
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
	if configured := configuredModels(); configured["fake-answer"] != "" {
		t.Fatal("删除最后一个引用模型后目录仍标记为已配置", configured)
	}
	if status, _, data := request(client, "DELETE", "/panel/api/providers/"+providerID, csrf, nil, nil); status != 200 {
		t.Fatal("删除空提供商失败", status, string(data))
	}
	// 新默认项会改变原默认项的版本，避免过期表单覆盖并发修改。
	anotherOfferingBody := offeringBody("1000000")
	anotherOfferingBody["provider_ref"] = "caption-model"
	firstStatus, _, firstData := request(client, "POST", "/panel/api/offerings", csrf, anotherOfferingBody, nil)
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
	var currentStandard struct {
		Version int64 `json:"version"`
	}
	if status, _, data := request(client, "GET", "/panel/api/offerings/"+created.PublicID, "", nil, nil); status != 200 || json.Unmarshal(data, &currentStandard) != nil {
		t.Fatal("读取当前默认条目版本失败", status, string(data))
	}
	conflictingUpdate := offeringBody("1000000")
	conflictingUpdate["provider_ref"], conflictingUpdate["version"] = "caption-model", currentStandard.Version
	if status, _, data := request(client, "PATCH", "/panel/api/offerings/"+created.PublicID, csrf, conflictingUpdate, nil); status != http.StatusConflict || !bytes.Contains(data, []byte(`"code":"model_already_published"`)) {
		t.Fatal("编辑公开条目时允许关联到已有条目的模型", status, string(data))
	}
	standardUpdate := offeringBody("1000000")
	standardUpdate["version"] = currentStandard.Version
	if status, _, data := request(client, "PATCH", "/panel/api/offerings/"+created.PublicID, csrf, standardUpdate, nil); status != 200 {
		t.Fatal("切换默认条目失败", status, string(data))
	}
	stale := anotherOfferingBody
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
	settingsPayload := map[string]any{
		"caption_model_id": "caption-model", "concurrency": 3, "timeout_seconds": 10, "max_output_tokens": 320,
		"cache_ttl_days": 5, "cache_max_ttl_days": 20, "shared_cache_enabled": true,
		"pricing_catalog_ttl_hours": 12, "pricing_exchange_ttl_hours": 36,
	}
	if status, _, _ := request(viewer, "PATCH", "/panel/api/settings", viewerCSRF, settingsPayload, nil); status != 403 {
		t.Fatal("只读账号修改了服务设置")
	}
	if status, _, _ := request(viewer, "POST", "/panel/api/settings/pricing-cache/refresh", viewerCSRF, map[string]any{}, nil); status != 403 {
		t.Fatal("只读账号刷新了价格缓存")
	}
	invalidSettings := map[string]any{}
	for key, value := range settingsPayload {
		invalidSettings[key] = value
	}
	invalidSettings["concurrency"] = 0
	if status, _, data := request(client, "PATCH", "/panel/api/settings", csrf, invalidSettings, nil); status != 422 {
		t.Fatal("无效服务设置没有被拒绝", status, string(data))
	}
	status, _, settingsData := request(client, "PATCH", "/panel/api/settings", csrf, settingsPayload, nil)
	if status != 200 || !bytes.Contains(settingsData, []byte(`"pricing_catalog_ttl_hours":12`)) ||
		!bytes.Contains(settingsData, []byte(`"restart_required":true`)) || !bytes.Contains(settingsData, []byte(`"catalog_ttl_hours":12`)) {
		t.Fatal("服务设置保存失败", status, string(settingsData))
	}
	loaded, err := config.Load(f.cfg.SourcePath)
	if err != nil || loaded.Limits.Concurrency != 3 || loaded.Limits.TimeoutSeconds != 10 || loaded.Limits.MaxOutputTokens != 320 ||
		loaded.Cache.TTLDays != 5 || loaded.Cache.MaxTTLDays != 20 || !loaded.Cache.SharedEnabled || loaded.CaptionModelID != "caption-model" ||
		loaded.PricingCache.CatalogTTLHours != 12 || loaded.PricingCache.ExchangeTTLHours != 36 || len(loaded.Models) != 2 {
		t.Fatalf("服务设置没有完整持久化：%+v/%v", loaded, err)
	}
	var settingsAudit int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.panel_audit WHERE action='settings.update' AND actor='maintainer'`).Scan(&settingsAudit); err != nil || settingsAudit != 1 {
		t.Fatal("服务设置缺少审计", settingsAudit, err)
	}
	status, _, settingsData = request(client, "GET", "/panel/api/settings", "", nil, nil)
	if status != 200 || !bytes.Contains(settingsData, []byte(`"caption_model_id":"caption-model"`)) ||
		!bytes.Contains(settingsData, []byte(`"pricing_cache"`)) || !bytes.Contains(settingsData, []byte(`"service"`)) {
		t.Fatal("服务设置读取不完整", status, string(settingsData))
	}
	if status, _, _ := request(client, "POST", "/panel/api/logout", csrf, map[string]any{}, nil); status != 200 {
		t.Fatal("无法退出维护会话")
	}
	if status, _, _ := request(client, "GET", "/panel/api/session", "", nil, nil); status != 401 {
		t.Fatal("退出后会话仍可用")
	}

	autoWeb := httptest.NewUnstartedServer(nil)
	autoOrigin := "http://" + autoWeb.Listener.Addr().String()
	autoCfg := *f.cfg
	autoCfg.Panel = config.Panel{Enabled: true, Origin: autoOrigin, LocalAutoLogin: true, Users: f.cfg.Panel.Users}
	autoHandler, err := panel.New(&autoCfg, f.svc, f.store, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	autoWeb.Config.Handler = panel.Mount(f.handler, autoHandler)
	autoWeb.Start()
	t.Cleanup(autoWeb.Close)
	autoClient := autoWeb.Client()
	sessionResponse, err := autoClient.Get(autoOrigin + "/panel/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var localSession struct {
		Username  string `json:"username"`
		Role      string `json:"role"`
		CSRF      string `json:"csrf_token"`
		AutoLogin bool   `json:"local_auto_login"`
	}
	sessionBody, err := io.ReadAll(sessionResponse.Body)
	sessionResponse.Body.Close()
	if err != nil || sessionResponse.StatusCode != 200 || json.Unmarshal(sessionBody, &localSession) != nil ||
		localSession.Username != "maintainer" || localSession.Role != "maintainer" || !localSession.AutoLogin || localSession.CSRF == "" {
		t.Fatal("回环本地预览没有自动建立维护员会话", sessionResponse.StatusCode, string(sessionBody), err)
	}
	cookies := sessionResponse.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "tiku_panel_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("本地自动会话 Cookie 缺少保护", cookies)
	}
	var localLogins int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tiku.panel_audit WHERE action='session.local_login'`).Scan(&localLogins); err != nil || localLogins != 1 {
		t.Fatal("本地自动登录缺少审计", localLogins, err)
	}
	writeRequest, err := http.NewRequest("PATCH", autoOrigin+"/panel/api/models/standard", strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	writeRequest.Header.Set("Origin", autoOrigin)
	writeRequest.Header.Set("X-CSRF-Token", localSession.CSRF)
	writeRequest.Header.Set("Content-Type", "application/json")
	writeRequest.AddCookie(cookies[0])
	writeResponse, err := autoClient.Do(writeRequest)
	if err != nil {
		t.Fatal(err)
	}
	writeResponse.Body.Close()
	if writeResponse.StatusCode != 200 {
		t.Fatal("本地自动会话没有维护员写权限", writeResponse.StatusCode)
	}
	evil, err := http.NewRequest("GET", autoOrigin+"/panel/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	evil.Host = "evil.example"
	evilResponse, err := autoClient.Do(evil)
	if err != nil {
		t.Fatal(err)
	}
	evilResponse.Body.Close()
	if evilResponse.StatusCode != 401 {
		t.Fatal("非回环 Host 不应触发本地自动登录", evilResponse.StatusCode)
	}
}
