// Package join normalizes video clips to one common format and joins them.
//
// Stage 1 re-encodes every clip to the target size and frame rate with
// PCM audio. Each clip is cut to an exact whole number of video frames and
// its audio to exactly the matching number of samples, starting at 0, so
// audio and video in every intermediate file are the same length.
// Stage 2 joins the intermediates with ffmpeg's concat demuxer, copying
// the video and encoding the audio to AAC once as one continuous stream.
// Because no clip's audio is ever longer or shorter than its video, the
// offsets cannot accumulate into drift, however many clips are joined.
package join

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"vidjoin/internal/ff"
	"vidjoin/internal/opts"
)

const sampleRate = 48000

// Settings describe the output.
type Settings struct {
	Size    opts.Size
	FPS     opts.FPS // zero value = auto
	BG      string   // "#rrggbb"
	Preset  opts.Preset
	Output  string
	AutoFPS bool
}

// Clip is one input with its exact normalized length.
type Clip struct {
	Media   *ff.Media
	Frames  int // output video frames
	Samples int // output audio samples at 48 kHz
	PlacedW int // size of the picture inside the frame
	PlacedH int
}

// Plan is everything decided before encoding starts.
type Plan struct {
	Settings Settings
	Clips    []Clip
}

// Duration is the length of the joined video in seconds.
func (p *Plan) Duration() float64 {
	n := 0
	for _, c := range p.Clips {
		n += c.Frames
	}
	return float64(n) / p.Settings.FPS.Float()
}

// TotalFrames is the number of frames in the joined video.
func (p *Plan) TotalFrames() int {
	n := 0
	for _, c := range p.Clips {
		n += c.Frames
	}
	return n
}

