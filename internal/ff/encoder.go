package ff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/igoose1/vidjoin/internal/opts"
)

// Encoder is an H.264 encoder together with the ffmpeg arguments it needs.
type Encoder struct {
	Name     string // ffmpeg encoder name, e.g. h264_nvenc
	Label    string // human-readable description
	Hardware bool
	Global   []string // arguments placed before the inputs (device setup)
	Filter   string   // appended to the video filter chain (e.g. hwupload)
	Jobs     int      // clips encoded in parallel
	args     func(p opts.Preset) []string
}

// Args returns the encoder arguments for a preset.
func (e Encoder) Args(p opts.Preset) []string {
	return append([]string{"-c:v", e.Name}, e.args(p)...)
}

func pick(p opts.Preset, quality, balanced, fast string) string {
	switch p {
	case opts.Fast:
		return fast
	case opts.Balanced:
		return balanced
	}
	return quality
}

func software() Encoder {
	jobs := runtime.NumCPU() / 4 // x264 already uses every core
	return Encoder{
		Name: "libx264", Label: "software (x264)", Jobs: clamp(jobs, 1, 4),
		args: func(p opts.Preset) []string {
			return []string{
				"-preset", pick(p, "slow", "medium", "veryfast"),
				"-crf", pick(p, "18", "21", "24"),
				"-profile:v", "high",
			}
		},
	}
}

func nvenc() Encoder {
	return Encoder{
		Name: "h264_nvenc", Label: "NVIDIA NVENC", Hardware: true, Jobs: 3,
		args: func(p opts.Preset) []string {
			return []string{
				"-preset", pick(p, "p7", "p5", "p2"), "-tune", "hq",
				"-rc", "vbr", "-cq", pick(p, "19", "23", "26"), "-b:v", "0",
				"-spatial-aq", "1", "-profile:v", "high",
			}
		},
	}
}

func qsv() Encoder {
	return Encoder{
		Name: "h264_qsv", Label: "Intel Quick Sync", Hardware: true, Jobs: 2,
		args: func(p opts.Preset) []string {
			return []string{
				"-preset", pick(p, "veryslow", "medium", "veryfast"),
				"-global_quality", pick(p, "20", "23", "26"),
				"-profile:v", "high",
			}
		},
	}
}

func amf() Encoder {
	return Encoder{
		Name: "h264_amf", Label: "AMD AMF", Hardware: true, Jobs: 2,
		args: func(p opts.Preset) []string {
			qp := pick(p, "19", "23", "26")
			return []string{
				"-usage", "transcoding", "-quality", pick(p, "quality", "balanced", "speed"),
				"-rc", "cqp", "-qp_i", qp, "-qp_p", qp, "-qp_b", qp,
				"-profile:v", "high",
			}
		},
	}
}

func vaapi(device string) Encoder {
	return Encoder{
		Name: "h264_vaapi", Label: "VA-API (" + device + ")", Hardware: true, Jobs: 2,
		Global: []string{"-vaapi_device", device},
		Filter: "format=nv12,hwupload",
		args: func(p opts.Preset) []string {
			return []string{
				"-rc_mode", "CQP", "-qp", pick(p, "19", "23", "26"),
				"-profile:v", "high",
			}
		},
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Software returns the x264 encoder, which always works.
func Software() Encoder { return software() }

func candidates() []Encoder {
	c := []Encoder{nvenc(), qsv()}
	switch runtime.GOOS {
	case "windows":
		c = append(c, amf())
	case "linux":
		nodes, _ := filepath.Glob("/dev/dri/renderD*")
		sort.Strings(nodes)
		for _, n := range nodes {
			c = append(c, vaapi(n))
		}
	}
	return c
}

// ByName returns the encoder with the given ffmpeg name (for the
// VIDJOIN_ENCODER override). For h264_vaapi, the first render node is used.
func ByName(name string) (Encoder, bool) {
	if name == "libx264" {
		return software(), true
	}
	for _, e := range candidates() {
		if e.Name == name {
			return e, true
		}
	}
	if name == "h264_vaapi" {
		return vaapi("/dev/dri/renderD128"), true
	}
	return Encoder{}, false
}

// Test encodes a few frames at the target size to check that e works on
// this machine (driver installed, GPU present, size supported).
func Test(ctx context.Context, t Tools, e Encoder, size opts.Size, fps opts.FPS) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vf := "format=yuv420p"
	if e.Filter != "" {
		vf += "," + e.Filter
	}
	args := append([]string{}, e.Global...)
	args = append(args,
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%s:r=%s", size, fps),
		"-frames:v", "10", "-vf", vf)
	args = append(args, e.Args(opts.Fast)...)
	args = append(args, "-f", "null", "-")
	return Run(ctx, t, args, nil)
}

// Detect picks the best working encoder: a hardware encoder if one works,
// otherwise x264. VIDJOIN_ENCODER=<ffmpeg encoder name> forces a choice.
func Detect(ctx context.Context, t Tools, size opts.Size, fps opts.FPS) (Encoder, error) {
	if forced := strings.TrimSpace(os.Getenv("VIDJOIN_ENCODER")); forced != "" {
		e, ok := ByName(forced)
		if !ok {
			return Encoder{}, fmt.Errorf("VIDJOIN_ENCODER=%s: unknown encoder (use libx264, h264_nvenc, h264_qsv, h264_amf or h264_vaapi)", forced)
		}
		if err := Test(ctx, t, e, size, fps); err != nil {
			return Encoder{}, fmt.Errorf("VIDJOIN_ENCODER=%s does not work on this machine: %v", forced, err)
		}
		return e, nil
	}
	for _, e := range candidates() {
		if ctx.Err() != nil {
			return Encoder{}, ctx.Err()
		}
		if Test(ctx, t, e, size, fps) == nil {
			return e, nil
		}
	}
	return software(), ctx.Err()
}
