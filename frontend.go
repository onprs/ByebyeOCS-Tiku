//go:build !ocs_frontend

package main

import (
	"fmt"
	"io/fs"
	"os"
)

// loadFrontend 在默认构建中读取源码目录的构建产物。
// 单文件发布构建使用 ocs_frontend 标签嵌入同一目录。
func loadFrontend(dir string) (fs.FS, error) {
	if dir == "" {
		dir = "frontend/dist"
	}
	assets := os.DirFS(dir)
	index, err := fs.Stat(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("无法读取网页首页；请先构建 frontend/dist，或使用 -frontend-dir 指定目录")
	}
	if !index.Mode().IsRegular() {
		return nil, fmt.Errorf("网页首页必须是普通文件")
	}
	return assets, nil
}