// Probe reads every input (in parallel) and returns the media info in order.
func Probe(ctx context.Context, t ff.Tools, inputs []string) ([]*ff.Media, error) {
	media := make([]*ff.Media, len(inputs))
	errs := make([]error, len(inputs))
	sem := make(chan struct{}, clampInt(runtime.NumCPU(), 2, 8))
	var wg sync.WaitGroup
	for i, in := range inputs {
		wg.Add(1)
		go func(i int, in string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			media[i], errs[i] = ff.Probe(ctx, t, in)
		}(i, in)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return media, nil
}

// AutoFPS picks the standard frame rate that covers the most footage.
// Ties go to the higher rate.
func AutoFPS(media []*ff.Media) opts.FPS {
	weight := map[opts.FPS]float64{}
	for _, m := range media {
		weight[opts.Snap(m.FPS)] += m.Duration
	}
	best, bestW := opts.FPS{Num: 30, Den: 1}, -1.0
	for _, s := range opts.Standard {
		if w, ok := weight[s]; ok && w >= bestW {
			best, bestW = s, w
		}
	}
	return best
}

// Fit returns the size of a w×h picture scaled to fit inside the frame,
// keeping its aspect ratio (enlarging small pictures), with even dimensions.
func Fit(w, h int, frame opts.Size) (int, int) {
	s := math.Min(float64(frame.W)/float64(w), float64(frame.H)/float64(h))
	pw := int(math.Round(float64(w)*s/2)) * 2
	ph := int(math.Round(float64(h)*s/2)) * 2
	return clampInt(pw, 2, frame.W), clampInt(ph, 2, frame.H)
}

// NewPlan computes exact frame and sample counts for every clip.
func NewPlan(media []*ff.Media, s Settings) *Plan {
	if s.AutoFPS {
		s.FPS = AutoFPS(media)
	}
	p := &Plan{Settings: s}
	fps := s.FPS.Float()
	for _, m := range media {
		frames := int(math.Round(m.Duration * fps))
		if frames < 1 {
			frames = 1
		}
		// Samples for exactly `frames` frames, computed with integers so
		// NTSC rates (30000/1001) don't accumulate rounding error.
		samples := int(math.Round(float64(frames) * sampleRate * float64(s.FPS.Den) / float64(s.FPS.Num)))
		pw, ph := Fit(m.Width, m.Height, s.Size)
		p.Clips = append(p.Clips, Clip{Media: m, Frames: frames, Samples: samples, PlacedW: pw, PlacedH: ph})
	}
	return p
}

func scaleFlags(p opts.Preset) string {
	switch p {
	case opts.Fast:
		return "bilinear"
	case opts.Balanced:
		return "bicubic"
	}
	return "lanczos"
}

// FilterGraph builds the ffmpeg -filter_complex for one clip. Outputs are
// labelled [v] and [a].
func FilterGraph(c Clip, s Settings, enc ff.Encoder) string {
	m := c.Media
	W, H := s.Size.W, s.Size.H
	var v []string
	if m.SARNum != m.SARDen {
		// Non-square pixels (e.g. DV, some broadcast material): make them
		// square first so the picture isn't stretched.
		v = append(v, "scale=w='trunc(iw*sar/2)*2':h=ih", "setsar=1")
	}
	v = append(v,
		// Fit inside the frame, keeping the aspect ratio (up or down).
		fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2:flags=%s", W, H, scaleFlags(s.Preset)),
		// Center it on a background of the chosen color.
		fmt.Sprintf("pad=w=%d:h=%d:x=(ow-iw)/2:y=(oh-ih)/2:color=0x%s", W, H, strings.TrimPrefix(s.BG, "#")),
		"setsar=1",
		// Constant frame rate. start_time=0 fills any gap before the first
		// frame (video starting later than audio) with that frame.
		fmt.Sprintf("fps=fps=%s:start_time=0", s.FPS),
		// If the video ends before the audio, hold the last frame.
		"tpad=stop=-1:stop_mode=clone",
		// Exactly the planned number of frames.
		fmt.Sprintf("trim=end_frame=%d", c.Frames),
		"setpts=PTS-STARTPTS",
		"format=yuv420p",
	)
	if enc.Filter != "" {
		v = append(v, enc.Filter)
	}
	graph := fmt.Sprintf("[0:%d]%s[v]", m.VideoIndex, strings.Join(v, ","))

	var a string
	if m.AudioIndex >= 0 {
		a = fmt.Sprintf("[0:%d]", m.AudioIndex) + strings.Join([]string{
			// Resample to 48 kHz; async fills timestamp gaps with silence and
			// first_pts=0 pads the start if audio begins after the video.
			fmt.Sprintf("aresample=%d:async=1:first_pts=0", sampleRate),
			fmt.Sprintf("aformat=sample_fmts=s16:sample_rates=%d:channel_layouts=stereo", sampleRate),
			// Pad with silence if audio is short, then cut to the exact length.
			"apad",
			fmt.Sprintf("atrim=end_sample=%d", c.Samples),
			"asetpts=PTS-STARTPTS",
		}, ",") + "[a]"
	} else {
		// No audio track: generate silence of exactly the clip's length.
		a = strings.Join([]string{
			fmt.Sprintf("anullsrc=r=%d:cl=stereo", sampleRate),
			fmt.Sprintf("aformat=sample_fmts=s16:sample_rates=%d:channel_layouts=stereo", sampleRate),
			fmt.Sprintf("atrim=end_sample=%d", c.Samples),
		}, ",") + "[a]"
	}
	return graph + ";" + a
}

// NormalizeArgs returns the ffmpeg arguments that turn clip c into out.
func NormalizeArgs(c Clip, s Settings, enc ff.Encoder, out string) []string {
	args := append([]string{"-y"}, enc.Global...)
	args = append(args,
		"-i", c.Media.Path,
		"-filter_complex", FilterGraph(c, s, enc),
		"-map", "[v]", "-map", "[a]",
	)
	args = append(args, enc.Args(s.Preset)...)
	args = append(args,
		"-g", fmt.Sprint(int(math.Round(s.FPS.Float()*2))), // keyframe every 2 s
		"-c:a", "pcm_s16le",
		"-map_metadata", "-1", "-map_chapters", "-1",
		out,
	)
	return args
}

func audioBitrate(p opts.Preset) string {
	switch p {
	case opts.Fast:
		return "160k"
	case opts.Balanced:
		return "192k"
	}
	return "256k"
}

// ConcatArgs returns the ffmpeg arguments for the joining stage.
func ConcatArgs(listFile, out string, p opts.Preset) []string {
	args := []string{"-y",
		"-f", "concat", "-safe", "0", "-i", listFile,
		"-map", "0:v:0", "-map", "0:a:0",
		"-c:v", "copy",
		"-c:a", "aac", "-b:a", audioBitrate(p),
	}
	switch strings.ToLower(filepath.Ext(out)) {
	case ".mp4", ".m4v", ".mov":
		args = append(args, "-movflags", "+faststart")
	}
	return append(args, out)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Reporter receives progress updates. Calls may come from several goroutines.
type Reporter interface {
	Encoding(done, total int64, clipsDone, clips int)
	Joining(done, total float64)
	Warn(msg string)
}

// ErrEncoder marks a failure that happened with a hardware encoder, so the
// caller may retry in software.
type ErrEncoder struct {
	Clip string
	Err  error
}

func (e *ErrEncoder) Error() string { return fmt.Sprintf("%s: %v", e.Clip, e.Err) }
func (e *ErrEncoder) Unwrap() error { return e.Err }

// Execute runs both stages and writes the output file.
func Execute(ctx context.Context, t ff.Tools, p *Plan, enc ff.Encoder, rep Reporter) error {
	s := p.Settings
	outDir := filepath.Dir(s.Output)
	tmp, err := os.MkdirTemp(outDir, ".vidjoin-tmp-")
	if err != nil {
		return fmt.Errorf("cannot create temporary folder next to the output: %w", err)
	}
	if os.Getenv("VIDJOIN_KEEP_TEMP") != "" {
		rep.Warn("keeping temporary files in " + tmp)
	} else {
		defer os.RemoveAll(tmp)
	}

	if err := encodeAll(ctx, t, p, enc, tmp, rep); err != nil {
		var ee *ErrEncoder
		if enc.Hardware && errors.As(err, &ee) && ctx.Err() == nil {
			rep.Warn(fmt.Sprintf("%s failed on %s; re-encoding everything in software so all clips match.\n%v",
				enc.Label, filepath.Base(ee.Clip), ee.Err))
			enc = ff.Software()
			err = encodeAll(ctx, t, p, enc, tmp, rep)
		}
		if err != nil {
			return err
		}
	}

	// Stage 2: join. The list uses plain relative names, so no escaping or
	// Unicode issues can arise regardless of the input file names.
	var list strings.Builder
	for i := range p.Clips {
		fmt.Fprintf(&list, "file '%s'\n", partName(i))
	}
	listFile := filepath.Join(tmp, "list.txt")
	if err := os.WriteFile(listFile, []byte(list.String()), 0o644); err != nil {
		return err
	}
	joined := filepath.Join(tmp, "joined"+filepath.Ext(s.Output))
	total := p.Duration()
	rep.Joining(0, total)
	err = ff.Run(ctx, t, ConcatArgs(listFile, joined, s.Preset), func(pr ff.Progress) {
		rep.Joining(math.Min(pr.OutTime, total), total)
	})
	if err != nil {
		return fmt.Errorf("joining failed: %w", err)
	}
	rep.Joining(total, total)
	if err := os.Rename(joined, s.Output); err != nil {
		return fmt.Errorf("cannot write %s: %w", s.Output, err)
	}
	return nil
}

func partName(i int) string { return fmt.Sprintf("part%05d.mov", i) }

func encodeAll(ctx context.Context, t ff.Tools, p *Plan, enc ff.Encoder, tmp string, rep Reporter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := clampInt(enc.Jobs, 1, len(p.Clips))
	if enc.Hardware {
		// Decoding and scaling still run on the CPU.
		jobs = clampInt(jobs, 1, max(1, runtime.NumCPU()/2))
	}
	total := int64(p.TotalFrames())
	var mu sync.Mutex
	frames := make([]int64, len(p.Clips))
	clipsDone := 0
	report := func() {
		var sum int64
		for _, f := range frames {
			sum += f
		}
		rep.Encoding(sum, total, clipsDone, len(p.Clips))
	}
	report()

	work := make(chan int)
	var firstErr error
	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				c := p.Clips[i]
				out := filepath.Join(tmp, partName(i))
				err := ff.Run(ctx, t, NormalizeArgs(c, p.Settings, enc, out), func(pr ff.Progress) {
					mu.Lock()
					frames[i] = min(pr.Frame, int64(c.Frames))
					report()
					mu.Unlock()
				})
				mu.Lock()
				if err != nil {
					if firstErr == nil && ctx.Err() == nil {
						firstErr = &ErrEncoder{Clip: c.Media.Path, Err: err}
					}
					mu.Unlock()
					cancel()
					continue
				}
				frames[i] = int64(c.Frames)
				clipsDone++
				report()
				mu.Unlock()
			}
		}()
	}
	for i := range p.Clips {
		select {
		case work <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
