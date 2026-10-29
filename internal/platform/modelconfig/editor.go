// Package modelconfig 维护题库模型、提供商及私密密钥文件。
package modelconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
)

var modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

var (
	ErrNotFound = errors.New("配置不存在")
	ErrExists   = errors.New("标识已存在")
	ErrInUse    = errors.New("提供商仍被模型引用")
)

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func invalid(message string) error       { return &ValidationError{Message: message} }

type Input struct {
	ProviderID      string
	DisplayName     string
	Protocol        string
	Vendor          string
	BaseURL         string
	UpstreamModel   string
	APIKey          string
	Vision          bool
	ToolCall        bool
	Reasoning       bool
	Context         int
	Disabled        bool
	MaxOutputTokens int
	Caption         bool
}

type ProviderInput struct {
	Name     string
	Protocol string
	Vendor   string
	BaseURL  string
	APIKey   string
}

type Editor struct {
	mu        sync.Mutex
	path      string
	dir       string
	secrets   string
	registry  *models.Registry
	providers []config.Provider
}

func NewEditor(cfg *config.Config, registry *models.Registry) (*Editor, error) {
	if cfg == nil || registry == nil || strings.TrimSpace(cfg.SourcePath) == "" {
		return nil, errors.New("题库配置文件路径不可用")
	}
	info, err := os.Lstat(cfg.SourcePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("题库配置文件必须是可写的普通文件")
	}
	dir := filepath.Dir(cfg.SourcePath)
	return &Editor{path: cfg.SourcePath, dir: dir, secrets: filepath.Join(dir, "secrets"), registry: registry,
		providers: append([]config.Provider(nil), cfg.Providers...)}, nil
}

// Providers 返回不包含密钥内容的配置快照；调用方不得把密钥文件路径发送给浏览器。
func (e *Editor) Providers() []config.Provider {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]config.Provider(nil), e.providers...)
}

