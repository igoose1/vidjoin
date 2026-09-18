package order

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Cache remembers the order a user chose for the videos of one folder, so
// the same order is restored the next time that folder is opened.
type Cache struct {
	Folder  string    `json:"folder"`
	Order   []string  `json:"order"`   // file names, in the chosen order
	Removed []string  `json:"removed"` // file names the user took out of the list
	Updated time.Time `json:"updated"`
}

// CacheDir is where caches are stored. It is a variable so tests can
// redirect it.
var CacheDir = func() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "vidjoin", "orders"), nil
}

// canonical returns the folder path used as the cache identity.
func canonical(folder string) string {
	abs, err := filepath.Abs(folder)
	if err != nil {
		abs = folder
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs) // paths are case-insensitive there
	}
	return abs
}

func cacheFile(folder string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical(folder)))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".json"), nil
}

// LoadCache returns the saved order for folder, or nil if there is none.
func LoadCache(folder string) (*Cache, error) {
	p, err := cacheFile(folder)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, nil // a damaged cache is simply ignored
	}
	return &c, nil
}

// SaveCache stores the order for c.Folder, replacing any earlier one.
func SaveCache(c *Cache) error {
	p, err := cacheFile(c.Folder)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	c.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DeleteCache forgets the saved order for folder.
func DeleteCache(folder string) error {
	p, err := cacheFile(folder)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Arrange sorts paths (files of the cached folder) by the saved order.
// Files in the saved order come first, in that order; files the cache
// doesn't know (added to the folder since) follow in their given order.
// If dropRemoved is set, files the user had removed are left out.
// It reports how many files were placed by the saved order, and how many
// were new.
func (c *Cache) Arrange(paths []string, dropRemoved bool) (out []string, restored, added int) {
	pos := make(map[string]int, len(c.Order))
	for i, n := range c.Order {
		if _, dup := pos[n]; !dup {
			pos[n] = i
		}
	}
	removed := map[string]bool{}
	for _, n := range c.Removed {
		removed[n] = true
	}
	var known, unknown []string
	for _, p := range paths {
		name := filepath.Base(p)
		switch {
		case dropRemoved && removed[name]:
		case hasKey(pos, name):
			known = append(known, p)
		default:
			unknown = append(unknown, p)
		}
	}
	// Stable insertion sort by saved position (lists are small).
	for i := 1; i < len(known); i++ {
		for j := i; j > 0 && pos[filepath.Base(known[j])] < pos[filepath.Base(known[j-1])]; j-- {
			known[j], known[j-1] = known[j-1], known[j]
		}
	}
	return append(known, unknown...), len(known), len(unknown)
}

func hasKey(m map[string]int, k string) bool {
	_, ok := m[k]
	return ok
}
