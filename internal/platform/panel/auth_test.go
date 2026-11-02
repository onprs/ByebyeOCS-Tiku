package panel

import (
	"crypto/sha256"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"golang.org/x/crypto/bcrypt"
)

func TestMaintenanceSessionsExpireAndLoginAttemptsAreBounded(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("Isolated-Panel-Password!"), 10)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "panel.hash")
	if err := os.WriteFile(path, hash, 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := newAuthentication(config.Panel{Enabled: true, Users: []config.PanelUser{{Username: "maintenance", Role: "maintainer", PasswordHashFile: path}}})
	if err != nil {
		t.Fatal(err)
	}
	if !auth.authenticate("maintenance", "Isolated-Panel-Password!") || auth.authenticate("maintenance", "incorrect") || auth.authenticate("missing", "Isolated-Panel-Password!") {
		t.Fatal("维护账号验证异常")
	}
	token, _, err := auth.create("maintenance")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://localhost/panel/api/session", nil)
	request.Header.Set("Cookie", sessionCookie+"="+token)
	if _, ok := auth.current(request); !ok {
		t.Fatal("新会话不可用")
	}
	key := sha256.Sum256([]byte(token))
	auth.mu.Lock()
	expired := auth.sessions[key]
	expired.Expires = time.Now().Add(-time.Second)
	auth.sessions[key] = expired
	auth.mu.Unlock()
	if _, ok := auth.current(request); ok {
		t.Fatal("过期会话仍可读取维护数据")
	}
	attempt := httptest.NewRequest("POST", "http://localhost/panel/api/login", nil)
	attempt.RemoteAddr = "127.0.0.1:10000"
	for range 20 {
		if !auth.permitLogin(attempt) {
			t.Fatal("限额前拒绝了登录")
		}
	}
	if auth.permitLogin(attempt) {
		t.Fatal("登录失败预算没有限制")
	}
	response := httptest.NewRecorder()
	setCookie(response, token, true, 3600)
	cookie := response.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || !strings.HasPrefix(cookie.Path, "/panel/") {
		t.Fatal("公网维护会话 Cookie 缺少保护")
	}
}

func TestLocalAutoLoginCreatesMaintainerSessionOnly(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("Isolated-Panel-Password!"), 10)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "panel.hash")
	if err := os.WriteFile(path, hash, 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := newAuthentication(config.Panel{Enabled: true, Users: []config.PanelUser{
		{Username: "reader", Role: "viewer", PasswordHashFile: path},
		{Username: "maintenance", Role: "maintainer", PasswordHashFile: path},
	}})
	if err != nil {
		t.Fatal(err)
	}
	token, current, err := auth.createLocal()
	if err != nil || token == "" || !current.AutoLogin || current.Role != "maintainer" || current.Username != "maintenance" || current.CSRF == "" {
		t.Fatalf("本地自动登录没有建立维护员会话：%+v/%v", current, err)
	}
	request := httptest.NewRequest("GET", "http://127.0.0.1/panel/api/session", nil)
	request.Header.Set("Cookie", sessionCookie+"="+token)
	if stored, ok := auth.current(request); !ok || !stored.AutoLogin || stored.Role != "maintainer" {
		t.Fatal("本地自动会话不可用或权限不足")
	}
	viewerOnly, err := newAuthentication(config.Panel{Enabled: true, Users: []config.PanelUser{{Username: "reader", Role: "viewer", PasswordHashFile: path}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := viewerOnly.createLocal(); err == nil {
		t.Fatal("没有维护员账号时仍建立了本地会话")
	}
}

func TestLoopbackRequestRejectsNonLoopbackSourceOrHost(t *testing.T) {
	for _, test := range []struct {
		remote, host string
		want         bool
	}{
		{"127.0.0.1:1234", "127.0.0.1:5193", true},
		{"[::1]:1234", "[::1]:5193", true},
		{"127.0.0.1:1234", "localhost:5193", true},
		{"192.168.1.5:1234", "127.0.0.1:5193", false},
		{"127.0.0.1:1234", "evil.example", false},
		{"127.0.0.1:1234", "", false},
	} {
		request := httptest.NewRequest("GET", "http://127.0.0.1/panel/api/session", nil)
		request.RemoteAddr = test.remote
		request.Host = test.host
		if got := loopbackRequest(request); got != test.want {
			t.Fatalf("%s / %s 回环判定错误：got=%v want=%v", test.remote, test.host, got, test.want)
		}
	}
}

func TestMaintenanceCredentialFilesRequireHashesAndPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.hash")
	cfg := config.Panel{Enabled: true, Users: []config.PanelUser{{Username: "maintenance", Role: "maintainer", PasswordHashFile: path}}}
	if err := os.WriteFile(path, []byte("plaintext-password"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(cfg); err == nil {
		t.Fatal("明文被当作密码哈希")
	}
	for _, password := range []string{"short", strings.Repeat("x", 73)} {
		if _, err := HashPassword(password); err == nil {
			t.Fatal("过短或过长密码被接受")
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("Isolated-Panel-Password!"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, hash, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(cfg); err != nil {
		t.Fatal(err)
	}
	// Windows 的文件权限位不代表 ACL；这里只核对 Unix 的宽松权限拒绝。
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(cfg); err == nil {
		t.Fatal("宽松权限的凭据文件被接受")
	}
}
