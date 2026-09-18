//go:build !windows

package ff

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
