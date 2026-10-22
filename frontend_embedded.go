//go:build ocs_frontend

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
)

// embeddedFrontend 将构建后的网页资源打包进可执行文件。
//
//go:embed all:frontend/dist
var embeddedFrontend embed.FS

func loadFrontend(dir string) (fs.FS, error) {
	assets, err := fs.Sub(embeddedFrontend, "frontend/dist")
	if err != nil {
		return nil, fmt.Errorf("无法加载内置网页: %w", err)
	}
	if dir != "" {
		assets = os.DirFS(dir)
	}
	index, err := fs.Stat(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("无法读取网页首页: %w", err)
	}
	if !index.Mode().IsRegular() {
		return nil, fmt.Errorf("网页首页必须是普通文件")
	}
	return assets, nil
}
