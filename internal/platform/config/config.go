// Package config 读取 fork 题库服务配置。
//
// 上游服务地址、模型名和密钥只存在于 fork 配置中；配置文件本身不保存密钥，
// 只保存密钥文件路径。示例配置不包含任何真实模型或密钥。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

const (
	DefaultDatabaseMaxConnections = 4
	DefaultMaxRequestBytes        = 32 << 20
	DefaultMaxImages              = 8
	DefaultMaxImageBytes          = 5 << 20
	DefaultMaxImagePixels         = 16000000
	DefaultMaxOutputTokens        = 2048
	DefaultConcurrency            = 2
	DefaultTimeout                = 90 * time.Second
	DefaultCacheTTLDays           = 7
	DefaultMaxCacheTTLDays        = 30
	DefaultIdempotencyTTL         = 168 * time.Hour
)

var modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

type Limits struct {
	MaxRequestBytes int64 `json:"max_request_bytes"`
	MaxImages       int   `json:"max_images"`
	MaxImageBytes   int64 `json:"max_image_bytes"`
	MaxImagePixels  int64 `json:"max_image_pixels"`
	MaxOutputTokens int   `json:"max_output_tokens"`
	Concurrency     int   `json:"concurrency"`
	TimeoutSeconds  int   `json:"timeout_seconds"`
	// IdempotencyRetentionHours 是幂等响应载荷（答案）的保留期；幂等键、请求编号和
	// 摘要长期保留，过期结果返回结果不明，不会再次调用模型。
	IdempotencyRetentionHours int `json:"idempotency_retention_hours"`
}

type Cache struct {
	TTLDays       int  `json:"ttl_days"`
	MaxTTLDays    int  `json:"max_ttl_days"`
	SharedEnabled bool `json:"shared_enabled"`
}

type Model struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Protocol        string `json:"protocol"`
	Vendor          string `json:"vendor"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	APIKeyFile      string `json:"api_key_file"`
	Vision          bool   `json:"vision"`
	ToolCall        bool   `json:"tool_call"`
	Reasoning       bool   `json:"reasoning"`
	Context         int    `json:"context"`
	Disabled        bool   `json:"disabled"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type PanelUser struct {
	Username         string `json:"username"`
	PasswordHashFile string `json:"password_hash_file"`
	Role             string `json:"role"`
}

type Panel struct {
	Enabled bool        `json:"enabled"`
	Origin  string      `json:"origin"`
	Users   []PanelUser `json:"users"`
}

type Config struct {
	Listen                 string  `json:"listen"`
	DatabaseMaxConnections int32   `json:"database_max_connections"`
	DatabaseDSNFile        string  `json:"database_dsn_file"`
	ServiceTokenFile       string  `json:"service_token_file"`
	UpstreamCAFile         string  `json:"upstream_ca_file"`
	DefaultModelID         string  `json:"default_model_id"`
	CaptionModelID         string  `json:"caption_model_id"`
	AllowLoopbackUpstreams bool    `json:"allow_loopback_upstreams"`
	PromptVersion          string  `json:"prompt_version"`
	NormalizationVersion   string  `json:"normalization_version"`
	Limits                 Limits  `json:"limits"`
	Cache                  Cache   `json:"cache"`
	Models                 []Model `json:"models"`
	Panel                  Panel   `json:"panel"`
}

