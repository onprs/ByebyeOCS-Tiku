// Package panel 提供独立题库维护面板、会话与最小权限控制。
package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "tiku_panel_session"

// HashPassword 只生成独立维护账号的密码哈希，不使用内部服务令牌。
func HashPassword(password string) (string, error) {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 72 {
		return "", errors.New("维护密码至少12个字符，且不超过72字节")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", errors.New("无法生成维护密码哈希")
	}
	return string(hash), nil
}

func LoadCredentials(cfg config.Panel) (map[string]string, error) {
	result := make(map[string]string)
	if !cfg.Enabled {
		return result, nil
	}
	for _, account := range cfg.Users {
		info, err := os.Stat(account.PasswordHashFile)
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("维护账号密码哈希文件必须为私密普通文件")
		}
		hash, err := config.ReadSecretFile(account.PasswordHashFile)
		if err != nil {
			return nil, errors.New("维护账号密码哈希文件不可用")
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil || cost < 10 || cost > 14 {
			return nil, errors.New("维护账号密码哈希格式或成本无效")
		}
		result[account.Username] = hash
	}
	return result, nil
}

type session struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CSRF      string    `json:"csrf_token"`
	Expires   time.Time `json:"expires_at"`
	AutoLogin bool      `json:"local_auto_login,omitempty"`
}

type loginWindow struct {
	Attempts int
	Until    time.Time
}
type authentication struct {
	mu        sync.Mutex
	sessions  map[[32]byte]session
	attempts  map[string]loginWindow
	hashes    map[string]string
	roles     map[string]string
	localUser string
	dummy     string
	semaphore chan struct{}
}

func newAuthentication(cfg config.Panel) (*authentication, error) {
	hashes, err := LoadCredentials(cfg)
	if err != nil {
		return nil, err
	}
	roles := make(map[string]string)
	localUser := ""
	for _, account := range cfg.Users {
		roles[account.Username] = account.Role
		if localUser == "" && account.Role == "maintainer" {
			localUser = account.Username
		}
	}
	dummy, err := HashPassword("unavailable-maintenance-account")
	if err != nil {
		return nil, err
	}
	return &authentication{sessions: make(map[[32]byte]session), attempts: make(map[string]loginWindow), hashes: hashes,
		roles: roles, localUser: localUser, dummy: dummy, semaphore: make(chan struct{}, 2)}, nil
}

func randomToken() (string, error) {
	var buffer [32]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer[:]), nil
}

func (a *authentication) permitLogin(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, window := range a.attempts {
		if !now.Before(window.Until) {
			delete(a.attempts, key)
		}
	}
	window, exists := a.attempts[ip]
	if !exists {
		if len(a.attempts) >= 1024 {
			return false
		}
		window.Until = now.Add(15 * time.Minute)
	}
	if window.Attempts >= 20 {
		return false
	}
	window.Attempts++
	a.attempts[ip] = window
	return true
}

func (a *authentication) authenticate(username, password string) bool {
	hash, found := a.hashes[username]
	if !found {
		hash = a.dummy
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return found && err == nil
}

func (a *authentication) current(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) > 128 {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sha256.Sum256([]byte(cookie.Value))
	value, ok := a.sessions[key]
	if ok && !time.Now().Before(value.Expires) {
		delete(a.sessions, key)
		return session{}, false
	}
	return value, ok
}

func (a *authentication) remove(r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(cookie.Value)))
		a.mu.Unlock()
	}
}

func (a *authentication) create(username string) (string, session, error) {
	return a.createSession(username, false)
}

// createLocal 为回环本地预览建立维护员会话，不经过密码校验。
func (a *authentication) createLocal() (string, session, error) {
	if a.localUser == "" {
		return "", session{}, errors.New("没有可用的维护员账号")
	}
	return a.createSession(a.localUser, true)
}

func (a *authentication) createSession(username string, autoLogin bool) (string, session, error) {
	token, err := randomToken()
	if err != nil {
		return "", session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", session{}, err
	}
	now := time.Now()
	value := session{Username: username, Role: a.roles[username], CSRF: csrf, Expires: now.Add(8 * time.Hour), AutoLogin: autoLogin}
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, existing := range a.sessions {
		if !now.Before(existing.Expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 256 {
		return "", session{}, errors.New("维护会话数量已达上限")
	}
	a.sessions[sha256.Sum256([]byte(token))] = value
	return token, value, nil
}

func sameCSRF(actual, expected string) bool {
	return len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func setCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/panel/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

func validOrigin(r *http.Request, origin string) bool {
	return r.Header.Get("Origin") == origin && !strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
}

// loopbackRequest 只认直接连接的回环地址和回环 Host，不信任转发头，避免域名重绑定。
func loopbackRequest(r *http.Request) bool {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if ip := net.ParseIP(strings.Trim(remote, "[]")); ip == nil || !ip.IsLoopback() {
		return false
	}
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
