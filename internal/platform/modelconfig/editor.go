// Package modelconfig 由维护面板维护上游模型配置。
//
// 持久化仍是题库私密配置文件：模型元数据写入 config.models，密钥写入
// 配置文件同目录 secrets/ 下的 0600 文件。写入前用正式配置加载器校验，
// 成功后原子替换并通知运行时模型注册表；密钥内容从不返回浏览器。
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
	ErrNotFound = errors.New("模型不存在")
	ErrExists   = errors.New("模型标识已存在")
)

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(message string) error { return &ValidationError{Message: message} }

type Input struct {
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

type Editor struct {
	mu       sync.Mutex
	path     string
	dir      string
	secrets  string
	registry *models.Registry
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
	return &Editor{path: cfg.SourcePath, dir: dir, secrets: filepath.Join(dir, "secrets"), registry: registry}, nil
}

// Upsert 新增或更新模型；更新时标识不可修改，空密钥保留原密钥文件。
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
	item := config.Model{
		ID: id, DisplayName: strings.TrimSpace(input.DisplayName), Protocol: strings.TrimSpace(input.Protocol),
		Vendor: strings.TrimSpace(input.Vendor), BaseURL: strings.TrimSpace(input.BaseURL),
		Model: strings.TrimSpace(input.UpstreamModel), Vision: input.Vision, ToolCall: input.ToolCall,
		Reasoning: input.Reasoning, Context: input.Context, Disabled: input.Disabled,
		MaxOutputTokens: input.MaxOutputTokens,
	}
	if !create {
		item.APIKeyFile = items[index].APIKeyFile
	}
	if key := strings.TrimSpace(input.APIKey); key != "" {
		if len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
			return nil, invalid("模型密钥格式无效")
		}
		path, err := e.writeKey(id, key)
		if err != nil {
			return nil, err
		}
		item.APIKeyFile = path
	}
	if item.APIKeyFile == "" && item.Protocol != "ollama-chat" {
		return nil, invalid("非 Ollama 模型必须填写 API 密钥")
	}
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
	if err := e.save(items, nil, captionID); err != nil {
		return nil, err
	}
	return e.registry.Models(), nil
}

// Delete 移除模型并清理本面板管理的密钥文件；配置中的默认/转述引用同时清空。
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
	if err := e.save(items, defaultID, captionID); err != nil {
		return nil, err
	}
	if removed.APIKeyFile != "" {
		clean := filepath.Clean(removed.APIKeyFile)
		if filepath.Dir(clean) == e.secrets && strings.HasPrefix(filepath.Base(clean), "model-") {
			_ = os.Remove(clean)
		}
	}
	return e.registry.Models(), nil
}

func (e *Editor) writeKey(id, key string) (string, error) {
	if err := os.MkdirAll(e.secrets, 0o700); err != nil {
		return "", errors.New("无法创建模型密钥目录")
	}
	if err := os.Chmod(e.secrets, 0o700); err != nil {
		return "", errors.New("无法保护模型密钥目录")
	}
	path := filepath.Join(e.secrets, "model-"+id+".key")
	temporary, err := os.CreateTemp(e.secrets, ".model-key-*.tmp")
	if err != nil {
		return "", errors.New("无法写入模型密钥文件")
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", errors.New("无法保护模型密钥文件")
	}
	if _, err := temporary.WriteString(key + "\n"); err != nil {
		temporary.Close()
		return "", errors.New("无法写入模型密钥文件")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", errors.New("无法保存模型密钥文件")
	}
	if err := temporary.Close(); err != nil {
		return "", errors.New("无法保存模型密钥文件")
	}
	if err := os.Rename(name, path); err != nil {
		return "", errors.New("无法保存模型密钥文件")
	}
	return path, nil
}

func (e *Editor) save(items []config.Model, defaultID, captionID *string) error {
	raw, err := os.ReadFile(e.path)
	if err != nil {
		return errors.New("无法读取题库配置文件")
	}
	document := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &document); err != nil {
		return errors.New("题库配置文件格式无效")
	}
	encoded, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return errors.New("无法编码模型配置")
	}
	document["models"] = encoded
	if defaultID != nil {
		encoded, err := json.Marshal(*defaultID)
		if err != nil {
			return errors.New("无法编码默认模型")
		}
		document["default_model_id"] = encoded
	}
	if captionID != nil {
		encoded, err := json.Marshal(*captionID)
		if err != nil {
			return errors.New("无法编码图片转述模型")
		}
		document["caption_model_id"] = encoded
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
		return invalid("模型配置校验失败：" + err.Error())
	}
	keys, err := config.LoadModelKeys(validated.Models)
	if err != nil {
		return invalid("模型密钥校验失败：" + err.Error())
	}
	if err := os.Rename(name, e.path); err != nil {
		return errors.New("无法替换题库配置文件")
	}
	if err := e.registry.Replace(validated.Models, keys); err != nil {
		return errors.New("无法刷新运行时模型")
	}
	e.registry.SetDefaults(validated.DefaultModelID, validated.CaptionModelID)
	return nil
}
