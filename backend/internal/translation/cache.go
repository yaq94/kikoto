package translation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/download"
)

// Cache owns only rebuildable translation files. It never opens the application
// database or media directories. The budget charges each JSON file rounded up
// to 4 KiB and caps the index at 8192 entries as an independent memory bound.
type Cache struct {
	mu        sync.Mutex
	root      string
	freeBytes func(string) (uint64, error)
}

type CacheStats struct {
	Bytes   int64  `json:"cacheBytes"`
	Entries int    `json:"cacheEntries"`
	Status  string `json:"cacheStatus"`
}

func NewCache(root string, freeBytes func(string) (uint64, error)) *Cache {
	return &Cache{root: root, freeBytes: freeBytes}
}

// CacheKey includes the original text as well as masked input: two names may
// produce identical placeholders but must never share another work's text.
func CacheKey(cfg Config, in Input, original, templateVersion string) string {
	payload, _ := json.Marshal([]any{original, in.Text, in.TargetLanguage, cfg.BaseURL, cfg.Model, cfg.DisableThinking, templateVersion, "names-v1"})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func cacheCharge(size int64) int64 { return ((size + 4095) / 4096) * 4096 }
func cacheFilename(name string) bool {
	if len(name) != 69 || !strings.HasSuffix(name, ".json") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".json"))
	return err == nil
}

func (c *Cache) files() ([]os.FileInfo, error) {
	if c.root == "" {
		return nil, os.ErrNotExist
	}
	info, err := os.Lstat(c.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrPermission
	}
	dir, err := os.Open(c.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	var files []os.FileInfo
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			// A process stop may leave the shared atomic writer's temporary file.
			// This cache mutex excludes active writers in the single-instance app.
			if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".") {
				base, _, ok := strings.Cut(strings.TrimPrefix(entry.Name(), "."), ".download-")
				if ok && cacheFilename(base) {
					if err := os.Remove(filepath.Join(c.root, entry.Name())); err != nil {
						return nil, err
					}
					continue
				}
			}
			if !cacheFilename(entry.Name()) || !entry.Type().IsRegular() {
				continue
			}
			file, err := entry.Info()
			if err != nil {
				return nil, err
			}
			files = append(files, file)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) })
	return files, nil
}

func (c *Cache) trim(limit int64, reserve int64) (CacheStats, error) {
	files, err := c.files()
	if err != nil {
		return CacheStats{Status: "unavailable"}, err
	}
	stats := CacheStats{Status: "ready", Entries: len(files)}
	for _, f := range files {
		stats.Bytes += cacheCharge(f.Size())
	}
	for _, f := range files {
		allowedEntries := 8192
		if reserve > 0 {
			allowedEntries--
		}
		if stats.Bytes+reserve <= limit && stats.Entries <= allowedEntries {
			break
		}
		if err := os.Remove(filepath.Join(c.root, f.Name())); err != nil {
			return CacheStats{Status: "unavailable"}, err
		}
		stats.Bytes -= cacheCharge(f.Size())
		stats.Entries--
	}
	return stats, nil
}

func (c *Cache) Stats(cfg Config) CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats, err := c.trim(cfg.CacheBytes, 0)
	if err != nil {
		return stats
	}
	if c.freeBytes == nil {
		stats.Status = "unavailable"
		return stats
	}
	free, err := c.freeBytes(c.root)
	if err != nil {
		stats.Status = "unavailable"
	} else if free < uint64(cfg.MinFreeBytes) {
		stats.Status = "paused_low_disk"
	}
	return stats
}

func (c *Cache) Get(key string, cfg Config) (Result, bool) {
	if !cacheFilename(key + ".json") {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.trim(cfg.CacheBytes, 0); err != nil {
		return Result{}, false
	}
	path := filepath.Join(c.root, key+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxResponseBytes {
		return Result{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, false
	}
	var result Result
	if json.Unmarshal(data, &result) != nil || result.Text == "" || result.TemplateVersion != TemplateVersion || result.Model != cfg.Model {
		return Result{}, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return result, true
}

func (c *Cache) Put(key string, cfg Config, result Result) string {
	if !cacheFilename(key + ".json") {
		return "unavailable"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaxResponseBytes {
		return "entry_too_large"
	}
	charge := cacheCharge(int64(len(data)))
	if charge > cfg.CacheBytes {
		return "entry_too_large"
	}
	if c.freeBytes == nil {
		return "unavailable"
	}
	free, err := c.freeBytes(c.root)
	if err != nil {
		return "unavailable"
	}
	if free < uint64(cfg.MinFreeBytes)+uint64(charge) {
		return "paused_low_disk"
	}
	if _, err := c.trim(cfg.CacheBytes, charge); err != nil {
		return "unavailable"
	}
	if err := os.MkdirAll(c.root, 0o700); err != nil {
		return "unavailable"
	}
	size := int64(len(data))
	if _, err := download.WriteFile(bytes.NewReader(data), size, filepath.Join(c.root, key+".json"), download.Options{MaxBytes: MaxResponseBytes, ExpectedBytes: &size}); err != nil {
		return "unavailable"
	}
	return "stored"
}