func providerIndex(items []config.Provider, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

// Upsert 新增或更新模型；旧版内嵌连接仍可使用，引用提供商时连接由提供商管理。
func (e *Editor) Upsert(create bool, id string, input Input) ([]config.Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id = strings.TrimSpace(id)
	if !modelIDPattern.MatchString(id) {
		return nil, invalid("模型标识只能包含小写字母、数字、下划线和连字符，长度为2到64")
	}
	items := e.registry.Models()
	index := -1
	for i := range items {
		if items[i].ID == id {
			index = i
			break
		}
	}
	if create && index >= 0 {
		return nil, ErrExists
	}
	if !create && index < 0 {
		return nil, ErrNotFound
	}
	item := config.Model{ID: id, ProviderID: strings.TrimSpace(input.ProviderID), DisplayName: strings.TrimSpace(input.DisplayName),
		Protocol: strings.TrimSpace(input.Protocol), Vendor: strings.TrimSpace(input.Vendor), BaseURL: strings.TrimSpace(input.BaseURL),
		Model: strings.TrimSpace(input.UpstreamModel), Vision: input.Vision, ToolCall: input.ToolCall,
		Reasoning: input.Reasoning, Context: input.Context, Disabled: input.Disabled, MaxOutputTokens: input.MaxOutputTokens}
	oldKey := ""
	if !create && items[index].ProviderID == "" {
		oldKey = items[index].APIKeyFile
	}
	newKey := ""
	if item.ProviderID != "" {
		if providerIndex(e.providers, item.ProviderID) < 0 {
			return nil, invalid("请选择已配置的提供商")
		}
		if strings.TrimSpace(input.APIKey) != "" {
			return nil, invalid("请在提供商中维护密钥")
		}
		item.Protocol, item.Vendor, item.BaseURL, item.APIKeyFile = "", "", "", ""
	} else {
		if !create && items[index].ProviderID == "" && input.APIKey == "" &&
			item.Protocol == items[index].Protocol && item.BaseURL == items[index].BaseURL {
			item.APIKeyFile = oldKey
		}
		if strings.TrimSpace(input.APIKey) != "" {
			var err error
			newKey, err = e.writeKey("model-", input.APIKey)
			if err != nil {
				return nil, err
			}
			item.APIKeyFile = newKey
		}
		if item.APIKeyFile == "" && item.Protocol != "ollama-chat" {
			return nil, invalid("请填写 API 密钥；更换地址或协议时需要重新填写")
		}
	}
	committed := false
	defer func() {
		if !committed && newKey != "" {
			_ = os.Remove(newKey)
		}
	}()
	if create {
		items = append(items, item)
	} else {
		items[index] = item
	}
	var captionID *string
	switch {
	case input.Caption:
		captionID = &id
	case e.registry.CaptionID() == id:
		empty := ""
		captionID = &empty
	}
	if err := e.save(items, e.providers, nil, captionID); err != nil {
		return nil, err
	}
	committed = true
	e.removeUnusedKey(oldKey)
	return e.registry.Models(), nil
}

func (e *Editor) Delete(id string) ([]config.Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	items := e.registry.Models()
	index := -1
	for i := range items {
		if items[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, ErrNotFound
	}
	removed := items[index]
	items = append(items[:index], items[index+1:]...)
	var defaultID, captionID *string
	if e.registry.DefaultID() == id {
		empty := ""
		defaultID = &empty
	}
	if e.registry.CaptionID() == id {
		empty := ""
		captionID = &empty
	}
	if err := e.save(items, e.providers, defaultID, captionID); err != nil {
		return nil, err
	}
	if removed.ProviderID == "" {
		e.removeUnusedKey(removed.APIKeyFile)
	}
	return e.registry.Models(), nil
}

// UpsertProvider 切换连接或密钥时先验证新配置，不覆盖原有密钥文件。
func (e *Editor) UpsertProvider(create bool, id string, input ProviderInput) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	id = strings.TrimSpace(id)
	if !modelIDPattern.MatchString(id) {
		return invalid("提供商标识只能包含小写字母、数字、下划线和连字符，长度为2到64")
	}
	items := append([]config.Provider(nil), e.providers...)
	index := providerIndex(items, id)
	if create && index >= 0 {
		return ErrExists
	}
	if !create && index < 0 {
		return ErrNotFound
	}
	provider := config.Provider{ID: id, Name: strings.TrimSpace(input.Name), Protocol: strings.TrimSpace(input.Protocol),
		Vendor: strings.TrimSpace(input.Vendor), BaseURL: strings.TrimSpace(input.BaseURL)}
	oldKey := ""
	if !create {
		oldKey = items[index].APIKeyFile
		if input.APIKey == "" && provider.Protocol == items[index].Protocol && provider.BaseURL == items[index].BaseURL {
			provider.APIKeyFile = oldKey
		}
	}
	newKey := ""
	if strings.TrimSpace(input.APIKey) != "" {
		var err error
		newKey, err = e.writeKey("provider-", input.APIKey)
		if err != nil {
			return err
		}
		provider.APIKeyFile = newKey
	}
	committed := false
	defer func() {
		if !committed && newKey != "" {
			_ = os.Remove(newKey)
		}
	}()
	if provider.APIKeyFile == "" && provider.Protocol != "ollama-chat" {
		return invalid("请填写 API 密钥；更换地址或协议时需要重新填写")
	}
	if create {
		items = append(items, provider)
	} else {
		items[index] = provider
	}
	if err := e.save(e.registry.Models(), items, nil, nil); err != nil {
		return err
	}
	committed = true
	e.removeUnusedKey(oldKey)
	return nil
}

func (e *Editor) DeleteProvider(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	index := providerIndex(e.providers, id)
	if index < 0 {
		return ErrNotFound
	}
	for _, item := range e.registry.Models() {
		if item.ProviderID == id {
			return ErrInUse
		}
	}
	items := append([]config.Provider(nil), e.providers...)
	removed := items[index]
	items = append(items[:index], items[index+1:]...)
	if err := e.save(e.registry.Models(), items, nil, nil); err != nil {
		return err
	}
	e.removeUnusedKey(removed.APIKeyFile)
	return nil
}

func (e *Editor) writeKey(prefix, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return "", invalid("模型密钥格式无效")
	}
	if err := os.MkdirAll(e.secrets, 0o700); err != nil {
		return "", errors.New("无法创建模型密钥目录")
	}
	info, err := os.Lstat(e.secrets)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("模型密钥目录无效")
	}
	if err := os.Chmod(e.secrets, 0o700); err != nil {
		return "", errors.New("无法保护模型密钥目录")
	}
	file, err := os.CreateTemp(e.secrets, prefix+"*.key")
	if err != nil {
		return "", errors.New("无法写入模型密钥文件")
	}
	name := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", errors.New("无法保护模型密钥文件")
	}
	if _, err := file.WriteString(key + "\n"); err != nil {
		return "", errors.New("无法写入模型密钥文件")
	}
	if err := file.Sync(); err != nil {
		return "", errors.New("无法保存模型密钥文件")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("无法保存模型密钥文件")
	}
	ok = true
	return name, nil
}

