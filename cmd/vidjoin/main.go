// Command vidjoin joins video clips into one video of a fixed resolution.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/igoose1/vidjoin/internal/ff"
	"github.com/igoose1/vidjoin/internal/join"
	"github.com/igoose1/vidjoin/internal/opts"
	"github.com/igoose1/vidjoin/internal/order"
)

var version = "dev"

const usage = `vidjoin - join videos into one, letterboxed to a fixed resolution

Usage:
  vidjoin join [options] FILE1 FILE2 ...   join files in the order given
  vidjoin join [options] FOLDER            join all videos in FOLDER, sorted by name
  vidjoin join [options] --list order.txt  join files in the order listed in order.txt
  vidjoin order FOLDER [-o order.txt]      write an editable order.txt for FOLDER
  vidjoin version

Join options:
  -o, --output FILE   output file (default: joined.mp4)
  --list FILE         read the video order from FILE (see "vidjoin order")
  --size WxH          output resolution (default: 1920x1080; also 720p, 1080p, 1440p, 4k)
  --fps N             output frame rate: 24, 25, 29.97, 30, 50, 60... (default: auto)
  --bg COLOR          background color around the picture (default: #000000)
  --preset NAME       quality (default), balanced or fast
  --dry-run           show what would be done, don't encode
  -y                  overwrite the output file if it exists

Order options:
  -o, --output FILE   where to write the list (default: FOLDER/order.txt)
  -y                  overwrite an existing list

Every clip is scaled to fit the output size with its aspect ratio kept and
centered on the background color. A hardware encoder (NVIDIA, Intel, AMD)
is used automatically when available.
`

// flagSpec maps a flag name to whether it takes a value.
type flagSpec map[string]bool

// parseArgs separates flags from positional arguments. Flags may appear
// anywhere, as -name, --name, --name=value or --name value. "--" ends flags.
func parseArgs(args []string, spec flagSpec, alias map[string]string) (map[string]string, []string, error) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		val, hasVal := "", false
		if n, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = n, v, true
		}
		if full, ok := alias[name]; ok {
			name = full
		}
		takes, known := spec[name]
		if !known {
			return nil, nil, fmt.Errorf("unknown option %s", a)
		}
		if takes {
			if !hasVal {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("option %s needs a value", a)
				}
				i++
				val = args[i]
			}
		} else {
			if hasVal {
				return nil, nil, fmt.Errorf("option %s takes no value", a)
			}
			val = "true"
		}
		if _, dup := flags[name]; dup {
			return nil, nil, fmt.Errorf("option --%s given twice", name)
		}
		flags[name] = val
	}
	return flags, pos, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:])
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\nCancelled.")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "\nError:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "join":
		return cmdJoin(ctx, args[1:])
	case "order":
		return cmdOrder(args[1:])
	case "version", "--version", "-v":
		fmt.Println("vidjoin", version)
		return nil
	case "help", "-h", "--help", "/?":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (use join, order or help)", args[0])
}

func cmdOrder(args []string) error {
	flags, pos, err := parseArgs(args, flagSpec{"output": true, "y": false, "help": false},
		map[string]string{"o": "output", "h": "help"})
	if err != nil {
		return err
	}
	if flags["help"] != "" {
		fmt.Print(usage)
		return nil
	}
	if len(pos) != 1 {
		return errors.New("usage: vidjoin order FOLDER [-o order.txt]")
	}
	dir := pos[0]
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	out := flags["output"]
	if out == "" {
		out = filepath.Join(dir, "order.txt")
	}
	if _, err := os.Stat(out); err == nil && flags["y"] == "" {
		return fmt.Errorf("%s already exists; use -y to overwrite it", out)
	}
	files, err := order.ScanDir(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no video files found in %s", dir)
	}
	// Write paths relative to the list's folder, so the list can be moved
	// together with the videos.
	outDir, _ := filepath.Abs(filepath.Dir(out))
	names := make([]string, len(files))
	for i, f := range files {
		abs, _ := filepath.Abs(f)
		rel, err := filepath.Rel(outDir, abs)
		if err != nil {
			rel = abs
		}
		names[i] = filepath.ToSlash(rel)
	}
	if err := order.WriteList(out, names); err != nil {
		return err
	}
	fmt.Printf("Wrote %d videos to %s\n", len(names), out)
	fmt.Printf("Edit the order, then run:  vidjoin join --list %s\n", quoteArg(out))
	return nil
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t'\"&()") {
		return `"` + s + `"`
	}
	return s
}

