package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/igoose1/vidjoin/internal/ff"
	"github.com/igoose1/vidjoin/internal/join"
	"github.com/igoose1/vidjoin/internal/opts"
	"github.com/igoose1/vidjoin/internal/order"
)

// clip is one entry of the list.
type clip struct {
	path  string
	media *ff.Media // nil until probed
	err   error     // probe failure
}

type ui struct {
	app      fyne.App
	win      fyne.Window
	tools    ff.Tools
	toolsErr error

	clips []*clip
	// removed holds, per folder, the file names the user took out of the
	// list, so they stay out when that folder is opened again.
	removed map[string]map[string]bool
	lastDir string

	list     *widget.List
	empty    fyne.CanvasObject
	summary  *widget.Label
	size     *widget.SelectEntry
	fps      *widget.Select
	bg       *widget.Entry
	swatch   *canvas.Rectangle
	preset   *widget.Select
	output   *widget.Entry
	progress *widget.ProgressBar
	status   *widget.Label
	joinBtn  *widget.Button
	stopBtn  *widget.Button
	showBtn  *widget.Button
	controls []fyne.Disableable

	running    bool
	quitAsked  bool // the "stop joining?" question is on screen
	cancel     context.CancelFunc
	done       chan struct{}
	confirmOut string // output path whose overwrite the user already confirmed
	probeSem   chan struct{}
}

const defaultOutput = "joined.mp4"

const (
	prefSize   = "size"
	prefFPS    = "fps"
	prefBG     = "bg"
	prefPreset = "preset"
)

func newUI(a fyne.App, w fyne.Window) *ui {
	u := &ui{app: a, win: w, removed: map[string]map[string]bool{}, probeSem: make(chan struct{}, 4)}
	u.tools, u.toolsErr = ff.Locate()
	w.SetContent(u.build())
	w.SetMainMenu(u.menu())
	w.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) { u.dropped(uris) })
	w.SetCloseIntercept(u.closeRequested)
	u.refresh()
	if u.toolsErr != nil {
		u.setStatus(u.toolsErr.Error())
	}
	return u
}