func applyDefaults(c *Config) {
	if c.DatabaseMaxConnections == 0 {
		c.DatabaseMaxConnections = DefaultDatabaseMaxConnections
	}
	if c.Limits.MaxRequestBytes <= 0 {
		c.Limits.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if c.Limits.MaxImages <= 0 {
		c.Limits.MaxImages = DefaultMaxImages
	}
	if c.Limits.MaxImageBytes <= 0 {
		c.Limits.MaxImageBytes = DefaultMaxImageBytes
	}
	if c.Limits.MaxImagePixels <= 0 {
		c.Limits.MaxImagePixels = DefaultMaxImagePixels
	}
	if c.Limits.MaxOutputTokens <= 0 {
		c.Limits.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if c.Limits.Concurrency <= 0 {
		c.Limits.Concurrency = DefaultConcurrency
	}
	if c.Limits.TimeoutSeconds <= 0 {
		c.Limits.TimeoutSeconds = int(DefaultTimeout / time.Second)
	}
	if c.Limits.IdempotencyRetentionHours <= 0 {
		c.Limits.IdempotencyRetentionHours = int(DefaultIdempotencyTTL / time.Hour)
	}
	if c.Cache.TTLDays <= 0 {
		c.Cache.TTLDays = DefaultCacheTTLDays
	}
	if c.Cache.MaxTTLDays <= 0 {
		c.Cache.MaxTTLDays = DefaultMaxCacheTTLDays
	}
	if c.PromptVersion == "" {
		c.PromptVersion = "fork-p1"
	}
	if c.NormalizationVersion == "" {
		c.NormalizationVersion = "fork-n1"
	}
	for i := range c.Models {
		if c.Models[i].Protocol == "" {
			c.Models[i].Protocol = engine.ProtocolOpenAIChat
		}
	}
}

// Load 读取 JSON 配置并完成结构与安全约束校验。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("无法读取配置文件")
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return nil, errors.New("配置文件 JSON 无效或包含未知字段")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("配置文件路径无效")
	}
	directory := filepath.Dir(absolute)
	for _, field := range []*string{&c.DatabaseDSNFile, &c.ServiceTokenFile, &c.UpstreamCAFile} {
		if *field != "" && !filepath.IsAbs(*field) {
			*field = filepath.Join(directory, *field)
		}
	}
	for index := range c.Models {
		if value := c.Models[index].APIKeyFile; value != "" && !filepath.IsAbs(value) {
			c.Models[index].APIKeyFile = filepath.Join(directory, value)
		}
	}
	for index := range c.Panel.Users {
		if value := c.Panel.Users[index].PasswordHashFile; value != "" && !filepath.IsAbs(value) {
			c.Panel.Users[index].PasswordHashFile = filepath.Join(directory, value)
		}
	}
	applyDefaults(&c)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		return errors.New("listen 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen 必须是 host:port")
	}
	if c.DatabaseMaxConnections < 0 || c.DatabaseMaxConnections > 32 {
		return errors.New("database_max_connections 必须在1到32之间；省略时使用4")
	}
	if c.DatabaseDSNFile == "" {
		return errors.New("database_dsn_file 不能为空")
	}
	if c.ServiceTokenFile == "" {
		return errors.New("service_token_file 不能为空")
	}
	if c.Limits.MaxRequestBytes < 1024 || c.Limits.MaxRequestBytes > 32<<20 {
		return errors.New("max_request_bytes 必须在 1 KiB 到 32 MiB 之间")
	}
	if c.Limits.MaxImages < 1 || c.Limits.MaxImages > 32 {
		return errors.New("max_images 必须在 1 到 32 之间")
	}
	if c.Limits.MaxImageBytes < 1 || c.Limits.MaxImageBytes > 20<<20 {
		return errors.New("max_image_bytes 必须在 1 B 到 20 MiB 之间")
	}
	if c.Limits.MaxImagePixels < 1 || c.Limits.MaxImagePixels > 16000000 {
		return errors.New("max_image_pixels 必须在 1 到 16000000 之间")
	}
	if c.Limits.MaxOutputTokens < 1 || c.Limits.MaxOutputTokens > 32768 {
		return errors.New("max_output_tokens 必须在 1 到 32768 之间")
	}
	if c.Limits.Concurrency < 1 || c.Limits.Concurrency > 16 {
		return errors.New("concurrency 必须在 1 到 16 之间")
	}
	if c.Limits.TimeoutSeconds < 5 || c.Limits.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds 必须在 5 到 300 之间")
	}
	if c.Cache.TTLDays < 1 || c.Cache.TTLDays > c.Cache.MaxTTLDays || c.Cache.MaxTTLDays > 365 {
		return errors.New("cache.ttl_days 必须不高于 cache.max_ttl_days，且上限不超过 365 天")
	}
	if c.Panel.Enabled {
		origin, err := url.Parse(c.Panel.Origin)
		if err != nil || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
			return errors.New("panel.origin 必须是无路径的完整来源地址")
		}
		if origin.Scheme == "http" {
			ip := net.ParseIP(origin.Hostname())
			if origin.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return errors.New("维护面板的非回环入口必须使用 HTTPS")
			}
		}
		if len(c.Panel.Users) < 1 || len(c.Panel.Users) > 16 {
			return errors.New("维护面板需要配置1到16个独立账号")
		}
		seenUsers := make(map[string]bool)
		maintainers := 0
		for _, user := range c.Panel.Users {
			if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`).MatchString(user.Username) || seenUsers[user.Username] || user.PasswordHashFile == "" {
				return errors.New("维护账号名称重复、无效或缺少密码哈希文件")
			}
			seenUsers[user.Username] = true
			if user.Role != "maintainer" && user.Role != "viewer" {
				return errors.New("维护账号角色必须为 maintainer 或 viewer")
			}
			if user.Role == "maintainer" {
				maintainers++
			}
		}
		if maintainers == 0 {
			return errors.New("维护面板至少需要一个维护员账号")
		}
	}
	seen := make(map[string]bool, len(c.Models))
	for i := range c.Models {
		m := &c.Models[i]
		if !modelIDPattern.MatchString(m.ID) {
			return fmt.Errorf("模型条目 %d 的 id 无效", i+1)
		}
		if seen[m.ID] {
			return fmt.Errorf("模型条目 id %q 重复", m.ID)
		}
		seen[m.ID] = true
		if strings.TrimSpace(m.DisplayName) == "" {
			return fmt.Errorf("模型条目 %q 缺少 display_name", m.ID)
		}
		if m.Model == "" {
			return fmt.Errorf("模型条目 %q 缺少 model", m.ID)
		}
		validProtocol := false
		for _, protocol := range engine.Protocols {
			if m.Protocol == protocol {
				validProtocol = true
				break
			}
		}
		if !validProtocol {
			return fmt.Errorf("模型条目 %q 的 protocol 无效", m.ID)
		}
		if err := validateBaseURL(m.BaseURL, c.AllowLoopbackUpstreams); err != nil {
			return fmt.Errorf("模型条目 %q: %w", m.ID, err)
		}
		if m.APIKeyFile == "" && m.Protocol != engine.ProtocolOllamaChat {
			return fmt.Errorf("模型条目 %q 缺少 api_key_file", m.ID)
		}
		if m.Context < 0 || m.Context > 10_000_000 {
			return fmt.Errorf("模型条目 %q 的 context 无效", m.ID)
		}
		if m.MaxOutputTokens < 0 || m.MaxOutputTokens > 32768 {
			return fmt.Errorf("模型条目 %q 的 max_output_tokens 无效", m.ID)
		}
	}
	if c.DefaultModelID != "" && !seen[c.DefaultModelID] {
		return errors.New("default_model_id 未在 models 中定义")
	}
	if c.CaptionModelID != "" && !seen[c.CaptionModelID] {
		return errors.New("caption_model_id 未在 models 中定义")
	}
	return nil
}

func validateBaseURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return errors.New("base_url 无效")
	}
	if u.Scheme != "https" {
		if !(allowLoopback && u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
			return errors.New("base_url 必须使用 https；本地测试可显式开启 allow_loopback_upstreams")
		}
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return errors.New("base_url 不能包含查询参数或片段")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReadSecretFile 读取密钥或服务令牌文件，限制长度并去除首尾空白。
func ReadSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("无法读取密钥文件")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("密钥文件为空")
	}
	if len(value) > 4096 {
		return "", errors.New("密钥文件内容过长")
	}
	return value, nil
}

// ReadDatabaseDSN 读取数据库连接串；不记录其内容。
func ReadDatabaseDSN(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("无法读取数据库连接文件")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("数据库连接文件为空")
	}
	return value, nil
}

func (c *Config) Timeout() time.Duration {
	return time.Duration(c.Limits.TimeoutSeconds) * time.Second
}

func (c *Config) IdempotencyRetention() time.Duration {
	return time.Duration(c.Limits.IdempotencyRetentionHours) * time.Hour
}

func (c *Config) Model(id string) *Model {
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	return nil
}

func (c *Config) DefaultModel() *Model {
	if c.DefaultModelID == "" {
		return nil
	}
	return c.Model(c.DefaultModelID)
}

func (m *Model) EffectiveMaxOutputTokens(serverCap int) int {
	if m.MaxOutputTokens > 0 && m.MaxOutputTokens < serverCap {
		return m.MaxOutputTokens
	}
	return serverCap
}

// Capabilities 返回向主平台公开的能力名称。置信度不属于能力字段。
func (m *Model) Capabilities() []string {
	result := []string{"text"}
	if m.Vision {
		result = append(result, "vision")
	}
	if m.ToolCall {
		result = append(result, "tool_call")
	}
	return result
}

// Profile 返回共享核心使用的模型能力画像。
func (m *Model) Profile() engine.Capability {
	return engine.Capability{
		Vision: m.Vision, ToolCall: m.ToolCall, Reasoning: m.Reasoning, Context: m.Context,
		Matched: true, Method: "config", Entry: m.ID,
	}
}
