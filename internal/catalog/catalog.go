// Package catalog 维护模型能力元数据索引。
//
// 数据来源为 models.dev（MIT 许可）的 catalog.json，构建时转换为精简快照内置于二进制；
// 运行时优先加载数据目录中的缓存快照，并可在线更新。匹配结果仅作为默认值，
// 用户在设置中标注的能力优先。
package catalog

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed data/snapshot.json.gz
var builtin []byte

// 权威厂商顺序：同名模型在多个厂商下出现时，按此顺序优先选取。
var authority = []string{
	"openai", "anthropic", "google", "deepseek", "xai", "mistral", "cohere",
	"alibaba", "moonshotai", "zhipuai", "minimax", "volcengine", "stepfun-ai",
	"siliconflow", "sensenova", "tencent", "tencent-tokenhub", "xiaomi",
	"inclusionai", "longcat", "modelscope", "openrouter",
}

var (
	dateSuffix = regexp.MustCompile(`[-_]?(20\d{6}|20\d{2}-\d{2}-\d{2})$`)
	separator  = strings.NewReplacer("_", "-", ":", "-", ".", "")
)

// Snapshot 是快照文件的序列化结构，由 tools/snapshot 生成。
type Snapshot struct {
	Generated string                   `json:"generated"`
	Vendors   map[string]string        `json:"vendors"`
	Models    map[string]SnapshotEntry `json:"models"`
}

