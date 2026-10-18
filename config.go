package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// 能力标注取值：auto 表示使用模型库推断结果，on/off 表示强制覆盖。
const (
	CapabilityAuto = "auto"
	CapabilityOn   = "on"
	CapabilityOff  = "off"
)

var serviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ServiceConfig 是一个服务条目：协议、端点与凭据。
type ServiceConfig struct {
	Name     string `json:"name" yaml:"name"`
	Protocol string `json:"protocol" yaml:"protocol"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
	Vendor   string `json:"vendor,omitempty" yaml:"vendor,omitempty"`
}

// ModelConfig 是一个模型选择：引用服务条目并指定模型与能力标注。
type ModelConfig struct {
	Service         string `json:"service" yaml:"service"`
	Model           string `json:"model" yaml:"model"`
	Vision          string `json:"vision" yaml:"vision"`
	ToolCall        string `json:"tool_call" yaml:"tool_call"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
}

// AgentConfig 是 agent 模式配置。
type AgentConfig struct {
	Enabled       bool     `json:"enabled" yaml:"enabled"`
	MaxIterations int      `json:"max_iterations" yaml:"max_iterations"`
	Tools         []string `json:"tools" yaml:"tools"`
}

// AnswerConfig 是答题配置，包含回答模型与可选的图片转述模型。
type AnswerConfig struct {
	Temperature   float64     `json:"temperature" yaml:"temperature"`
	SystemPrompt  string      `json:"system_prompt" yaml:"system_prompt"`
	CaptionPrompt string      `json:"caption_prompt" yaml:"caption_prompt"`
	Answer        ModelConfig `json:"answer" yaml:"answer"`
	Caption       ModelConfig `json:"caption" yaml:"caption"`
	Agent         AgentConfig `json:"agent" yaml:"agent"`
}

type serverConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type fileConfig struct {
	Server   serverConfig             `yaml:"server"`
	Services map[string]ServiceConfig `yaml:"services"`
	Answer   AnswerConfig             `yaml:"answer"`
	// Legacy 用于识别已废弃的旧配置结构。
	Legacy any `yaml:"models"`
}

// Config 读写 config.yaml 并在内存中提供快照。
type Config struct {
	mu   sync.RWMutex
	path string
	data fileConfig
}

const defaultSystemPrompt = "你是一个大学生网课答题助手。请根据题目和选项，在<answer>标签内输出正确答案。\n\n格式要求：\n单选题：<answer>A</answer>\n多选题：<answer>A#B#C</answer>\n判断题：<answer>对</answer> 或 <answer>错</answer>\n填空题：<answer>填空内容</answer>（多个空用#分隔）\n\n只输出<answer>标签，禁止任何额外文字、分析或解释。"

const defaultCaptionPrompt = "请精确识别图片中的内容。如果是数学公式，用LaTeX格式完整输出每个符号，例如：\\frac{1}{2}、\\sqrt{x}、\\int_{a}^{b}。如果是图表或文字，描述关键信息。如果图片空白或完全无法辨认，回复'空白'。只输出识别结果，不解释。"