func (u *ui) build() fyne.CanvasObject {
	p := u.app.Preferences()

	addFolder := widget.NewButtonWithIcon("Add folder...", theme.FolderOpenIcon(), u.chooseFolder)
	addFile := widget.NewButtonWithIcon("Add file...", theme.FileVideoIcon(), u.chooseFile)
	loadList := widget.NewButtonWithIcon("Load list...", theme.FolderOpenIcon(), u.chooseList)
	saveList := widget.NewButtonWithIcon("Save list...", theme.DocumentSaveIcon(), u.saveList)
	sortBtn := widget.NewButton("Sort by name", u.sortByName)
	clear := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), u.clear)
	u.summary = widget.NewLabel("")
	toolbar := container.NewHBox(addFolder, addFile, loadList, saveList, layout.NewSpacer(), u.summary, sortBtn, clear)

	u.list = widget.NewList(
		func() int { return len(u.clips) },
		func() fyne.CanvasObject { return newRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) { u.updateRow(id, o.(*row)) },
	)
	u.list.OnSelected = func(id widget.ListItemID) { u.list.Unselect(id) }
	hint := widget.NewLabelWithStyle("Drop videos or folders here,\nor use Add folder... / Add file...",
		fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	hint.Importance = widget.LowImportance
	u.empty = container.NewCenter(hint)
	listArea := container.NewStack(u.list, u.empty)

	u.size = widget.NewSelectEntry([]string{"1280x720", "1920x1080", "2560x1440", "3840x2160"})
	u.size.SetText(p.StringWithFallback(prefSize, "1920x1080"))
	u.size.OnChanged = func(s string) { p.SetString(prefSize, s) }

	u.fps = widget.NewSelect([]string{"auto", "23.976", "24", "25", "29.97", "30", "50", "60"},
		func(s string) { p.SetString(prefFPS, s) })
	u.fps.SetSelected(p.StringWithFallback(prefFPS, "auto"))

	u.swatch = canvas.NewRectangle(color.Black)
	u.swatch.SetMinSize(fyne.NewSize(28, 28))
	u.swatch.StrokeWidth = 1
	u.swatch.StrokeColor = color.Gray{Y: 128}
	u.bg = widget.NewEntry()
	u.bg.OnChanged = func(s string) {
		if c, err := opts.ParseColor(s); err == nil {
			u.swatch.FillColor = hexColor(c)
			u.swatch.Refresh()
			p.SetString(prefBG, c)
		}
	}
	u.bg.SetText(p.StringWithFallback(prefBG, "#000000"))
	pick := widget.NewButtonWithIcon("", theme.ColorPaletteIcon(), u.chooseColor)

	u.preset = widget.NewSelect([]string{"quality", "balanced", "fast"}, func(s string) { p.SetString(prefPreset, s) })
	u.preset.SetSelected(p.StringWithFallback(prefPreset, "quality"))

	u.output = widget.NewEntry()
	u.output.SetPlaceHolder("Output file (.mp4, .mov, .m4v or .mkv)")
	browse := widget.NewButton("Browse...", u.chooseOutput)

	left := container.New(layout.NewFormLayout(),
		widget.NewLabel("Size"), u.size,
		widget.NewLabel("Background"), container.NewBorder(nil, nil, u.swatch, pick, u.bg),
	)
	right := container.New(layout.NewFormLayout(),
		widget.NewLabel("Frame rate"), u.fps,
		widget.NewLabel("Preset"), u.preset,
	)
	outRow := container.New(layout.NewFormLayout(),
		widget.NewLabel("Output"), container.NewBorder(nil, nil, nil, browse, u.output))
	settings := container.NewVBox(container.NewGridWithColumns(2, left, right), outRow)

	u.progress = widget.NewProgressBar()
	u.progress.Hide() // shown once joining starts
	u.status = widget.NewLabel("")
	u.status.Truncation = fyne.TextTruncateEllipsis
	u.joinBtn = widget.NewButtonWithIcon("Join", theme.MediaPlayIcon(), u.start)
	u.joinBtn.Importance = widget.HighImportance
	u.stopBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), u.stop)
	u.stopBtn.Hide()
	u.showBtn = widget.NewButtonWithIcon("Show in folder", theme.FolderOpenIcon(), u.showOutput)
	u.showBtn.Hide()
	bottom := container.NewVBox(
		u.progress,
		container.NewBorder(nil, nil, nil, container.NewHBox(u.showBtn, u.stopBtn, u.joinBtn), u.status),
	)

	u.controls = []fyne.Disableable{addFolder, addFile, loadList, saveList, sortBtn, clear,
		u.size, u.fps, u.bg, pick, u.preset, u.output, browse}

	return container.NewBorder(
		toolbar,
		container.NewVBox(widget.NewSeparator(), settings, widget.NewSeparator(), bottom),
		nil, nil,
		listArea,
	)
}

// menu builds the File menu. Its shortcuts work even while a text field
// has focus, because main menu shortcuts are checked first.
func (u *ui) menu() *fyne.MainMenu {
	quit := fyne.NewMenuItem("Quit", u.closeRequested)
	quit.IsQuit = true
	quit.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierShortcutDefault}
	return fyne.NewMainMenu(fyne.NewMenu("File",
		fyne.NewMenuItem("Add folder...", u.chooseFolder),
		fyne.NewMenuItem("Add file...", u.chooseFile),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Load list...", u.chooseList),
		fyne.NewMenuItem("Save list...", u.saveList),
		fyne.NewMenuItemSeparator(),
		quit,
	))
}

// ---- list rows -------------------------------------------------------

type row struct {
	widget.BaseWidget
	num, name, detail *widget.Label
	up, down, del     *widget.Button
	content           fyne.CanvasObject
}

func newRow() *row {
	r := &row{num: widget.NewLabel("000"), name: widget.NewLabel(""), detail: widget.NewLabel("")}
	r.num.Alignment = fyne.TextAlignTrailing
	r.name.TextStyle = fyne.TextStyle{Bold: true}
	r.name.Truncation = fyne.TextTruncateEllipsis
	r.detail.Truncation = fyne.TextTruncateEllipsis
	r.detail.SizeName = theme.SizeNameCaptionText
	r.up = widget.NewButtonWithIcon("", theme.MoveUpIcon(), nil)
	r.down = widget.NewButtonWithIcon("", theme.MoveDownIcon(), nil)
	r.del = widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	for _, b := range []*widget.Button{r.up, r.down, r.del} {
		b.Importance = widget.LowImportance
	}
	text := container.New(layout.NewCustomPaddedVBoxLayout(-8), r.name, r.detail)
	buttons := container.NewCenter(container.NewHBox(r.up, r.down, r.del))
	r.content = container.NewBorder(nil, nil, r.num, buttons, text)
	r.ExtendBaseWidget(r)
	return r
}

