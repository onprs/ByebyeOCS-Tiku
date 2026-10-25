// Package models 保存运行时可替换的模型目录与密钥快照。
//
// 配置文件与私密密钥文件仍是持久化来源；面板保存后通过 Registry 热替换，
// 已开始的查询继续使用调用时读取的模型快照。
package models

import (
	"errors"
	"sync"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
)

type Registry struct {
	mu        sync.RWMutex
	models    []config.Model
	keys      map[string]string
	defaultID string
	captionID string
}

func New(items []config.Model, keys map[string]string) *Registry {
	copied := make([]config.Model, len(items))
	copy(copied, items)
	keyCopy := make(map[string]string, len(keys))
	for key, value := range keys {
		keyCopy[key] = value
	}
	return &Registry{models: copied, keys: keyCopy}
}

func (r *Registry) Models() []config.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]config.Model, len(r.models))
	copy(result, r.models)
	return result
}

func (r *Registry) Model(id string) *config.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.models {
		if r.models[i].ID == id {
			item := r.models[i]
			return &item
		}
	}
	return nil
}

func (r *Registry) Key(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.keys[id]
}

func (r *Registry) SetDefaults(defaultID, captionID string) {
	r.mu.Lock()
	r.defaultID, r.captionID = defaultID, captionID
	r.mu.Unlock()
}

func (r *Registry) DefaultID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultID
}

func (r *Registry) CaptionID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.captionID
}

// Replace 校验非空标识唯一后原子替换模型与密钥快照。
func (r *Registry) Replace(items []config.Model, keys map[string]string) error {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.ID == "" || seen[item.ID] {
			return errors.New("模型标识为空或重复")
		}
		seen[item.ID] = true
	}
	copied := make([]config.Model, len(items))
	copy(copied, items)
	keyCopy := make(map[string]string, len(keys))
	for key, value := range keys {
		keyCopy[key] = value
	}
	r.mu.Lock()
	r.models, r.keys = copied, keyCopy
	r.mu.Unlock()
	return nil
}
