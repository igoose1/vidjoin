package order

import (
	"path/filepath"
	"reflect"
	"testing"
)

func useTempCache(t *testing.T) {
	dir := t.TempDir()
	old := CacheDir
	CacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { CacheDir = old })
}

func TestCacheRoundTrip(t *testing.T) {
	useTempCache(t)
	folder := t.TempDir()
	if c, err := LoadCache(folder); c != nil || err != nil {
		t.Fatalf("empty cache: %v %v", c, err)
	}
	want := &Cache{Folder: folder, Order: []string{"b.mp4", "视频.mp4", "a.mp4"}, Removed: []string{"x.mp4"}}
	if err := SaveCache(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCache(folder + string(filepath.Separator)) // same folder, different spelling
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if !reflect.DeepEqual(got.Order, want.Order) || !reflect.DeepEqual(got.Removed, want.Removed) {
		t.Fatalf("got %+v", got)
	}
	if err := DeleteCache(folder); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadCache(folder); c != nil {
		t.Fatal("cache not deleted")
	}
}

func TestArrange(t *testing.T) {
	c := &Cache{Order: []string{"c.mp4", "a.mp4", "b.mp4"}, Removed: []string{"b.mp4"}}
	in := []string{"/v/a.mp4", "/v/b.mp4", "/v/c.mp4", "/v/new1.mp4", "/v/new2.mp4"}

	got, restored, added := c.Arrange(in, true)
	want := []string{"/v/c.mp4", "/v/a.mp4", "/v/new1.mp4", "/v/new2.mp4"}
	if !reflect.DeepEqual(got, want) || restored != 2 || added != 2 {
		t.Fatalf("dropRemoved: got %q %d %d", got, restored, added)
	}

	got, _, _ = c.Arrange(in, false)
	want = []string{"/v/c.mp4", "/v/a.mp4", "/v/b.mp4", "/v/new1.mp4", "/v/new2.mp4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keepRemoved: got %q", got)
	}
}
