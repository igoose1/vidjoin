// Command vidjoin-gui is the graphical version of vidjoin.
package main

import (
	"os"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

var version = "dev"

const isWindows = runtime.GOOS == "windows"

func main() {
	a := app.NewWithID("io.github.igoose1.vidjoin")
	w := a.NewWindow("vidjoin " + version)
	w.Resize(fyne.NewSize(960, 680))
	u := newUI(a, w)
	// Files and folders given as arguments (e.g. dropped onto the program's
	// icon, or "Open with") are added right away.
	u.open(os.Args[1:])
	w.ShowAndRun()
}