func (r *row) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(r.content) }

func (u *ui) updateRow(id widget.ListItemID, r *row) {
	if id >= len(u.clips) {
		return
	}
	c := u.clips[id]
	r.num.SetText(strconv.Itoa(id + 1))
	r.name.SetText(filepath.Base(c.path))
	switch {
	case c.err != nil:
		r.detail.Importance = widget.DangerImportance
		r.detail.SetText(firstLine(c.err.Error()))
	case c.media == nil:
		r.detail.Importance = widget.LowImportance
		if u.toolsErr != nil {
			r.detail.SetText(filepath.Dir(c.path))
		} else {
			r.detail.SetText("Reading...")
		}
	default:
		r.detail.Importance = widget.LowImportance
		r.detail.SetText(describe(c.media) + "   " + filepath.Dir(c.path))
	}
	r.up.OnTapped = func() { u.move(id, -1) }
	r.down.OnTapped = func() { u.move(id, +1) }
	r.del.OnTapped = func() { u.remove(id) }
	setEnabled(r.up, !u.running && id > 0)
	setEnabled(r.down, !u.running && id < len(u.clips)-1)
	setEnabled(r.del, !u.running)
}

func describe(m *ff.Media) string {
	parts := []string{fmt.Sprintf("%dx%d", m.Width, m.Height)}
	if m.FPS > 0 {
		parts = append(parts, strings.TrimSuffix(fmt.Sprintf("%.2f", m.FPS), ".00")+" fps")
	}
	parts = append(parts, fmtDur(m.Duration))
	if m.AudioIndex < 0 {
		parts = append(parts, "no audio")
	}
	return strings.Join(parts, ", ")
}

func setEnabled(d fyne.Disableable, on bool) {
	if on {
		d.Enable()
	} else {
		d.Disable()
	}
}

// ---- list changes ----------------------------------------------------

func (u *ui) move(i, delta int) {
	j := i + delta
	if u.running || i < 0 || j < 0 || i >= len(u.clips) || j >= len(u.clips) {
		return
	}
	u.clips[i], u.clips[j] = u.clips[j], u.clips[i]
	u.changed()
}

func (u *ui) remove(i int) {
	if u.running || i < 0 || i >= len(u.clips) {
		return
	}
	c := u.clips[i]
	u.clips = append(u.clips[:i], u.clips[i+1:]...)
	if !u.contains(c.path) {
		u.markRemoved(c.path, true)
	}
	u.changed()
}

// sortByName puts the list back in file name order, which also forgets
// the saved custom order.
func (u *ui) sortByName() {
	if u.running {
		return
	}
	for i := 1; i < len(u.clips); i++ {
		for j := i; j > 0 && order.NaturalLess(filepath.Base(u.clips[j].path), filepath.Base(u.clips[j-1].path)); j-- {
			u.clips[j], u.clips[j-1] = u.clips[j-1], u.clips[j]
		}
	}
	u.changed()
	u.setStatus("Sorted by file name")
}

func (u *ui) clear() {
	u.clips = nil
	u.removed = map[string]map[string]bool{}
	u.refresh()
	u.setStatus("")
}

func (u *ui) contains(path string) bool {
	for _, c := range u.clips {
		if samePath(c.path, path) {
			return true
		}
	}
	return false
}

func (u *ui) markRemoved(path string, on bool) {
	dir := folderKey(filepath.Dir(path))
	name := filepath.Base(path)
	if on {
		if u.removed[dir] == nil {
			u.removed[dir] = map[string]bool{}
		}
		u.removed[dir][name] = true
	} else if u.removed[dir] != nil {
		delete(u.removed[dir], name)
	}
}

