package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// apply mounts the disk image, copies the new app next to the running one,
// and leaves a small script to swap them once this process has exited.
func apply(dmg string, quit func()) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe))) // …/Clipferry.app/Contents/MacOS/Clipferry
	if !strings.HasSuffix(bundle, ".app") {
		return errors.New("update: Clipferry is not running from an app bundle; download the new version from the website")
	}

	mnt, err := os.MkdirTemp("", "clipferry-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mnt)
	if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg).CombinedOutput(); err != nil {
		return fmt.Errorf("update: open disk image: %v: %s", err, out)
	}
	defer exec.Command("hdiutil", "detach", "-quiet", mnt).Run()

	staged := bundle + ".new"
	os.RemoveAll(staged)
	if out, err := exec.Command("ditto", filepath.Join(mnt, "Clipferry.app"), staged).CombinedOutput(); err != nil {
		os.RemoveAll(staged)
		if strings.Contains(string(out), "ermission") {
			return fmt.Errorf("update: no permission to replace %s — install the new version from the website", bundle)
		}
		return fmt.Errorf("update: copy new version: %v: %s", err, out)
	}

	script := `pid="$1"; app="$2"
while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done
rm -rf "$app.old"
mv "$app" "$app.old" && mv "$app.new" "$app" && rm -rf "$app.old"
open "$app"`
	cmd := exec.Command("/bin/sh", "-c", script, "clipferry-update", strconv.Itoa(os.Getpid()), bundle)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(staged)
		return err
	}
	quit()
	return nil
}
