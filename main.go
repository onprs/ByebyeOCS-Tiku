package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/onprs/OCS-Tiku/internal/catalog"
)

// refreshModels 在后台检查模型库更新，同一天内不重复检查，失败不影响服务。
func refreshModels(store *catalog.Store) {
	status := store.Status()
	if status.Checked != "" {
		if checked, err := time.Parse(time.RFC3339, status.Checked); err == nil && time.Since(checked) < 24*time.Hour {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	changed, err := store.Update(ctx)
	if err != nil {
		log.Printf("模型库更新检查失败: %v", err)
		return
	}
	if changed {
		current := store.Status()
		log.Printf("模型库已更新: %d 个厂商，%d 条模型", current.Vendors, current.Entries)
	}
}

func main() {
	dataDir := flag.String("data-dir", ".", "配置、数据库、图片和模型库缓存目录")
	frontendDir := flag.String("frontend-dir", "frontend/dist", "前端构建目录")
	listen := flag.String("listen", "", "监听地址（覆盖配置文件）")
	flag.Parse()

	config, err := loadConfig(filepath.Join(*dataDir, "config.yaml"))
	if err != nil {
		log.Fatal(err)
	}
	cfg := config.snapshot()
	address := *listen
	if address == "" {
		address = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		log.Fatalf("监听地址无效: %v", err)
	}
	if _, err := os.Stat(filepath.Join(*frontendDir, "index.html")); err != nil {
		log.Fatalf("前端未构建: 请先在 frontend 目录运行 npm ci 和 npm run build: %v", err)
	}
	store, err := openStore(filepath.Join(*dataDir, "ocs-tiku.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.close()
	images, err := newImageProcessor(filepath.Join(*dataDir, "images"))
	if err != nil {
		log.Fatal(err)
	}
	modelCatalog, err := catalog.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	models := newModelClient()
	server := &Server{config: config, store: store, models: models, catalog: modelCatalog,
		solver: &Solver{config: config, models: models, images: images, catalog: modelCatalog, store: store},
		assets: *frontendDir, imageDir: images.dir, address: address}
	go refreshModels(modelCatalog)
	log.Printf("OCS 题库服务: http://%s", address)
	log.Fatal(http.ListenAndServe(address, server.handler()))
}
