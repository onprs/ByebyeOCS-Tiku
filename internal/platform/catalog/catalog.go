// Package catalog 定义题库服务发布的公开模型条目。
//
// 公开条目（offering）是题库侧的唯一配置来源：公开名称、能力、价格版本、
// 缓存策略、默认与可见状态都由题库面板维护。主平台只读取这份目录，
// 不保存、也不改写价格；上游模型地址与密钥仍只存在于题库配置。
package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ValidationError 表示面板输入或公开条目自身不满足约束；服务层映射为 422。
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(message string) error { return &ValidationError{Message: message} }

// Micros 以十进制字符串在 JSON 中传输，避免跨服务浮点或 64 位整数精度丢失。
type Micros int64

func (m Micros) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatInt(int64(m), 10) + `"`), nil
}

func (m *Micros) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return invalid("金额必须是十进制字符串")
	}
	value, err := strconv.ParseInt(string(data[1:len(data)-1]), 10, 64)
	if err != nil || value < 0 {
		return invalid("金额必须是非负十进制字符串")
	}
	*m = Micros(value)
	return nil
}

// Pricing 是一个不可变的价格版本。费率单位为微元/百万 token。
type Pricing struct {
	Version                      string `json:"version"`
	InputMicrosPerMillionTokens  Micros `json:"input_micros_per_million_tokens"`
	OutputMicrosPerMillionTokens Micros `json:"output_micros_per_million_tokens"`
	MicrosPerMinor               Micros `json:"micros_per_minor"`
	Rounding                     string `json:"rounding"`
	Rule                         string `json:"rule"`
}

func (p *Pricing) Validate() error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.Version) == "" || len(p.Version) > 64 {
		return invalid("价格版本不能为空或超过 64 字")
	}
	if strings.TrimSpace(p.Rule) == "" || len(p.Rule) > 64 {
		return invalid("结算规则不能为空或超过 64 字")
	}
	if p.Rounding != "ceil" && p.Rounding != "floor" {
		return invalid("价格取整方式必须是 ceil 或 floor")
	}
	if p.MicrosPerMinor != 10000 {
		return invalid("人民币每分固定为10000微元")
	}
	if p.InputMicrosPerMillionTokens < 0 || p.OutputMicrosPerMillionTokens < 0 {
		return invalid("token 费率不能为负")
	}
	return nil
}

func (p *Pricing) Free() bool {
	return p != nil && p.InputMicrosPerMillionTokens == 0 && p.OutputMicrosPerMillionTokens == 0
}

// CachePolicy 控制精确缓存的可用范围与期限；期限不得超过题库服务上限。
type CachePolicy struct {
	Allow   bool   `json:"allow"`
	Scope   string `json:"scope"`
	TTLDays int    `json:"ttl_days"`
}

func (p CachePolicy) Validate(maxTTL int, sharedEnabled bool) error {
	if p.Scope != "user" && p.Scope != "shared" {
		return invalid("缓存范围必须是 user 或 shared")
	}
	if p.Scope == "shared" && !sharedEnabled {
		return invalid("题库服务未启用共享缓存")
	}
	if p.TTLDays < 1 || p.TTLDays > maxTTL {
		return invalid(fmt.Sprintf("缓存期限必须在 1 到 %d 天之间", maxTTL))
	}
	return nil
}

// Health 是派生状态，不写入数据库：配置中的模型被停用或移除时不对外提供。
const (
	HealthConfigured  = "configured"
	HealthUnavailable = "unavailable"
)

const (
	PublicIDPrefix  = "qm_"
	maxCapabilities = 2
)

var capabilityOrder = []string{"text", "vision"}