func defaults() fileConfig {
	var c fileConfig
	c.Server.Host, c.Server.Port = "127.0.0.1", 8000
	c.Services = map[string]ServiceConfig{
		"deepseek": {Name: "DeepSeek", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.deepseek.com", Vendor: "deepseek"},
	}
	c.Answer = AnswerConfig{
		Temperature:   0.1,
		SystemPrompt:  defaultSystemPrompt,
		CaptionPrompt: defaultCaptionPrompt,
		Answer:        ModelConfig{Service: "deepseek", Model: "deepseek-v4-flash", Vision: CapabilityAuto, ToolCall: CapabilityAuto, ReasoningEffort: "max"},
		Caption:       ModelConfig{Vision: CapabilityAuto, ToolCall: CapabilityAuto},
		Agent:         AgentConfig{MaxIterations: 3},
	}
	return c
}

func loadConfig(path string) (*Config, error) {
	c := &Config{path: path, data: defaults()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, c.save()
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &c.data); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if c.data.Legacy != nil {
		return nil, errors.New("配置文件格式已更新，请删除 config.yaml 后重新配置")
	}
	if c.data.Server.Port < 1 || c.data.Server.Port > 65535 {
		return nil, fmt.Errorf("配置端口无效: %d", c.data.Server.Port)
	}
	if c.data.Services == nil {
		c.data.Services = map[string]ServiceConfig{}
	}
	c.data.Legacy = nil
	return c, nil
}

func (c *Config) save() error {
	c.data.Legacy = nil
	b, err := yaml.Marshal(c.data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.path); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// snapshot 返回配置副本，并应用环境变量覆盖。
func (c *Config) snapshot() fileConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v := c.data
	v.Legacy = nil
	v.Services = make(map[string]ServiceConfig, len(c.data.Services))
	for id, service := range c.data.Services {
		v.Services[id] = service
	}
	v.Answer.Answer = applyModelEnv("OCS_ANSWER", v.Answer.Answer, v.Services)
	v.Answer.Caption = applyModelEnv("OCS_CAPTION", v.Answer.Caption, v.Services)
	if value := os.Getenv("OCS_PORT"); value != "" {
		if port, err := strconv.Atoi(value); err == nil && port > 0 && port <= 65535 {
			v.Server.Port = port
		}
	}
	return v
}

// applyModelEnv 应用模型相关的环境变量覆盖，并同步更新对应的服务条目。
func applyModelEnv(prefix string, model ModelConfig, services map[string]ServiceConfig) ModelConfig {
	if value := os.Getenv(prefix + "_SERVICE"); value != "" {
		model.Service = value
	}
	if value := os.Getenv(prefix + "_MODEL"); value != "" {
		model.Model = value
	}
	service, ok := services[model.Service]
	if !ok {
		return model
	}
	if value := os.Getenv(prefix + "_API_KEY"); value != "" {
		service.APIKey = value
	}
	if value := os.Getenv(prefix + "_BASE_URL"); value != "" {
		service.BaseURL = value
	}
	services[model.Service] = service
	return model
}

func validateService(service ServiceConfig) error {
	valid := false
	for _, protocol := range protocols {
		if protocol == service.Protocol {
			valid = true
			break
		}
	}
	if !valid {
		return errors.New("未知协议")
	}
	if service.Protocol != ProtocolGeminiGenerate && service.Protocol != ProtocolOllamaChat && strings.TrimSpace(service.BaseURL) == "" {
		return errors.New("请填写服务地址")
	}
	if err := validateEndpoint(service.BaseURL); err != nil {
		return err
	}
	return nil
}

func validateModel(model ModelConfig, services map[string]ServiceConfig) error {
	if _, ok := services[model.Service]; !ok {
		return errors.New("请选择服务")
	}
	if strings.TrimSpace(model.Model) == "" {
		return errors.New("请填写模型名称")
	}
	for _, value := range []string{model.Vision, model.ToolCall} {
		switch value {
		case "", CapabilityAuto, CapabilityOn, CapabilityOff:
		default:
			return errors.New("能力标注无效")
		}
	}
	return nil
}

// updateService 新增或更新一个服务条目。
func (c *Config) updateService(id string, service ServiceConfig) error {
	if !serviceIDPattern.MatchString(id) {
		return errors.New("服务标识只能包含字母、数字、点、下划线和连字符")
	}
	if err := validateService(service); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.data
	if c.data.Services == nil {
		c.data.Services = map[string]ServiceConfig{}
	}
	c.data.Services[id] = service
	if err := c.save(); err != nil {
		c.data = old
		return err
	}
	return nil
}

// deleteService 删除服务条目，被回答模型或转述模型引用时拒绝删除。
func (c *Config) deleteService(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data.Services[id]; !ok {
		return errors.New("服务不存在")
	}
	if c.data.Answer.Answer.Service == id || c.data.Answer.Caption.Service == id {
		return errors.New("该服务正在被模型引用，请先更换模型")
	}
	old := c.data
	delete(c.data.Services, id)
	if err := c.save(); err != nil {
		c.data = old
		return err
	}
	return nil
}

// updateAnswer 更新回答模型与转述模型。
func (c *Config) updateAnswer(v AnswerConfig) error {
	if v.Temperature < 0 || v.Temperature > 2 {
		return errors.New("温度必须在 0 到 2 之间")
	}
	c.mu.RLock()
	services := make(map[string]ServiceConfig, len(c.data.Services))
	for id, service := range c.data.Services {
		services[id] = service
	}
	c.mu.RUnlock()
	if err := validateModel(v.Answer, services); err != nil && !(v.Answer.Service == "" && v.Answer.Model == "") {
		return fmt.Errorf("回答模型: %w", err)
	}
	if v.Answer.Service == "" && v.Answer.Model == "" {
		v.Answer = ModelConfig{Vision: CapabilityAuto, ToolCall: CapabilityAuto}
	}
	captionEmpty := v.Caption.Service == "" && v.Caption.Model == ""
	if !captionEmpty {
		if err := validateModel(v.Caption, services); err != nil {
			return fmt.Errorf("图片转述模型: %w", err)
		}
	} else {
		v.Caption = ModelConfig{Vision: CapabilityAuto, ToolCall: CapabilityAuto}
	}
	if v.Agent.MaxIterations < 1 || v.Agent.MaxIterations > 10 {
		return errors.New("Agent 步数必须在 1 到 10 之间")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.data
	c.data.Answer = v
	if err := c.save(); err != nil {
		c.data = old
		return err
	}
	return nil
}
