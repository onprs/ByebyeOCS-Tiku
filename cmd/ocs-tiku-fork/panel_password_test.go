package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPanelPasswordCreatesPrivateHashWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.password")
	output := filepath.Join(directory, "panel.hash")
	password := []byte("Maintenance-Fixture-2026!")
	if err := os.WriteFile(input, password, 0600); err != nil {
		t.Fatal(err)
	}
	if err := panelPassword([]string{"-out", output, "-password-file", input}); err != nil {
		t.Fatal(err)
	}
	hash, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(string(hash[:len(hash)-1])), password); err != nil {
		t.Fatal("密码哈希与输入不符")
	}
	if err := panelPassword([]string{"-out", output, "-password-file", input}); err == nil {
		t.Fatal("既有密码哈希被覆盖")
	}
	unchanged, err := os.ReadFile(output)
	if err != nil || string(unchanged) != string(hash) {
		t.Fatal("失败的更新改变了既有哈希")
	}
}