// removeUnusedKey 只清理面板托管且已不再引用的文件。
func (e *Editor) removeUnusedKey(path string) {
	if path == "" {
		return
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if filepath.Dir(clean) != e.secrets || (!strings.HasPrefix(base, "model-") && !strings.HasPrefix(base, "provider-")) {
		return
	}
	for _, provider := range e.providers {
		if provider.APIKeyFile == clean {
			return
		}
	}
	for _, model := range e.registry.Models() {
		if model.APIKeyFile == clean {
			return
		}
	}
	_ = os.Remove(clean)
}

func (e *Editor) save(items []config.Model, providers []config.Provider, defaultID, captionID *string) error {
	raw, err := os.ReadFile(e.path)
	if err != nil {
		return errors.New("无法读取题库配置文件")
	}
	document := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &document); err != nil {
		return errors.New("题库配置文件格式无效")
	}
	stored := append([]config.Model(nil), items...)
	for i := range stored {
		if stored[i].ProviderID != "" {
			stored[i].Protocol, stored[i].Vendor, stored[i].BaseURL, stored[i].APIKeyFile = "", "", "", ""
		}
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return errors.New("无法编码模型配置")
	}
	document["models"] = encoded
	encoded, err = json.Marshal(providers)
	if err != nil {
		return errors.New("无法编码提供商配置")
	}
	document["providers"] = encoded
	if defaultID != nil {
		document["default_model_id"], _ = json.Marshal(*defaultID)
	}
	if captionID != nil {
		document["caption_model_id"], _ = json.Marshal(*captionID)
	}
	output, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return errors.New("无法编码题库配置")
	}
	output = append(output, '\n')
	temporary, err := os.CreateTemp(e.dir, ".ocs-tiku-config-*.tmp")
	if err != nil {
		return errors.New("无法写入题库配置文件")
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("无法保护题库配置文件")
	}
	if _, err := temporary.Write(output); err != nil {
		temporary.Close()
		return errors.New("无法写入题库配置文件")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("无法保存题库配置文件")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("无法保存题库配置文件")
	}
	validated, err := config.Load(name)
	if err != nil {
		return invalid("配置校验失败：" + err.Error())
	}
	for _, provider := range validated.Providers {
		if provider.APIKeyFile != "" {
			if _, err := config.ReadSecretFile(provider.APIKeyFile); err != nil {
				return invalid("提供商密钥不可用")
			}
		}
	}
	keys, err := config.LoadModelKeys(validated.Models)
	if err != nil {
		return invalid("模型密钥校验失败：" + err.Error())
	}
	if err := os.Rename(name, e.path); err != nil {
		return errors.New("无法替换题库配置文件")
	}
	if err := e.registry.ReplaceWithDefaults(validated.Models, keys, validated.DefaultModelID, validated.CaptionModelID); err != nil {
		return errors.New("无法刷新运行时模型")
	}
	e.providers = append([]config.Provider(nil), validated.Providers...)
	return nil
}
