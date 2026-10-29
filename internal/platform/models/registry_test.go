package models

import (
	"fmt"
	"sync"
	"testing"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
)

func TestResolveForAnswerUsesOneSnapshot(t *testing.T) {
	registry := New(nil, nil)
	set := func(version int) {
		value := fmt.Sprint(version)
		items := []config.Model{{ID: "answer", Model: value}, {ID: "caption", Model: value, Vision: true}}
		if err := registry.ReplaceWithDefaults(items, map[string]string{"answer": value, "caption": value}, "answer", "caption"); err != nil {
			t.Error(err)
		}
	}
	set(0)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := 1; i <= 2000; i++ {
			set(i)
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 4000; i++ {
			model, key, caption, captionKey := registry.ResolveForAnswer("answer", true)
			if model == nil || caption == nil || model.Model != key || caption.Model != captionKey || model.Model != caption.Model {
				t.Error("回答模型、转述模型与密钥不属于同一快照")
				return
			}
		}
	}()
	workers.Wait()
}