// Offering 是题库发布的公开模型条目。ID 由题库生成并保持稳定，
// 主平台以它作为查询记录的关联键。
type Offering struct {
	ID           string      `json:"id"`
	PublicID     string      `json:"public_id"`
	DisplayName  string      `json:"display_name"`
	Description  string      `json:"description"`
	ProviderRef  string      `json:"provider_ref"`
	Capabilities []string    `json:"capabilities"`
	Pricing      *Pricing    `json:"pricing"`
	PriceVersion string      `json:"price_version"`
	CachePolicy  CachePolicy `json:"cache_policy"`
	Enabled      bool        `json:"enabled"`
	Default      bool        `json:"is_default"`
	SortOrder    int         `json:"sort_order"`
	Health       string      `json:"health"`
	Version      int64       `json:"version"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// ModelProfile 是校验公开能力时需要的模型事实。
type ModelProfile struct {
	Vision bool
}

// Normalize 清理输入并校验公开条目自身的一致性；模型存在性由调用方确认。
func (o *Offering) Normalize(profile ModelProfile, maxTTL int, sharedEnabled bool) error {
	o.DisplayName = strings.TrimSpace(o.DisplayName)
	o.Description = strings.TrimSpace(o.Description)
	o.ProviderRef = strings.TrimSpace(o.ProviderRef)
	if utf8.RuneCountInString(o.DisplayName) < 1 || utf8.RuneCountInString(o.DisplayName) > 80 {
		return invalid("公开名称不能为空或超过 80 字")
	}
	if utf8.RuneCountInString(o.Description) > 500 {
		return invalid("说明不能超过 500 字")
	}
	if o.ProviderRef == "" || len(o.ProviderRef) > 64 {
		return invalid("模型引用无效")
	}
	seen := map[string]bool{}
	for _, value := range o.Capabilities {
		if value != "text" && value != "vision" {
			return invalid("模型能力包含未知值")
		}
		seen[value] = true
	}
	if len(seen) == 0 {
		seen["text"] = true
	}
	if len(seen) > maxCapabilities {
		return invalid("模型能力数量超出上限")
	}
	if seen["vision"] && !profile.Vision {
		return invalid("该模型未声明图片能力")
	}
	normalized := make([]string, 0, len(capabilityOrder))
	for _, wanted := range capabilityOrder {
		if seen[wanted] {
			normalized = append(normalized, wanted)
		}
	}
	o.Capabilities = normalized
	if err := o.Pricing.Validate(); err != nil {
		return err
	}
	if o.Pricing != nil {
		o.PriceVersion = o.Pricing.Version
	} else {
		o.PriceVersion = ""
	}
	if err := o.CachePolicy.Validate(maxTTL, sharedEnabled); err != nil {
		return err
	}
	if o.SortOrder < -1_000_000 || o.SortOrder > 1_000_000 {
		return invalid("排序值超出范围")
	}
	if o.Default && !o.Enabled {
		return invalid("默认模型必须同时可见")
	}
	return nil
}

// NewPublicID 生成对外公开的条目编号；与上游模型标识和价格无关。
func NewPublicID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("无法生成公开条目编号")
	}
	return PublicIDPrefix + hex.EncodeToString(value[:])
}

// ValidPublicID 校验公开条目编号格式。
func ValidPublicID(value string) bool {
	if len(value) != len(PublicIDPrefix)+32 || !strings.HasPrefix(value, PublicIDPrefix) {
		return false
	}
	for _, c := range value[len(PublicIDPrefix):] {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

// EffectiveHealth 依据配置模型是否存在/停用以及发布状态计算健康事实。
func EffectiveHealth(o Offering, modelConfigured, modelDisabled bool) string {
	if !modelConfigured || modelDisabled {
		return HealthUnavailable
	}
	return HealthConfigured
}

// EncodePricing 返回用于入库的 JSON；nil 表示未定价。
func EncodePricing(p *Pricing) ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// DecodePricing 解析已保存的价格；NULL 或空对象视为未定价。
func DecodePricing(data []byte) (*Pricing, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var p Pricing
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// EncodeCachePolicy 返回用于入库的 JSON。
func EncodeCachePolicy(p CachePolicy) ([]byte, error) {
	return json.Marshal(p)
}

// DecodeCachePolicy 解析已保存的缓存策略。
func DecodeCachePolicy(data []byte) (CachePolicy, error) {
	var p CachePolicy
	if len(data) == 0 {
		return p, invalid("缓存策略缺失")
	}
	return p, json.Unmarshal(data, &p)
}

// 存储层向面板暴露的确定性错误；面板据此映射 409/404，不转发数据库文本。
var (
	ErrVersionConflict      = errors.New("公开条目已被其他操作更新，请刷新后重试")
	ErrPriceVersionConflict = errors.New("此价格版本已有不同费率，请发布新版本")
	ErrNotFound             = errors.New("公开条目不存在")
)

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("无法生成公开条目标识")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