// SnapshotEntry 描述单个模型条目的能力。
// Spec 标记该条目来自厂商无关层，只用于能力参考与规范标识重定向，
// 不代表厂商当前提供该模型。
type SnapshotEntry struct {
	Name      string `json:"name,omitempty"`
	Vision    bool   `json:"vision,omitempty"`
	ToolCall  bool   `json:"tool,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
	Context   int    `json:"context,omitempty"`
	Canonical string `json:"canonical,omitempty"`
	Spec      bool   `json:"spec,omitempty"`
}

// Entry 是索引中的模型条目。
type Entry struct {
	ID        string
	Vendor    string
	Model     string
	Name      string
	Vision    bool
	ToolCall  bool
	Reasoning bool
	Context   int
	Canonical string
	// Spec 为真表示条目来自厂商无关层，仅供能力参考，不在厂商候选中展示。
	Spec bool
}

// Match 是一次能力查询的结果。
type Match struct {
	Found  bool
	Method string // vendor | exact | normalized | canonical
	Entry  Entry
}

// Catalog 是模型能力索引。
type Catalog struct {
	generated string
	vendors   map[string]string
	byKey     map[string]Entry
	byModel   map[string][]string
	byNorm    map[string][]string
	byVendor  map[string][]string
}

// Load 从 gzip 压缩的快照数据构建索引。
func Load(data []byte) (*Catalog, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("快照解压失败: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("快照读取失败: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("快照解析失败: %w", err)
	}
	c := &Catalog{
		generated: snapshot.Generated,
		vendors:   snapshot.Vendors,
		byKey:     make(map[string]Entry, len(snapshot.Models)),
		byModel:   make(map[string][]string, len(snapshot.Models)),
		byNorm:    make(map[string][]string, len(snapshot.Models)),
		byVendor:  make(map[string][]string),
	}
	if c.vendors == nil {
		c.vendors = map[string]string{}
	}
	for id, item := range snapshot.Models {
		entry := Entry{
			ID: id, Name: item.Name, Vision: item.Vision, ToolCall: item.ToolCall,
			Reasoning: item.Reasoning, Context: item.Context, Canonical: item.Canonical,
			Spec: item.Spec,
		}
		vendor, model := splitID(id)
		entry.Vendor, entry.Model = vendor, model
		c.byKey[id] = entry
		c.byModel[strings.ToLower(model)] = append(c.byModel[strings.ToLower(model)], id)
		key := normalizeID(model)
		c.byNorm[key] = append(c.byNorm[key], id)
		if !entry.Spec {
			c.byVendor[vendor] = append(c.byVendor[vendor], id)
		}
	}
	return c, nil
}

var (
	builtinOnce sync.Once
	builtinData *Catalog
	builtinErr  error
)

// Builtin 返回内置快照构建的索引，只在首次调用时解析。
func Builtin() (*Catalog, error) {
	builtinOnce.Do(func() { builtinData, builtinErr = Load(builtin) })
	return builtinData, builtinErr
}

// Generated 返回快照的生成时间。
func (c *Catalog) Generated() string { return c.generated }

// Stats 返回厂商数与条目数。
func (c *Catalog) Stats() (int, int) { return len(c.vendors), len(c.byKey) }

// Vendors 返回全部厂商标识与名称。
func (c *Catalog) Vendors() map[string]string {
	result := make(map[string]string, len(c.vendors))
	for id, name := range c.vendors {
		result[id] = name
	}
	return result
}

// VendorName 返回厂商显示名，未知时回落为标识本身。
func (c *Catalog) VendorName(id string) string {
	if name := c.vendors[id]; name != "" {
		return name
	}
	return id
}

// VendorModels 返回厂商当前提供的模型条目，按名称排序。
// 不含厂商无关层的历史条目。
func (c *Catalog) VendorModels(vendor string) []Entry {
	ids := c.byVendor[vendor]
	result := make([]Entry, 0, len(ids))
	for _, id := range ids {
		result = append(result, c.byKey[id])
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// Lookup 查询模型能力。vendor 非空时仅在该厂商内匹配，为空时跨厂商匹配；
// 模型名可以带厂商前缀。
func (c *Catalog) Lookup(vendor, model string) Match {
	name := strings.TrimSpace(model)
	if name == "" || c == nil {
		return Match{}
	}
	vendor = strings.TrimSpace(vendor)
	if vendor != "" {
		if entry, ok := c.byKey[vendor+"/"+name]; ok {
			return c.finish(Match{Found: true, Method: "vendor", Entry: entry})
		}
		if entry, ok := c.byKey[name]; ok && entry.Vendor == vendor {
			return c.finish(Match{Found: true, Method: "vendor", Entry: entry})
		}
		for _, id := range c.byNorm[normalizeID(name)] {
			if entry, ok := c.byKey[id]; ok && entry.Vendor == vendor {
				return c.finish(Match{Found: true, Method: "normalized", Entry: entry})
			}
		}
		return Match{}
	}
	if entry, ok := c.byKey[name]; ok {
		return c.finish(Match{Found: true, Method: "exact", Entry: entry})
	}
	if ids := c.byModel[strings.ToLower(name)]; len(ids) > 0 {
		return c.finish(Match{Found: true, Method: "exact", Entry: c.pick(ids)})
	}
	if ids := c.byNorm[normalizeID(name)]; len(ids) > 0 {
		return c.finish(Match{Found: true, Method: "normalized", Entry: c.pick(ids)})
	}
	return Match{}
}

// finish 在命中条目带有规范模型标识时改用规范条目的能力。
func (c *Catalog) finish(m Match) Match {
	if m.Entry.Canonical != "" {
		if entry, ok := c.byKey[m.Entry.Canonical]; ok {
			m.Entry = entry
			m.Method = "canonical"
		}
	}
	return m
}

// pick 在多个候选条目中按厂商权威顺序选取。
func (c *Catalog) pick(ids []string) Entry {
	best := c.byKey[ids[0]]
	bestRank := vendorRank(best.Vendor)
	for _, id := range ids[1:] {
		entry := c.byKey[id]
		if rank := vendorRank(entry.Vendor); rank < bestRank {
			best, bestRank = entry, rank
		}
	}
	return best
}

func splitID(id string) (string, string) {
	if index := strings.Index(id, "/"); index >= 0 {
		return id[:index], id[index+1:]
	}
	return "", id
}

func normalizeID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if index := strings.LastIndex(id, "/"); index >= 0 {
		id = id[index+1:]
	}
	id = dateSuffix.ReplaceAllString(id, "")
	return separator.Replace(id)
}

func vendorRank(vendor string) int {
	for index, id := range authority {
		if id == vendor {
			return index
		}
	}
	return len(authority)
}
