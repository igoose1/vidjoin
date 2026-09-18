// Package ff wraps the ffmpeg and ffprobe executables.
package ff

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Tools holds the paths of the ffmpeg and ffprobe executables.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// Locate finds ffmpeg and ffprobe: first next to the vidjoin executable
// (the bundled copies), then on PATH.
func Locate() (Tools, error) {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	find := func(name string) (string, error) {
		for _, d := range dirs {
			p := filepath.Join(d, exeName(name))
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
		return exec.LookPath(name)
	}
	var t Tools
	var err error
	if t.FFmpeg, err = find("ffmpeg"); err != nil {
		return t, errors.New("ffmpeg not found: put ffmpeg next to vidjoin or on your PATH")
	}
	if t.FFprobe, err = find("ffprobe"); err != nil {
		return t, errors.New("ffprobe not found: put ffprobe next to vidjoin or on your PATH")
	}
	return t, nil
}

// Error is a failed ffmpeg run, carrying the tail of its log.
type Error struct {
	Err error
	Log string
}

func (e *Error) Error() string {
	if e.Log == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + "\n" + e.Log
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// Progress is one ffmpeg "-progress" report.
type Progress struct {
	Frame   int64
	OutTime float64 // seconds of output written
}

// Run executes ffmpeg with args. If onProgress is non-nil, "-progress
// pipe:1" is added and every report is passed to it.
func Run(ctx context.Context, t Tools, args []string, onProgress func(Progress)) error {
	full := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if onProgress != nil {
		full = append(full, "-progress", "pipe:1", "-nostats")
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, t.FFmpeg, full...)
	hideWindow(cmd)
	stderr := &tailBuffer{max: 4000}
	cmd.Stderr = stderr
	var stdout io.ReadCloser
	if onProgress != nil {
		var err error
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if stdout != nil {
		sc := bufio.NewScanner(stdout)
		var p Progress
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			switch k {
			case "frame":
				p.Frame, _ = strconv.ParseInt(v, 10, 64)
			case "out_time_us":
				if us, err := strconv.ParseInt(v, 10, 64); err == nil && us >= 0 {
					p.OutTime = float64(us) / 1e6
				}
			case "progress":
				onProgress(p)
			}
		}
		io.Copy(io.Discard, stdout)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Err: fmt.Errorf("ffmpeg failed: %w", err), Log: stderr.String()}
	}
	return nil
}

// Media describes the parts of an input file that matter for joining.
type Media struct {
	Path       string
	VideoIndex int // absolute stream index of the video stream
	AudioIndex int // absolute stream index of the audio stream, -1 if none
	Width      int // displayed width (after rotation and pixel aspect)
	Height     int // displayed height
	SARNum     int // sample (pixel) aspect ratio; 1:1 for square pixels
	SARDen     int
	FPS        float64 // average frame rate, 0 if unknown
	Duration   float64 // seconds
	Codec      string
}

type probeJSON struct {
	Streams []struct {
		Index             int               `json:"index"`
		CodecType         string            `json:"codec_type"`
		CodecName         string            `json:"codec_name"`
		Width             int               `json:"width"`
		Height            int               `json:"height"`
		SampleAspectRatio string            `json:"sample_aspect_ratio"`
		AvgFrameRate      string            `json:"avg_frame_rate"`
		RFrameRate        string            `json:"r_frame_rate"`
		Duration          string            `json:"duration"`
		Tags              map[string]string `json:"tags"`
		Disposition       map[string]int    `json:"disposition"`
		SideData          []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func ratio(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		n, d, ok = strings.Cut(s, ":")
	}
	if !ok {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	a, err1 := strconv.ParseFloat(n, 64)
	b, err2 := strconv.ParseFloat(d, 64)
	if err1 != nil || err2 != nil || b == 0 {
		return 0
	}
	return a / b
}

func parseDur(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// Probe inspects a media file with ffprobe.
func Probe(ctx context.Context, t Tools, path string) (*Media, error) {
	cmd := exec.CommandContext(ctx, t.FFprobe, "-v", "error", "-print_format", "json",
		"-show_streams", "-show_format", path)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("cannot read %s: %s", path, msg)
	}
	var pj probeJSON
	if err := json.Unmarshal(out.Bytes(), &pj); err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	m := &Media{Path: path, VideoIndex: -1, AudioIndex: -1, SARNum: 1, SARDen: 1}
	var vDur, aDur float64
	for _, s := range pj.Streams {
		switch s.CodecType {
		case "video":
			if m.VideoIndex >= 0 || s.Disposition["attached_pic"] == 1 || s.Width == 0 {
				continue // skip cover art and extra video streams
			}
			m.VideoIndex = s.Index
			m.Codec = s.CodecName
			m.Width, m.Height = s.Width, s.Height
			if n, d, ok := strings.Cut(s.SampleAspectRatio, ":"); ok {
				ni, _ := strconv.Atoi(n)
				di, _ := strconv.Atoi(d)
				if ni > 0 && di > 0 {
					m.SARNum, m.SARDen = ni, di
				}
			}
			if m.SARNum != m.SARDen {
				m.Width = int(math.Round(float64(m.Width) * float64(m.SARNum) / float64(m.SARDen)))
			}
			rot := 0.0
			if r, ok := s.Tags["rotate"]; ok {
				rot, _ = strconv.ParseFloat(r, 64)
			}
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					rot = sd.Rotation
				}
			}
			if int(math.Abs(math.Round(rot)))%180 == 90 {
				m.Width, m.Height = m.Height, m.Width
			}
			m.FPS = ratio(s.AvgFrameRate)
			if m.FPS <= 0 || m.FPS > 1000 {
				m.FPS = ratio(s.RFrameRate)
			}
			vDur = parseDur(s.Duration)
		case "audio":
			if m.AudioIndex < 0 {
				m.AudioIndex = s.Index
				aDur = parseDur(s.Duration)
			}
		}
	}
	if m.VideoIndex < 0 {
		return nil, fmt.Errorf("%s has no video stream", path)
	}
	// The clip lasts as long as its longest stream (a player shows the
	// same). Fall back to the container duration when streams don't say.
	m.Duration = math.Max(vDur, aDur)
	if m.Duration == 0 {
		m.Duration = parseDur(pj.Format.Duration)
	}
	if m.Duration <= 0 {
		return nil, fmt.Errorf("%s: cannot determine duration", path)
	}
	return m, nil
}
