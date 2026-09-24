package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const ImageCacheMaxBytes int64 = 1024 * 1024 * 1024
const imageCacheMaxFileBytes int64 = 64 * 1024 * 1024
const imageCacheTTL = 7 * 24 * time.Hour
const imageCacheCleanupInterval = time.Hour

type ImageCache struct {
	dir      string
	client   *http.Client
	group    singleflight.Group
	mu       sync.Mutex
	maxBytes int64
	stop     chan struct{}
	stopOnce sync.Once
}

func NewImageCache(dir string) (*ImageCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cache := &ImageCache{
		dir:      dir,
		client:   &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{Proxy: nil}},
		maxBytes: ImageCacheMaxBytes,
		stop:     make(chan struct{}),
	}
	cache.prune()
	go cache.cleanupLoop()
	return cache, nil
}

func (c *ImageCache) Close() { c.stopOnce.Do(func() { close(c.stop) }) }

func (c *ImageCache) cleanupLoop() {
	ticker := time.NewTicker(imageCacheCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.prune()
		case <-c.stop:
			return
		}
	}
}

func (c *ImageCache) path(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(digest[:])+".img")
}

func (c *ImageCache) Get(key string) (string, bool) {
	path := c.path(key)
	info, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > imageCacheMaxFileBytes {
		_ = os.Remove(path)
		return "", false
	}
	if time.Since(info.ModTime()) > imageCacheTTL {
		_ = os.Remove(path)
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return path, true
}

func (c *ImageCache) Fetch(ctx context.Context, key, source string) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("图片缓存只接受本地预览地址")
	}
	result, err, _ := c.group.Do(key, func() (any, error) {
		if path, ok := c.Get(key); ok {
			return path, nil
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return "", err
		}
		response, err := c.client.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("图片缓存下载失败：HTTP %d", response.StatusCode)
		}
		if response.ContentLength > imageCacheMaxFileBytes {
			return "", fmt.Errorf("图片超过单文件缓存上限")
		}
		if err := os.MkdirAll(c.dir, 0o700); err != nil {
			return "", err
		}
		temporary, err := os.CreateTemp(c.dir, ".image-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(temporary.Name())
		count, copyErr := io.Copy(temporary, io.LimitReader(response.Body, imageCacheMaxFileBytes+1))
		closeErr := temporary.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if count == 0 || count > imageCacheMaxFileBytes {
			return "", fmt.Errorf("图片大小超出缓存范围")
		}
		content, err := os.Open(temporary.Name())
		if err != nil {
			return "", err
		}
		header := make([]byte, 512)
		read, readErr := content.Read(header)
		_ = content.Close()
		if readErr != nil && readErr != io.EOF {
			return "", readErr
		}
		if contentType := http.DetectContentType(header[:read]); contentType == "text/html; charset=utf-8" || contentType == "text/plain; charset=utf-8" {
			return "", fmt.Errorf("预览地址未返回图片")
		}
		path := c.path(key)
		if err := os.Rename(temporary.Name(), path); err != nil {
			return "", err
		}
		c.prune()
		return path, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (c *ImageCache) prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type cachedFile struct {
		path string
		size int64
		used time.Time
	}
	files := make([]cachedFile, 0, len(entries))
	var total int64
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(c.dir, entry.Name())
		if filepath.Ext(entry.Name()) != ".img" {
			if len(entry.Name()) > 7 && entry.Name()[:7] == ".image-" && now.Sub(info.ModTime()) > imageCacheCleanupInterval {
				_ = os.Remove(path)
			}
			continue
		}
		if now.Sub(info.ModTime()) > imageCacheTTL {
			_ = os.Remove(path)
			continue
		}
		files = append(files, cachedFile{path: path, size: info.Size(), used: info.ModTime()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return
	}
	sort.Slice(files, func(first, second int) bool { return files[first].used.Before(files[second].used) })
	for _, file := range files {
		if total <= c.maxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}
