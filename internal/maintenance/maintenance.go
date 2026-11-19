package maintenance

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gravitybox-mygo/internal/config"
)

// SafeCacheDirs matches GravityBox's lossless runtime cache catalog.
var SafeCacheDirs = []string{
	"Cache",
	"Code Cache",
	"GPUCache",
	"DawnCache",
	"DawnWebGPUCache",
	"DawnGraphiteCache",
	"GraphiteDawnCache",
	"ShaderCache",
	"CachedData",
	"CachedExtensionVSIXs",
	"CachedProfilesData",
	"Crashpad",
	"logs",
	"blob_storage",
}

// CacheItem describes a scanned cache directory and whether it is selected for cleanup.
type CacheItem struct {
	Name     string
	Path     string
	Bytes    int64
	Selected bool
	Exists   bool
}

// ScanSafeCaches inspects Antigravity's UserData directory for safe lossless caches.
func ScanSafeCaches() ([]CacheItem, int64) {
	userData := config.AntigravityUserDataDir()
	items := make([]CacheItem, 0, len(SafeCacheDirs))
	var total int64

	for _, name := range SafeCacheDirs {
		p := filepath.Join(userData, name)
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		sz := dirSize(p)
		total += sz
		items = append(items, CacheItem{
			Name:     name,
			Path:     p,
			Bytes:    sz,
			Selected: sz > 0,
			Exists:   true,
		})
	}
	return items, total
}

// CleanSelected moves selected cache folders into GravityBox's maintenance trash (non-destructive).
func CleanSelected(items []CacheItem) (movedBytes int64, movedCount int, errs []string) {
	trashRoot := filepath.Join(config.TrashDir(), time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return 0, 0, []string{err.Error()}
	}
	for _, item := range items {
		if !item.Selected || !item.Exists {
			continue
		}
		dst := filepath.Join(trashRoot, item.Name)
		if err := os.Rename(item.Path, dst); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.Name, err))
			continue
		}
		movedBytes += item.Bytes
		movedCount++
	}
	return movedBytes, movedCount, errs
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// FormatBytes formats a byte count into human-readable KiB/MiB/GiB units.
func FormatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