// addFolder adds every video in dir, in the order saved for that folder
// if there is one, otherwise sorted by name.
func (u *ui) addFolder(dir string) {
	// Never pick up the output as an input: the chosen one, or the
	// default one this folder would get.
	out := u.output.Text
	if out == "" && len(u.clips) == 0 {
		out = filepath.Join(dir, defaultOutput)
	}
	files, err := order.ScanDir(dir, out)
	if err != nil {
		u.showError(err)
		return
	}
	if len(files) == 0 {
		u.setStatus("No videos found in " + dir)
		return
	}
	u.lastDir = dir
	msg := ""
	if cache, _ := order.LoadCache(dir); cache != nil {
		key := folderKey(dir)
		for _, n := range cache.Removed {
			if u.removed[key] == nil {
				u.removed[key] = map[string]bool{}
			}
			u.removed[key][n] = true
		}
		var restored, added int
		files, restored, added = cache.Arrange(files, true)
		if restored > 0 {
			msg = "Restored your saved order for " + filepath.Base(dir)
			if added > 0 {
				msg += fmt.Sprintf(" (%d new %s added at the end)", added, plural(added, "video", "videos"))
			}
			if len(cache.Removed) > 0 {
				msg += fmt.Sprintf("; %d removed earlier %s left out", len(cache.Removed), plural(len(cache.Removed), "video is", "videos are"))
			}
		}
	}
	n := u.add(files)
	if msg == "" {
		msg = fmt.Sprintf("Added %d %s from %s", n, plural(n, "video", "videos"), filepath.Base(dir))
	}
	u.setStatus(msg)
}

// addFiles adds individually chosen or dropped files. If they all come
// from a folder with a saved order, they are arranged by it.
func (u *ui) addFiles(files []string) {
	if len(files) == 0 {
		return
	}
	sortNatural(files)
	if dir, ok := commonDir(files); ok {
		if cache, _ := order.LoadCache(dir); cache != nil {
			files, _, _ = cache.Arrange(files, false)
		}
		u.lastDir = dir
	}
	for _, f := range files {
		u.markRemoved(f, false) // chosen again on purpose
	}
	n := u.add(files)
	u.setStatus(fmt.Sprintf("Added %d %s", n, plural(n, "video", "videos")))
}

// add appends files not already in the list and starts reading them.
func (u *ui) add(files []string) int {
	n := 0
	for _, f := range files {
		if abs, err := filepath.Abs(f); err == nil {
			f = abs
		}
		if u.contains(f) {
			continue
		}
		c := &clip{path: f}
		u.clips = append(u.clips, c)
		u.probe(c)
		n++
	}
	if u.output.Text == "" && len(u.clips) > 0 {
		u.output.SetText(filepath.Join(filepath.Dir(u.clips[0].path), defaultOutput))
	}
	u.changed()
	return n
}

func (u *ui) probe(c *clip) {
	if u.toolsErr != nil {
		return
	}
	go func() {
		u.probeSem <- struct{}{}
		m, err := ff.Probe(context.Background(), u.tools, c.path)
		<-u.probeSem
		fyne.Do(func() {
			c.media, c.err = m, err
			u.refresh()
		})
	}()
}

// changed is called after every edit of the list: it redraws and saves
// the order for the folder, if all clips come from one folder.
func (u *ui) changed() {
	u.refresh()
	u.saveOrder()
}

func (u *ui) saveOrder() {
	if len(u.clips) == 0 {
		return
	}
	paths := make([]string, len(u.clips))
	for i, c := range u.clips {
		paths[i] = c.path
	}
	dir, ok := commonDir(paths)
	if !ok {
		return // clips from several folders: nothing to remember per folder
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	var removed []string
	for n := range u.removed[folderKey(dir)] {
		removed = append(removed, n)
	}
	sortNatural(removed)
	sorted := append([]string(nil), names...)
	sortNatural(sorted)
	if equal(sorted, names) && len(removed) == 0 {
		// Plain name order: nothing custom to remember.
		_ = order.DeleteCache(dir)
		return
	}
	_ = order.SaveCache(&order.Cache{Folder: dir, Order: names, Removed: removed})
}

func (u *ui) refresh() {
	if len(u.clips) == 0 {
		u.empty.Show()
		u.summary.SetText("")
	} else {
		u.empty.Hide()
		var total float64
		known := true
		for _, c := range u.clips {
			if c.media == nil {
				known = false
				continue
			}
			total += c.media.Duration
		}
		s := fmt.Sprintf("%d %s", len(u.clips), plural(len(u.clips), "video", "videos"))
		if known {
			s += ", " + fmtDur(total)
		}
		u.summary.SetText(s)
	}
	u.list.Refresh()
	setEnabled(u.joinBtn, !u.running && len(u.clips) > 0 && u.toolsErr == nil)
}

// ---- dialogs -----------------------------------------------------------

func (u *ui) location() fyne.ListableURI {
	if u.lastDir == "" {
		return nil
	}
	l, err := storage.ListerForURI(storage.NewFileURI(u.lastDir))
	if err != nil {
		return nil
	}
	return l
}

func (u *ui) chooseFolder() {
	if u.running {
		return
	}
	d := dialog.NewFolderOpen(func(l fyne.ListableURI, err error) {
		if err != nil {
			u.showError(err)
			return
		}
		if l != nil {
			u.addFolder(uriPath(l))
		}
	}, u.win)
	d.SetLocation(u.location())
	d.Show()
	d.Resize(u.dialogSize()) // only valid after Show
}

func (u *ui) chooseFile() {
	if u.running {
		return
	}
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			u.showError(err)
			return
		}
		if r == nil {
			return
		}
		r.Close()
		u.addFiles([]string{uriPath(r.URI())})
	}, u.win)
	d.SetFilter(storage.NewExtensionFileFilter(videoExtensions()))
	d.SetLocation(u.location())
	d.Show()
	d.Resize(u.dialogSize()) // only valid after Show
}

