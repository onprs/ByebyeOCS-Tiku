// Command snapshot 从 models.dev 生成内置的模型能力快照。
//
// 用法：
//
//	go run ./tools/snapshot                       # 从 models.dev 下载并生成快照
//	go run ./tools/snapshot -source catalog.json  # 使用本地文件生成
//
// 快照会写入 internal/catalog/data/snapshot.json.gz，随二进制一起分发。
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/onprs/OCS-Tiku/internal/catalog"
)

const sourceURL = catalog.SourceURL

func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func main() {
	source := flag.String("source", "", "本地 catalog.json 路径，留空则从 models.dev 下载")
	output := flag.String("output", "internal/catalog/data/snapshot.json.gz", "快照输出路径")
	flag.Parse()

	var raw []byte
	if *source != "" {
		data, err := os.ReadFile(*source)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取源文件失败:", err)
			os.Exit(1)
		}
		raw = data
	} else {
		fmt.Println("正在下载", sourceURL)
		data, err := download(sourceURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "下载失败:", err)
			os.Exit(1)
		}
		raw = data
	}

	snapshot, err := catalog.Build(raw, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		fmt.Fprintln(os.Stderr, "转换失败:", err)
		os.Exit(1)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "序列化失败:", err)
		os.Exit(1)
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		fmt.Fprintln(os.Stderr, "压缩失败:", err)
		os.Exit(1)
	}
	if _, err = writer.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, "压缩失败:", err)
		os.Exit(1)
	}
	if err = writer.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "压缩失败:", err)
		os.Exit(1)
	}
	if err = os.WriteFile(*output, compressed.Bytes(), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		os.Exit(1)
	}
	fmt.Printf("厂商 %d 个，条目 %d 条，未压缩 %.0f KB，压缩后 %.0f KB，写入 %s\n",
		len(snapshot.Vendors), len(snapshot.Models),
		float64(len(data))/1024, float64(compressed.Len())/1024, *output)
}
