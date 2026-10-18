package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SourceURL 是模型库的在线数据源。
const SourceURL = "https://models.dev/catalog.json"

// Status 描述模型库当前的来源与更新时间。
type Status struct {
	Origin    string `json:"origin"`    // builtin | cache
	Generated string `json:"generated"` // 数据生成时间
	Checked   string `json:"checked"`   // 最近一次在线检查时间
	Vendors   int    `json:"vendors"`
	Entries   int    `json:"entries"`
}

type storeMeta struct {
	ETag    string `json:"etag"`
	Checked string `json:"checked"`
}

// Store 管理模型库快照的本地缓存与在线更新。
type Store struct {
	snapshotPath string
	metaPath     string
	source       string
	client       *http.Client

	updateMu sync.Mutex

	mu      sync.RWMutex
	current *Catalog
	meta    storeMeta
	origin  string
}

// Open 加载数据目录中的缓存快照，缓存缺失或损坏时回落到内置快照。
// 该函数不发起网络请求。
func Open(dir string) (*Store, error) {
	store := &Store{
		snapshotPath: filepath.Join(dir, "models.json.gz"),
		metaPath:     filepath.Join(dir, "models.meta.json"),
		source:       SourceURL,
		client:       &http.Client{Timeout: 180 * time.Second},
	}
	if data, err := os.ReadFile(store.snapshotPath); err == nil {
		if current, err := Load(data); err == nil {
			store.current = current
			store.origin = "cache"
			_ = json.Unmarshal(readFile(store.metaPath), &store.meta)
			return store, nil
		}
	}
	current, err := Builtin()
	if err != nil {
		return nil, err
	}
	store.current = current
	store.origin = "builtin"
	return store, nil
}

// Catalog 返回当前模型库索引。
func (s *Store) Catalog() *Catalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Status 返回模型库状态。
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vendors, entries := s.current.Stats()
	return Status{
		Origin: s.origin, Generated: s.current.Generated(),
		Checked: s.meta.Checked, Vendors: vendors, Entries: entries,
	}
}

// Update 检查并拉取模型库更新，返回是否有变化。服务端返回 304 时视为无变化。
func (s *Store) Update(ctx context.Context) (bool, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	s.mu.RLock()
	etag := s.meta.ETag
	s.mu.RUnlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.source, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/json")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return false, fmt.Errorf("模型库更新失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		s.markChecked(etag)
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("模型库更新失败: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return false, fmt.Errorf("模型库下载失败: %w", err)
	}
	snapshot, err := Build(raw, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	data, err := encodeSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	current, err := Load(data)
	if err != nil {
		return false, err
	}
	if err = writeFileAtomic(s.snapshotPath, data, 0600); err != nil {
		return false, fmt.Errorf("模型库缓存写入失败: %w", err)
	}
	s.mu.Lock()
	s.current = current
	s.origin = "cache"
	s.meta = storeMeta{ETag: response.Header.Get("ETag"), Checked: time.Now().UTC().Format(time.RFC3339)}
	meta := s.meta
	s.mu.Unlock()
	s.saveMeta(meta)
	return true, nil
}

func (s *Store) markChecked(etag string) {
	s.mu.Lock()
	s.meta.ETag = etag
	s.meta.Checked = time.Now().UTC().Format(time.RFC3339)
	meta := s.meta
	s.mu.Unlock()
	s.saveMeta(meta)
}

// encodeSnapshot 把快照序列化为 gzip 压缩的 JSON。
func encodeSnapshot(snapshot *Snapshot) ([]byte, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err = writer.Write(data); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func (s *Store) saveMeta(meta storeMeta) {
	if data, err := json.Marshal(meta); err == nil {
		_ = writeFileAtomic(s.metaPath, data, 0600)
	}
}

func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Chmod(mode); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return nil
}
