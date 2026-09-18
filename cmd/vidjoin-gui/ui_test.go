package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"

	"github.com/igoose1/vidjoin/internal/order"
)

// setup returns a folder of (empty) video files and redirects the order
// cache to a temporary directory.
func setup(t *testing.T, names ...string) string {
	t.Helper()
	cache := t.TempDir()
	old := order.CacheDir
	order.CacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { order.CacheDir = old })
	dir := t.TempDir()
	for _, n := range names {
		touch(t, filepath.Join(dir, n))
	}
	return dir
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestUI(t *testing.T) *ui {
	t.Helper()
	a := test.NewTempApp(t)
	w := a.NewWindow("test")
	w.Resize(fyne.NewSize(900, 600))
	u := newUI(a, w)
	u.toolsErr = os.ErrNotExist // don't run ffprobe on the fake files
	return u
}

func names(u *ui) []string {
	var out []string
	for _, c := range u.clips {
		out = append(out, filepath.Base(c.path))
	}
	return out
}

func check(t *testing.T, u *ui, want ...string) {
	t.Helper()
	if got := names(u); !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %q, want %q", got, want)
	}
}

func TestOrderIsRememberedPerFolder(t *testing.T) {
	dir := setup(t, "clip1.mp4", "clip2.mp4", "clip10.mp4", "视频.mkv", "notes.txt")

	u := newTestUI(t)
	u.addFolder(dir)
	check(t, u, "clip1.mp4", "clip2.mp4", "clip10.mp4", "视频.mkv")

	// Plain name order is not saved.
	if c, _ := order.LoadCache(dir); c != nil {
		t.Fatal("cache saved without a custom order")
	}

	u.move(0, +1) // clip2, clip1, clip10, 视频
	u.move(3, -1) // clip2, clip1, 视频, clip10
	u.remove(1)   // clip2, 视频, clip10
	u.move(0, -1) // no-op at the top
	u.move(2, +1) // no-op at the bottom
	check(t, u, "clip2.mp4", "视频.mkv", "clip10.mp4")

	// A new file appears in the folder; the app is started again.
	touch(t, filepath.Join(dir, "clip3.mp4"))
	u2 := newTestUI(t)
	u2.addFolder(dir)
	check(t, u2, "clip2.mp4", "视频.mkv", "clip10.mp4", "clip3.mp4")

	// Choosing a removed file on purpose brings it back and un-removes it.
	u2.addFiles([]string{filepath.Join(dir, "clip1.mp4")})
	check(t, u2, "clip2.mp4", "视频.mkv", "clip10.mp4", "clip3.mp4", "clip1.mp4")
	u3 := newTestUI(t)
	u3.addFolder(dir)
	check(t, u3, "clip2.mp4", "视频.mkv", "clip10.mp4", "clip3.mp4", "clip1.mp4")

	// Sort by name returns to the default and forgets the custom order.
	u3.sortByName()
	check(t, u3, "clip1.mp4", "clip2.mp4", "clip3.mp4", "clip10.mp4", "视频.mkv")
	if c, _ := order.LoadCache(dir); c != nil {
		t.Fatalf("cache kept after sorting by name: %+v", c)
	}
}

func TestOutputIsNotAnInput(t *testing.T) {
	dir := setup(t, "a.mp4", "b.mp4", defaultOutput)
	u := newTestUI(t)
	u.addFolder(dir)
	check(t, u, "a.mp4", "b.mp4")
	if got, want := u.output.Text, filepath.Join(dir, defaultOutput); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestDropAndDuplicates(t *testing.T) {
	dir := setup(t, "b.mp4", "a.mp4", "readme.txt")
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	touch(t, filepath.Join(sub, "c.mov"))

	u := newTestUI(t)
	u.open([]string{filepath.Join(dir, "b.mp4"), filepath.Join(dir, "readme.txt"), filepath.Join(dir, "a.mp4"), sub})
	check(t, u, "c.mov", "a.mp4", "b.mp4") // folder first, then the files, sorted
	u.open([]string{filepath.Join(dir, "a.mp4")})
	check(t, u, "c.mov", "a.mp4", "b.mp4") // no duplicates
}

func TestSettingsValidation(t *testing.T) {
	dir := setup(t, "a.mp4")
	u := newTestUI(t)
	u.addFolder(dir)
	if _, err := u.settings(); err != nil {
		t.Fatalf("defaults should be valid: %v", err)
	}
	u.size.SetText("1921x1080")
	if _, err := u.settings(); err == nil {
		t.Fatal("odd size accepted")
	}
	u.size.SetText("4k")
	u.output.SetText(filepath.Join(dir, "a.mp4"))
	if _, err := u.settings(); err == nil {
		t.Fatal("output equal to an input accepted")
	}
	u.output.SetText(filepath.Join(dir, "out.avi"))
	if _, err := u.settings(); err == nil {
		t.Fatal(".avi output accepted")
	}
}

func TestRelativeList(t *testing.T) {
	dir := setup(t, "a.mp4")
	u := newTestUI(t)
	u.addFolder(dir)
	outside := filepath.Join(t.TempDir(), "x", "y")
	if got := u.relativeTo(dir); !reflect.DeepEqual(got, []string{"a.mp4"}) {
		t.Fatalf("got %q", got)
	}
	if got := u.relativeTo(outside); got[0] != filepath.ToSlash(filepath.Join(dir, "a.mp4")) {
		t.Fatalf("got %q", got)
	}
}

func TestQuitShortcut(t *testing.T) {
	u := newTestUI(t)
	menu := u.win.MainMenu()
	if menu == nil || len(menu.Items) == 0 {
		t.Fatal("no main menu")
	}
	for _, item := range menu.Items[0].Items {
		if !item.IsQuit {
			continue
		}
		sc, ok := item.Shortcut.(*desktop.CustomShortcut)
		if !ok || sc.KeyName != fyne.KeyQ || sc.Modifier != fyne.KeyModifierShortcutDefault {
			t.Fatalf("quit shortcut = %#v, want Ctrl+Q", item.Shortcut)
		}
		return
	}
	t.Fatal("no Quit item in the File menu")
}
