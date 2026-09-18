//go:build windows

package ff

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps ffmpeg from opening its own console window. vidjoin
// stops ffmpeg itself on Ctrl+C, via the command's context.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
