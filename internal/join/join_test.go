package join

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igoose1/vidjoin/internal/ff"
	"github.com/igoose1/vidjoin/internal/opts"
)

var fullHD = opts.Size{W: 1920, H: 1080}

func TestFit(t *testing.T) {
	cases := []struct{ w, h, pw, ph int }{
		{3840, 2160, 1920, 1080}, // bigger, same aspect
		{640, 480, 1440, 1080},   // smaller 4:3 -> upscaled, pillarbox
		{1080, 1920, 608, 1080},  // portrait
		{1920, 800, 1920, 800},   // scope -> letterbox
		{1280, 720, 1920, 1080},  // smaller, same aspect -> full frame
	}
	for _, c := range cases {
		if pw, ph := Fit(c.w, c.h, fullHD); pw != c.pw || ph != c.ph {
			t.Errorf("Fit(%d,%d) = %dx%d, want %dx%d", c.w, c.h, pw, ph, c.pw, c.ph)
		}
	}
}

func TestPlanExactSamples(t *testing.T) {
	media := []*ff.Media{{Width: 1920, Height: 1080, Duration: 1.37, FPS: 25}}
	p := NewPlan(media, Settings{Size: fullHD, FPS: opts.FPS{Num: 30000, Den: 1001}})
	c := p.Clips[0]
	if c.Frames != 41 { // round(1.37 * 29.97)
		t.Fatalf("frames = %d", c.Frames)
	}
	// 41 frames at 30000/1001 = 1.368033 s = 65665.6 samples
	if c.Samples != 65666 {
		t.Fatalf("samples = %d", c.Samples)
	}
	if d := math.Abs(float64(c.Samples)/sampleRate - float64(c.Frames)*1001/30000); d > 1.0/sampleRate {
		t.Fatalf("audio/video length mismatch %v", d)
	}
}

func TestAutoFPS(t *testing.T) {
	media := []*ff.Media{{FPS: 25, Duration: 10}, {FPS: 29.97, Duration: 4}, {FPS: 30, Duration: 7}}
	if got := AutoFPS(media).String(); got != "25" {
		t.Errorf("AutoFPS = %s", got)
	}
}

func TestFilterGraph(t *testing.T) {
	s := Settings{Size: fullHD, FPS: opts.FPS{Num: 30, Den: 1}, BG: "#102030", Preset: opts.Quality}
	c := Clip{Media: &ff.Media{VideoIndex: 1, AudioIndex: -1, SARNum: 1, SARDen: 1}, Frames: 60, Samples: 96000}
	g := FilterGraph(c, s, ff.Software())
	for _, want := range []string{"[0:1]", "color=0x102030", "trim=end_frame=60", "anullsrc", "atrim=end_sample=96000", "[v]", "[a]"} {
		if !strings.Contains(g, want) {
			t.Errorf("graph lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "sar/2") {
		t.Error("square pixels should not get SAR correction")
	}
}

type nopReporter struct{}

func (nopReporter) Encoding(int64, int64, int, int) {}
func (nopReporter) Joining(float64, float64)        {}
func (nopReporter) Warn(msg string)                 {}

// TestIntegration runs the whole pipeline with real ffmpeg. It is skipped
// when ffmpeg/ffprobe are not available.
func TestIntegration(t *testing.T) {
	tools, err := ff.Locate()
	if err != nil {
		t.Skip("ffmpeg not available:", err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen := func(name, video, audio string) string {
		p := filepath.Join(dir, name)
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", video}
		if audio != "" {
			args = append(args, "-f", "lavfi", "-i", audio, "-c:a", "aac")
		}
		args = append(args, "-t", "1.5", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", p)
		if out, err := exec.Command(tools.FFmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("generating %s: %v\n%s", name, err, out)
		}
		return p
	}
	inputs := []string{
		gen("big 大.mp4", "testsrc2=s=1280x720:r=25", "sine=f=440:sample_rate=44100"),
		gen("small.mkv", "testsrc2=s=320x240:r=30", ""),
		gen("tall.mp4", "testsrc2=s=240x432:r=24", "sine=f=880:sample_rate=48000"),
	}
	media, err := Probe(ctx, tools, inputs)
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Size: opts.Size{W: 640, H: 360}, AutoFPS: true, BG: "#000000", Preset: opts.Fast,
		Output: filepath.Join(dir, "out 输出.mp4")}
	p := NewPlan(media, s)
	enc, err := ff.Detect(ctx, tools, s.Size, p.Settings.FPS)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("encoder: %s", enc.Name)
	if err := Execute(ctx, tools, p, enc, nopReporter{}); err != nil {
		t.Fatal(err)
	}
	res, err := ff.Probe(ctx, tools, s.Output)
	if err != nil {
		t.Fatal(err)
	}
	if res.Width != 640 || res.Height != 360 || res.AudioIndex < 0 {
		t.Fatalf("unexpected output: %+v", res)
	}
	if d := math.Abs(res.Duration - p.Duration()); d > 0.1 {
		t.Fatalf("duration %.3f, planned %.3f", res.Duration, p.Duration())
	}
	// Temporary files must be gone.
	left, _ := filepath.Glob(filepath.Join(dir, ".vidjoin-tmp-*"))
	if len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
	_ = os.Remove(s.Output)
}