func (u *ui) chooseList() {
	if u.running {
		return
	}
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			u.showError(err)
			return
		}
		if r == nil {
			return
		}
		r.Close()
		path := uriPath(r.URI())
		files, err := order.ReadList(path)
		if err != nil {
			u.showError(err)
			return
		}
		u.clips = nil
		u.lastDir = filepath.Dir(path)
		for _, f := range files {
			u.markRemoved(f, false)
		}
		n := u.add(files)
		u.setStatus(fmt.Sprintf("Loaded %d %s from %s", n, plural(n, "video", "videos"), filepath.Base(path)))
	}, u.win)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".txt"}))
	d.SetLocation(u.location())
	d.Show()
	d.Resize(u.dialogSize()) // only valid after Show
}

func (u *ui) saveList() {
	if u.running {
		return
	}
	if len(u.clips) == 0 {
		u.setStatus("The list is empty")
		return
	}
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			u.showError(err)
			return
		}
		if w == nil {
			return
		}
		w.Close()
		path := uriPath(w.URI())
		if err := order.WriteList(path, u.relativeTo(filepath.Dir(path))); err != nil {
			u.showError(err)
			return
		}
		u.setStatus("Saved the list to " + path)
	}, u.win)
	d.SetFileName("order.txt")
	d.SetLocation(u.location())
	d.Show()
	d.Resize(u.dialogSize()) // only valid after Show
}

// relativeTo returns the clip paths relative to dir where possible, so a
// list file keeps working when moved together with the videos.
func (u *ui) relativeTo(dir string) []string {
	out := make([]string, len(u.clips))
	for i, c := range u.clips {
		rel, err := filepath.Rel(dir, c.path)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = c.path
		}
		out[i] = filepath.ToSlash(rel)
	}
	return out
}

func (u *ui) chooseOutput() {
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			u.showError(err)
			return
		}
		if w == nil {
			return
		}
		// The dialog has just created (or emptied, after asking) this file.
		// Remove it again; it is written when joining finishes.
		w.Close()
		path := uriPath(w.URI())
		if st, err := os.Stat(path); err == nil && st.Size() == 0 {
			os.Remove(path)
		}
		if filepath.Ext(path) == "" {
			path += ".mp4"
		}
		u.output.SetText(path)
		u.confirmOut = path
	}, u.win)
	name := defaultOutput
	if u.output.Text != "" {
		name = filepath.Base(u.output.Text)
		if l, err := storage.ListerForURI(storage.NewFileURI(filepath.Dir(u.output.Text))); err == nil {
			d.SetLocation(l)
		}
	} else {
		d.SetLocation(u.location())
	}
	d.SetFileName(name)
	d.Show()
	d.Resize(u.dialogSize()) // only valid after Show
}

func (u *ui) chooseColor() {
	d := dialog.NewColorPicker("Background", "Color of the bars around the picture", func(c color.Color) {
		r, g, b, _ := c.RGBA()
		u.bg.SetText(fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8))
	}, u.win)
	d.Advanced = true
	if c, err := opts.ParseColor(u.bg.Text); err == nil {
		d.SetColor(hexColor(c))
	}
	d.Show()
}

