package order

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	got := []string{"clip10.mp4", "Clip2.mp4", "clip1.mp4", "clip02.mp4", "b.mp4", "a10b2.mp4", "a10b10.mp4", "视频2.mp4", "视频10.mp4"}
	want := []string{"a10b2.mp4", "a10b10.mp4", "b.mp4", "clip1.mp4", "Clip2.mp4", "clip02.mp4", "clip10.mp4", "视频2.mp4", "视频10.mp4"}
	sort.SliceStable(got, func(i, j int) bool { return NaturalLess(got[i], got[j]) })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	names := []string{"视频 测试.mp4", "#hash.mp4", "sub/clip 2.mov"}
	p := filepath.Join(dir, "order.txt")
	if err := WriteList(p, names); err != nil {
		t.Fatal(err)
	}
	got, err := ReadList(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "视频 测试.mp4"), filepath.Join(dir, "#hash.mp4"), filepath.Join(dir, "sub", "clip 2.mov")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestReadListBOMAndCRLF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "o.txt")
	os.WriteFile(p, []byte("\xef\xbb\xbf# comment\r\n\r\n  b.mp4  \r\n\"a b.mp4\"\r\n"), 0o644)
	got, err := ReadList(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "b.mp4"), filepath.Join(dir, "a b.mp4")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	os.WriteFile(p, []byte{0xff, 0xfe, 'a', 0}, 0o644)
	if _, err := ReadList(p); err == nil {
		t.Fatal("expected UTF-16 error")
	}
}

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"c10.mp4", "c9.MKV", ".hidden.mp4", "notes.txt", "out.mp4"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	os.Mkdir(filepath.Join(dir, "sub.mp4"), 0o755)
	got, err := ScanDir(dir, filepath.Join(dir, "out.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "c9.MKV"), filepath.Join(dir, "c10.mp4")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}