func cmdJoin(ctx context.Context, args []string) error {
	flags, pos, err := parseArgs(args, flagSpec{
		"output": true, "list": true, "size": true, "fps": true, "bg": true,
		"preset": true, "dry-run": false, "y": false, "help": false,
	}, map[string]string{"o": "output", "h": "help", "n": "dry-run"})
	if err != nil {
		return err
	}
	if flags["help"] != "" {
		fmt.Print(usage)
		return nil
	}
	get := func(k, def string) string {
		if v, ok := flags[k]; ok {
			return v
		}
		return def
	}

	var s join.Settings
	if s.Size, err = opts.ParseSize(get("size", "1920x1080")); err != nil {
		return err
	}
	if f := strings.ToLower(get("fps", "auto")); f == "auto" {
		s.AutoFPS = true
	} else if s.FPS, err = opts.ParseFPS(f); err != nil {
		return err
	}
	if s.BG, err = opts.ParseColor(get("bg", "#000000")); err != nil {
		return err
	}
	if s.Preset, err = opts.ParsePreset(get("preset", "quality")); err != nil {
		return err
	}
	s.Output = get("output", "joined.mp4")
	switch strings.ToLower(filepath.Ext(s.Output)) {
	case ".mp4", ".m4v", ".mov", ".mkv":
	default:
		return fmt.Errorf("output %s: use a .mp4, .mov, .m4v or .mkv file name", s.Output)
	}
	if st, err := os.Stat(filepath.Dir(s.Output)); err != nil || !st.IsDir() {
		return fmt.Errorf("output folder %s does not exist", filepath.Dir(s.Output))
	}
	dryRun := flags["dry-run"] != ""

	inputs, err := resolveInputs(flags["list"], pos, s.Output)
	if err != nil {
		return err
	}
	if _, err := os.Stat(s.Output); err == nil && flags["y"] == "" && !dryRun {
		return fmt.Errorf("%s already exists; use -y to overwrite it", s.Output)
	}

	tools, err := ff.Locate()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Reading %d videos...\n", len(inputs))
	media, err := join.Probe(ctx, tools, inputs)
	if err != nil {
		return err
	}
	plan := join.NewPlan(media, s)
	s = plan.Settings

	fmt.Fprintln(os.Stderr, "Checking hardware encoders...")
	enc, err := ff.Detect(ctx, tools, s.Size, s.FPS)
	if err != nil {
		return err
	}

	printPlan(os.Stdout, plan, enc, tools)
	if dryRun {
		return nil
	}

	start := time.Now()
	rep := newReporter(os.Stderr)
	err = join.Execute(ctx, tools, plan, enc, rep)
	rep.finish()
	if err != nil {
		return err
	}

	// Check the result: both streams must match the planned length.
	if res, err := ff.Probe(ctx, tools, s.Output); err == nil {
		if diff := math.Abs(res.Duration - plan.Duration()); diff > 0.5 {
			fmt.Fprintf(os.Stderr, "Warning: output is %s long, expected %s\n",
				fmtDur(res.Duration), fmtDur(plan.Duration()))
		}
	}
	fmt.Printf("\nDone in %s: %s (%s)\n", fmtDur(time.Since(start).Seconds()), s.Output, fmtDur(plan.Duration()))
	return nil
}

func resolveInputs(list string, pos []string, output string) ([]string, error) {
	var inputs []string
	switch {
	case list != "":
		if len(pos) > 0 {
			return nil, errors.New("give either --list or input files, not both")
		}
		var err error
		if inputs, err = order.ReadList(list); err != nil {
			return nil, err
		}
	case len(pos) == 0:
		return nil, errors.New("no input videos given (see vidjoin help)")
	case len(pos) == 1:
		if st, err := os.Stat(pos[0]); err == nil && st.IsDir() {
			files, err := order.ScanDir(pos[0], output)
			if err != nil {
				return nil, err
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("no video files found in %s", pos[0])
			}
			inputs = files
		} else {
			inputs = pos
		}
	default:
		inputs = pos
	}
	outAbs, _ := filepath.Abs(output)
	var missing []string
	for _, in := range inputs {
		st, err := os.Stat(in)
		if err != nil {
			missing = append(missing, in)
			continue
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a folder; give a single folder, or files only", in)
		}
		if abs, _ := filepath.Abs(in); abs == outAbs {
			return nil, fmt.Errorf("%s is both an input and the output", in)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("file not found:\n  %s", strings.Join(missing, "\n  "))
	}
	return inputs, nil
}

func fmtDur(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(math.Round(sec * 10))
	h, m, s, d := t/36000, (t/600)%60, (t/10)%60, t%10
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%d", h, m, s, d)
	}
	return fmt.Sprintf("%d:%02d.%d", m, s, d)
}