func (u *ui) dialogSize() fyne.Size {
	s := u.win.Canvas().Size()
	return fyne.NewSize(float32(math.Max(600, float64(s.Width)*0.9)), float32(math.Max(420, float64(s.Height)*0.9)))
}

func (u *ui) dropped(uris []fyne.URI) {
	paths := make([]string, len(uris))
	for i, uri := range uris {
		paths[i] = uriPath(uri)
	}
	u.open(paths)
}

// open adds folders and video files; anything else is ignored.
func (u *ui) open(paths []string) {
	if u.running {
		return
	}
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		switch {
		case err != nil:
		case st.IsDir():
			u.addFolder(p)
		case order.IsVideo(p):
			files = append(files, p)
		}
	}
	u.addFiles(files)
}

// ---- joining ---------------------------------------------------------

func (u *ui) settings() (join.Settings, error) {
	var s join.Settings
	var err error
	if s.Size, err = opts.ParseSize(u.size.Text); err != nil {
		return s, err
	}
	if u.fps.Selected == "auto" || u.fps.Selected == "" {
		s.AutoFPS = true
	} else if s.FPS, err = opts.ParseFPS(u.fps.Selected); err != nil {
		return s, err
	}
	if s.BG, err = opts.ParseColor(u.bg.Text); err != nil {
		return s, err
	}
	if s.Preset, err = opts.ParsePreset(u.preset.Selected); err != nil {
		return s, err
	}
	s.Output = strings.TrimSpace(u.output.Text)
	if s.Output == "" {
		return s, errors.New("choose an output file")
	}
	if abs, err := filepath.Abs(s.Output); err == nil {
		s.Output = abs
	}
	switch strings.ToLower(filepath.Ext(s.Output)) {
	case ".mp4", ".m4v", ".mov", ".mkv":
	default:
		return s, errors.New("the output file must end in .mp4, .mov, .m4v or .mkv")
	}
	if st, err := os.Stat(filepath.Dir(s.Output)); err != nil || !st.IsDir() {
		return s, fmt.Errorf("the output folder %s does not exist", filepath.Dir(s.Output))
	}
	for _, c := range u.clips {
		if samePath(c.path, s.Output) {
			return s, errors.New("the output file is also one of the input videos")
		}
	}
	return s, nil
}

func (u *ui) start() {
	if u.running || len(u.clips) == 0 {
		return
	}
	s, err := u.settings()
	if err != nil {
		u.showError(err)
		return
	}
	media := make([]*ff.Media, len(u.clips))
	for i, c := range u.clips {
		switch {
		case c.err != nil:
			u.showError(fmt.Errorf("%s can't be read. Remove it from the list (it is marked red) and try again", filepath.Base(c.path)))
			return
		case c.media == nil:
			u.setStatus("Still reading the videos, try again in a moment")
			return
		}
		media[i] = c.media
	}
	if _, err := os.Stat(s.Output); err == nil && !samePath(u.confirmOut, s.Output) {
		dialog.ShowConfirm("Replace file?", filepath.Base(s.Output)+" already exists. Replace it?", func(ok bool) {
			if ok {
				u.confirmOut = s.Output
				u.run(s, media)
			}
		}, u.win)
		return
	}
	u.run(s, media)
}

func (u *ui) run(s join.Settings, media []*ff.Media) {
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	u.done = make(chan struct{})
	u.setRunning(true)
	u.progress.SetValue(0)
	u.progress.Show()
	u.setStatus("Checking hardware encoders...")
	started := time.Now()
	go func() {
		defer close(u.done)
		plan := join.NewPlan(media, s)
		enc, err := ff.Detect(ctx, u.tools, plan.Settings.Size, plan.Settings.FPS)
		if err == nil {
			rep := &reporter{ui: u, encoder: enc.Label}
			err = join.Execute(ctx, u.tools, plan, enc, rep)
		}
		fyne.Do(func() { u.finished(err, plan, started) })
	}()
}

func (u *ui) finished(err error, plan *join.Plan, started time.Time) {
	u.cancel()
	u.setRunning(false)
	switch {
	case errors.Is(err, context.Canceled):
		u.progress.Hide()
		u.setStatus("Cancelled")
	case err != nil:
		u.progress.Hide()
		u.setStatus("Failed")
		u.showError(err)
	default:
		u.progress.SetValue(1)
		u.confirmOut = ""
		u.setStatus(fmt.Sprintf("Done in %s: %s (%s)", fmtDur(time.Since(started).Seconds()),
			filepath.Base(plan.Settings.Output), fmtDur(plan.Duration())))
		u.showBtn.Show()
	}
}

