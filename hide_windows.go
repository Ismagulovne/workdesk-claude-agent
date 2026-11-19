//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow: без этого на Windows при каждом сообщении мигает консольное окно claude.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
