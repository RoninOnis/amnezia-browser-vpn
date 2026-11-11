//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

// spawnDetached starts `awgbv --daemon` with no inherited handles: the
// native-messaging host's stdout belongs to the browser, so the daemon must not
// keep a copy of it.
func spawnDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetachedAt(exe)
}

// spawnDetachedAt starts the daemon from an explicit path. Needed by the
// self-updater: after the binary is swapped, os.Executable() still points at
// the old, renamed file, so the daemon would be started from the old build.
func spawnDetachedAt(exe string) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "--daemon")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow,
		HideWindow:    true,
	}
	return cmd.Start()
}

// tryLock uses an exclusive-create lock file; on Windows the OS releases the
// handle when the process dies, so a stale file is removed on the next start.
func tryLock(path string) (func(), bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		// Stale lock from a killed daemon: drop it and retry once.
		if resp, rerr := call(request{Cmd: "ping"}); rerr == nil && resp != nil && resp.OK {
			return func() {}, false
		}
		_ = os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return func() {}, false
		}
	}
	return func() {
		f.Close()
		_ = os.Remove(path)
	}, true
}
