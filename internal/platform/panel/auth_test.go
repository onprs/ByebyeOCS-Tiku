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
