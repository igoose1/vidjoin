// Package opts parses and validates user-facing option values.
package opts

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Size is an output resolution in pixels.
type Size struct{ W, H int }

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }

var sizeAliases = map[string]Size{
	"480p": {854, 480}, "720p": {1280, 720}, "hd": {1280, 720},
	"1080p": {1920, 1080}, "fullhd": {1920, 1080}, "fhd": {1920, 1080},
	"1440p": {2560, 1440}, "2k": {2560, 1440},
	"2160p": {3840, 2160}, "4k": {3840, 2160}, "uhd": {3840, 2160},
}

// ParseSize accepts "WIDTHxHEIGHT" (e.g. 1920x1080) or an alias like 1080p or 4k.
func ParseSize(s string) (Size, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if a, ok := sizeAliases[s]; ok {
		return a, nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == '*' })
	if len(parts) != 2 {
		return Size{}, fmt.Errorf("invalid size %q: use WIDTHxHEIGHT, e.g. 1920x1080, or 720p/1080p/1440p/4k", s)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return Size{}, fmt.Errorf("invalid size %q", s)
	}
	if w%2 != 0 || h%2 != 0 {
		return Size{}, fmt.Errorf("size %dx%d: width and height must be even numbers", w, h)
	}
	if w < 64 || h < 64 || w > 8192 || h > 8192 {
		return Size{}, fmt.Errorf("size %dx%d is out of range (64..8192)", w, h)
	}
	return Size{w, h}, nil
}

// FPS is a frame rate as an exact fraction (30000/1001 for 29.97).
type FPS struct{ Num, Den int }

func (f FPS) Float() float64 { return float64(f.Num) / float64(f.Den) }

// String returns the ffmpeg notation ("30" or "30000/1001").
func (f FPS) String() string {
	if f.Den == 1 {
		return strconv.Itoa(f.Num)
	}
	return fmt.Sprintf("%d/%d", f.Num, f.Den)
}

// Pretty returns a human-readable rate ("29.97").
func (f FPS) Pretty() string {
	if f.Den == 1 {
		return strconv.Itoa(f.Num)
	}
	return strconv.FormatFloat(math.Round(f.Float()*1000)/1000, 'f', -1, 64)
}

// Standard frame rates that auto-detection snaps to.
var Standard = []FPS{
	{24000, 1001}, {24, 1}, {25, 1}, {30000, 1001}, {30, 1},
	{48, 1}, {50, 1}, {60000, 1001}, {60, 1},
}

// Snap returns the standard frame rate nearest to f (30 if f is unusable).
func Snap(f float64) FPS {
	if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return FPS{30, 1}
	}
	best, bestD := FPS{30, 1}, math.Inf(1)
	for _, s := range Standard {
		if d := math.Abs(s.Float() - f); d < bestD {
			best, bestD = s, d
		}
	}
	return best
}

// ParseFPS accepts "30", "29.97", "30000/1001". Rates within 0.01 of an
// NTSC rate (23.976, 29.97, 59.94) are mapped to the exact fraction.
func ParseFPS(s string) (FPS, error) {
	s = strings.TrimSpace(s)
	var f FPS
	if n, d, ok := strings.Cut(s, "/"); ok {
		num, err1 := strconv.Atoi(n)
		den, err2 := strconv.Atoi(d)
		if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
			return FPS{}, fmt.Errorf("invalid fps %q", s)
		}
		f = FPS{num, den}
	} else {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			return FPS{}, fmt.Errorf("invalid fps %q: use e.g. 25, 30, 29.97 or auto", s)
		}
		for _, st := range Standard {
			if st.Den != 1 && math.Abs(st.Float()-v) < 0.01 {
				return st, nil
			}
		}
		if v != math.Trunc(v) {
			// e.g. 12.5 -> 25/2
			f = FPS{int(math.Round(v * 1000)), 1000}
		} else {
			f = FPS{int(v), 1}
		}
	}
	if f.Float() < 1 || f.Float() > 240 {
		return FPS{}, fmt.Errorf("fps %s is out of range (1..240)", s)
	}
	return f, nil
}

// ParseColor accepts "#RRGGBB" or "RRGGBB" and returns it normalized as "#rrggbb".
func ParseColor(s string) (string, error) {
	c := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(c) == 3 { // #abc -> #aabbcc
		c = string([]byte{c[0], c[0], c[1], c[1], c[2], c[2]})
	}
	if len(c) != 6 {
		return "", fmt.Errorf("invalid color %q: use #RRGGBB, e.g. #000000", s)
	}
	if _, err := strconv.ParseUint(c, 16, 32); err != nil {
		return "", fmt.Errorf("invalid color %q: use #RRGGBB, e.g. #000000", s)
	}
	return "#" + strings.ToLower(c), nil
}

// Preset trades encoding speed against output quality.
type Preset string

const (
	Quality  Preset = "quality"
	Balanced Preset = "balanced"
	Fast     Preset = "fast"
)

func ParsePreset(s string) (Preset, error) {
	switch p := Preset(strings.ToLower(strings.TrimSpace(s))); p {
	case Quality, Balanced, Fast:
		return p, nil
	}
	return "", fmt.Errorf("invalid preset %q: use quality, balanced or fast", s)
}