func printPlan(w io.Writer, p *join.Plan, enc ff.Encoder, t ff.Tools) {
	s := p.Settings
	fpsNote := ""
	if s.AutoFPS {
		fpsNote = " (auto)"
	}
	fmt.Fprintf(w, "\nffmpeg:   %s\n", t.FFmpeg)
	fmt.Fprintf(w, "encoder:  %s [%s]\n", enc.Label, enc.Name)
	fmt.Fprintf(w, "output:   %s\n", s.Output)
	fmt.Fprintf(w, "format:   %s, %s fps%s, background %s, preset %s\n\n",
		s.Size, s.FPS.Pretty(), fpsNote, s.BG, s.Preset)
	fmt.Fprintf(w, "%4s  %-11s %6s  %-11s %9s  %-5s  %s\n", "#", "source", "fps", "placed as", "length", "audio", "file")
	for i, c := range p.Clips {
		m := c.Media
		audio := "yes"
		if m.AudioIndex < 0 {
			audio = "none"
		}
		fmt.Fprintf(w, "%4d  %-11s %6s  %-11s %9s  %-5s  %s\n", i+1,
			fmt.Sprintf("%dx%d", m.Width, m.Height),
			strings.TrimSuffix(fmt.Sprintf("%.2f", m.FPS), ".00"),
			fmt.Sprintf("%dx%d", c.PlacedW, c.PlacedH),
			fmtDur(float64(c.Frames)/s.FPS.Float()), audio, m.Path)
	}
	fmt.Fprintf(w, "\ntotal:    %d clips, %s\n\n", len(p.Clips), fmtDur(p.Duration()))
}

// reporter prints progress, redrawing one line on a terminal and printing
// occasional lines otherwise (e.g. when output goes to a log file).
type reporter struct {
	mu       sync.Mutex
	w        io.Writer
	tty      bool
	last     time.Time
	lastPct  int
	stage    string
	lineOpen bool
}

func newReporter(w *os.File) *reporter {
	st, err := w.Stat()
	return &reporter{w: w, tty: err == nil && st.Mode()&os.ModeCharDevice != 0, lastPct: -1}
}

func (r *reporter) line(stage string, pct float64, detail string, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stage != r.stage {
		if r.lineOpen {
			fmt.Fprintln(r.w)
			r.lineOpen = false
		}
		r.lastPct = -1
	}
	r.stage = stage
	p := int(pct)
	if r.tty {
		if !force && time.Since(r.last) < 200*time.Millisecond {
			return
		}
		r.last = time.Now()
		const width = 30
		n := int(pct / 100 * width)
		bar := strings.Repeat("#", n) + strings.Repeat("-", width-n)
		fmt.Fprintf(r.w, "\r%-9s [%s] %5.1f%%  %s   ", stage, bar, pct, detail)
		r.lineOpen = true
		return
	}
	if force || p/10 != r.lastPct/10 {
		fmt.Fprintf(r.w, "%s %3d%%  %s\n", stage, p, detail)
		r.lastPct = p
	}
}

func (r *reporter) Encoding(done, total int64, clipsDone, clips int) {
	pct := 0.0
	if total > 0 {
		pct = float64(done) * 100 / float64(total)
	}
	r.line("Encoding", pct, fmt.Sprintf("%d/%d clips", clipsDone, clips), clipsDone == clips)
}

func (r *reporter) Joining(done, total float64) {
	pct := 100.0
	if total > 0 {
		pct = done * 100 / total
	}
	r.line("Joining", pct, "", done >= total)
}

func (r *reporter) Warn(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lineOpen {
		fmt.Fprintln(r.w)
		r.lineOpen = false
	}
	fmt.Fprintln(r.w, "Warning:", msg)
	r.lastPct = -1
}

func (r *reporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lineOpen {
		fmt.Fprintln(r.w)
		r.lineOpen = false
	}
}
