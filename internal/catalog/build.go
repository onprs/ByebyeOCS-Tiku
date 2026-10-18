package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sourceCatalog 对应 models.dev catalog.json 的顶层结构。
type sourceCatalog struct {
	Providers map[string]sourceProvider `json:"providers"`
	Models    map[string]sourceModel    `json:"models"`
}

type sourceProvider struct {
	Name   string                 `json:"name"`
	Models map[string]sourceModel `json:"models"`
}

type sourceModel struct {
	Name       string `json:"name"`
	Reasoning  bool   `json:"reasoning"`
	ToolCall   bool   `json:"tool_call"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
	CanonicalModelID string `json:"canonical_model_id"`
}

// Build 把 models.dev 的 catalog.json 转换为精简快照，generated 为数据生成时间。
func Build(raw []byte, generated string) (*Snapshot, error) {
	var file sourceCatalog
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("模型库数据解析失败: %w", err)
	}
	if len(file.Providers) == 0 && len(file.Models) == 0 {
		return nil, fmt.Errorf("模型库数据为空")
	}
	snapshot := &Snapshot{
		Generated: generated,
		Vendors:   make(map[string]string, len(file.Providers)),
		Models:    make(map[string]SnapshotEntry, len(file.Providers)+len(file.Models)),
	}
	// 厂商无关的规范条目先写入，随后由厂商条目覆盖同名条目。
	for id, model := range file.Models {
		snapshot.Models[id] = convertModel(model)
		vendor, _ := splitID(id)
		if vendor != "" && snapshot.Vendors[vendor] == "" {
			snapshot.Vendors[vendor] = vendor
		}
	}
	for id, provider := range file.Providers {
		name := provider.Name
		if name == "" {
			name = id
		}
		snapshot.Vendors[id] = name
		for modelID, model := range provider.Models {
			snapshot.Models[id+"/"+modelID] = convertModel(model)
		}
	}
	return snapshot, nil
}

func convertModel(model sourceModel) SnapshotEntry {
	vision := false
	for _, modality := range model.Modalities.Input {
		if strings.EqualFold(modality, "image") {
			vision = true
			break
		}
	}
	return SnapshotEntry{
		Name: model.Name, Vision: vision, ToolCall: model.ToolCall,
		Reasoning: model.Reasoning, Context: model.Limit.Context,
		Canonical: model.CanonicalModelID,
	}
}
