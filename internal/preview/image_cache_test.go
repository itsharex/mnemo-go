package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestImageCachePersistsAndReusesFile(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nimage-data"))
	}))
	defer server.Close()

	dir := filepath.Join(t.TempDir(), "data", "cache", "images")
	cache, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	first, err := cache.Fetch(context.Background(), "account/file", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := reopened.Fetch(context.Background(), "account/file", server.URL)
	if err != nil || second != first || requests.Load() != 1 {
		t.Fatalf("缓存未复用: first=%q second=%q requests=%d err=%v", first, second, requests.Load(), err)
	}
	if _, err := reopened.Fetch(context.Background(), "other", "https://example.com/image.png"); err == nil {
		t.Fatal("非本地预览地址不应进入缓存")
	}
}

func TestImageCacheExpiresAndPrunesOldest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nimage-data"))
	}))
	defer server.Close()

	dir := filepath.Join(t.TempDir(), "images")
	cache, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.maxBytes = 24
	first, err := cache.Fetch(context.Background(), "first", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Fetch(context.Background(), "second", server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("超过容量后应清理最久未使用图片，stat=%v", err)
	}
	second, ok := cache.Get("second")
	if !ok {
		t.Fatal("最新图片不应被清理")
	}
	expired := time.Now().Add(-imageCacheTTL - time.Hour)
	if err := os.Chtimes(second, expired, expired); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get("second"); ok {
		t.Fatal("过期图片仍被命中")
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("过期图片未自动删除，stat=%v", err)
	}
	third, err := cache.Fetch(context.Background(), "third", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(third, expired, expired); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(third); !os.IsNotExist(err) {
		t.Fatalf("启动时应清理过期图片，stat=%v", err)
	}
}
