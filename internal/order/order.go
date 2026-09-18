// Package order decides which videos are joined and in what sequence:
// natural-sorted directory scans and the editable order.txt list format.
package order

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true,
	".avi": true, ".wmv": true, ".flv": true, ".mts": true, ".m2ts": true,
	".ts": true, ".3gp": true, ".mpg": true, ".mpeg": true, ".vob": true,
	".ogv": true, ".mxf": true, ".dv": true,
}

// IsVideo reports whether the file name has a known video extension.
func IsVideo(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// ScanDir returns the video files directly inside dir (not recursive),
// natural-sorted by file name. Hidden files are skipped. Any path listed
// in exclude (compared by absolute path) is skipped too, so the output
// file of a previous run is never picked up as an input.
func ScanDir(dir string, exclude ...string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, e := range exclude {
		if abs, err := filepath.Abs(e); err == nil {
			skip[abs] = true
		}
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !IsVideo(n) {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(dir, n)); err == nil && skip[abs] {
			continue
		}
		names = append(names, n)
	}
	sort.SliceStable(names, func(i, j int) bool { return NaturalLess(names[i], names[j]) })
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(dir, n)
	}
	return paths, nil
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// NaturalLess compares strings the way people expect file names to sort:
// case-insensitive, with digit runs compared by numeric value, so
// "clip2" < "clip10". Non-ASCII text (e.g. Chinese) compares by code point.
func NaturalLess(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	tie := 0 // secondary ordering: fewer leading zeros first
	for i < len(ra) && j < len(rb) {
		if isDigit(ra[i]) && isDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && isDigit(ra[i]) {
				i++
			}
			for j < len(rb) && isDigit(rb[j]) {
				j++
			}
			na := strings.TrimLeft(string(ra[si:i]), "0")
			nb := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			if tie == 0 && (i-si) != (j-sj) {
				if (i - si) < (j - sj) {
					tie = -1
				} else {
					tie = 1
				}
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	if len(ra)-i != len(rb)-j {
		return len(ra)-i < len(rb)-j
	}
	if tie != 0 {
		return tie < 0
	}
	return a < b
}

// ReadList parses an order file. Blank lines and lines starting with '#'
// are ignored; a UTF-8 BOM and Windows line endings are tolerated.
// Relative paths are resolved against the list file's directory.
func ReadList(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return nil, fmt.Errorf("%s is saved as UTF-16; please save it as UTF-8", path)
	}
	base := filepath.Dir(path)
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate paths pasted with surrounding quotes.
		if len(line) >= 2 && (line[0] == '"' && line[len(line)-1] == '"' || line[0] == '\'' && line[len(line)-1] == '\'') {
			line = line[1 : len(line)-1]
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(base, line)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no videos", path)
	}
	return out, nil
}

const listHeader = `# vidjoin order file
# One video per line. Top to bottom = order in the final video.
# Reorder, delete or duplicate lines freely. Lines starting with # are ignored.
# Paths are relative to this file's folder; absolute paths work too.
`

// WriteList writes names (one per line, below an explanatory header) as UTF-8.
func WriteList(path string, names []string) error {
	nl := "\n"
	if runtime.GOOS == "windows" {
		nl = "\r\n"
	}
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(listHeader, "\n", nl))
	b.WriteString(nl)
	for _, n := range names {
		if strings.HasPrefix(n, "#") {
			n = "./" + n // otherwise it would be read back as a comment
		}
		b.WriteString(n)
		b.WriteString(nl)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
