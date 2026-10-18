package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestServiceSaveFailurePreservesConfiguration(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			cfg, err := loadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			service := ServiceConfig{Name: "备用服务", Protocol: ProtocolOpenAIChat, BaseURL: "https://models.example.com/v1"}
			if err := cfg.createService("standby", service); err != nil {
				t.Fatal(err)
			}
			before := cfg.snapshot()
			fileBefore, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			// 使用目录作为目标，使原子替换失败，模拟配置无法写入。
			cfg.path = dir
			switch operation {
			case "create":
				err = cfg.createService("additional", service)
			case "update":
				service.Name = "未保存的名称"
				err = cfg.updateService("standby", service)
			case "delete":
				err = cfg.deleteService("standby")
			}
			if err == nil {
				t.Fatal("配置保存应失败")
			}
			if !reflect.DeepEqual(before, cfg.snapshot()) {
				t.Fatal("保存失败改变了内存配置")
			}
			fileAfter, err := os.ReadFile(configPath)
			if err != nil || string(fileAfter) != string(fileBefore) {
				t.Fatalf("保存失败改变了原配置文件: %v", err)
			}
		})
	}
}

func TestConcurrentServiceCreationDoesNotOverwrite(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			err := cfg.createService("shared", ServiceConfig{Name: "新增服务", Protocol: ProtocolOpenAIChat, BaseURL: "https://models.example.com/v1"})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, errServiceExists) {
				t.Errorf("新增失败原因异常: %v", err)
			}
		})
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("同一服务标识只能新增一次，实际成功 %d 次", successes.Load())
	}
}

func TestServiceTemplatesCanBeSaved(t *testing.T) {
	for _, template := range serviceTemplates {
		t.Run(template.ID, func(t *testing.T) {
			service := ServiceConfig{Name: template.Name, Protocol: template.Protocol, BaseURL: template.BaseURL, Vendor: template.Vendor}
			if err := validateService(service); err != nil {
				t.Fatalf("预置服务无法保存: %v", err)
			}
		})
	}
}