func (u *ui) stop() {
	if u.cancel != nil {
		u.cancel()
		u.setStatus("Cancelling...")
		u.stopBtn.Disable()
	}
}

func (u *ui) setRunning(on bool) {
	u.running = on
	for _, c := range u.controls {
		setEnabled(c, !on)
	}
	if on {
		u.joinBtn.Hide()
		u.showBtn.Hide()
		u.stopBtn.Enable()
		u.stopBtn.Show()
	} else {
		u.stopBtn.Hide()
		u.joinBtn.Show()
	}
	u.refresh()
}

// closeRequested handles the window's close button, the window manager's
// close request and File > Quit (Ctrl+Q).
func (u *ui) closeRequested() {
	if !u.running {
		u.app.Quit()
		return
	}
	if u.quitAsked {
		return
	}
	u.quitAsked = true
	dialog.ShowConfirm("Stop joining?", "Joining is still running. Stop it and quit?", func(ok bool) {
		u.quitAsked = false
		if !ok {
			return
		}
		u.stop()
		done := u.done
		go func() {
			<-done // let ffmpeg stop and temporary files be removed
			fyne.Do(u.app.Quit)
		}()
	}, u.win)
}

func (u *ui) showOutput() {
	dir := filepath.Dir(strings.TrimSpace(u.output.Text))
	if uri, err := url.Parse(storage.NewFileURI(dir).String()); err == nil {
		_ = u.app.OpenURL(uri)
	}
}

func (u *ui) setStatus(s string) { u.status.SetText(s) }

func (u *ui) showError(err error) {
	msg := err.Error()
	if len(msg) > 1500 {
		msg = msg[:1500] + "..."
	}
	dialog.ShowError(errors.New(msg), u.win)
}

// reporter shows join progress. Encoding is 95% of the bar, joining 5%.
type reporter struct {
	ui      *ui
	encoder string
	mu      sync.Mutex
	last    time.Time
}

func (r *reporter) show(frac float64, text string, force bool) {
	r.mu.Lock()
	if !force && time.Since(r.last) < 100*time.Millisecond {
		r.mu.Unlock()
		return
	}
	r.last = time.Now()
	r.mu.Unlock()
	fyne.Do(func() {
		if !r.ui.running {
			return
		}
		r.ui.progress.SetValue(frac)
		if r.ui.stopBtn.Disabled() {
			return // keep showing "Cancelling..."
		}
		r.ui.setStatus(text)
	})
}

func (r *reporter) Encoding(done, total int64, clipsDone, clips int) {
	f := 0.0
	if total > 0 {
		f = float64(done) / float64(total)
	}
	r.show(0.95*f, fmt.Sprintf("Encoding with %s: %d of %d clips done", r.encoder, clipsDone, clips), clipsDone == clips)
}

func (r *reporter) Joining(done, total float64) {
	f := 1.0
	if total > 0 {
		f = done / total
	}
	r.show(0.95+0.05*f, "Joining...", done >= total)
}

func (r *reporter) Warn(msg string) {
	fyne.Do(func() { r.ui.setStatus("Warning: " + firstLine(msg)) })
}

// ---- helpers ---------------------------------------------------------

func uriPath(u fyne.URI) string { return filepath.FromSlash(u.Path()) }

func folderKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if isWindows {
		return strings.ToLower(abs)
	}
	return abs
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	if isWindows {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func commonDir(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	dir := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		if !samePath(filepath.Dir(p), dir) {
			return "", false
		}
	}
	return dir, true
}

func sortNatural(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && order.NaturalLess(filepath.Base(s[j]), filepath.Base(s[j-1])); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func videoExtensions() []string {
	exts := []string{".mp4", ".m4v", ".mov", ".mkv", ".webm", ".avi", ".wmv", ".flv", ".mts", ".m2ts",
		".ts", ".3gp", ".mpg", ".mpeg", ".vob", ".ogv", ".mxf", ".dv"}
	out := append([]string(nil), exts...)
	for _, e := range exts {
		out = append(out, strings.ToUpper(e))
	}
	return out
}

func hexColor(s string) color.Color {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
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
