package update

import (
	"os/exec"
	"syscall"
)

// apply runs the installer silently. It closes any running Clipferry,
// installs the new version and starts it again.
func apply(setup string, quit func()) error {
	cmd := exec.Command(setup, "/S")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008} // DETACHED_PROCESS
	if err := cmd.Start(); err != nil {
		return err
	}
	quit()
	return nil
}
