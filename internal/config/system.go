package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var SystemDir = "/etc/llm-dlp"

var ownedByRoot = func(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

func SystemPath() string { return filepath.Join(SystemDir, "config.json") }

func ErrProtected() error {
	return fmt.Errorf("the configuration is protected by the lock (%s): change it with sudoedit %s and then run llm-dlp reload",
		SystemPath(), SystemPath())
}

func Protected() (bool, error) {
	p := SystemPath()
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !fi.Mode().IsRegular() {
		return true, fmt.Errorf("%s is not a regular file", p)
	}
	if err := writableOnlyByRoot(fi, p); err != nil {
		return true, err
	}
	di, err := os.Stat(SystemDir)
	if err != nil {
		return true, err
	}
	return true, writableOnlyByRoot(di, SystemDir)
}

func writableOnlyByRoot(fi os.FileInfo, p string) error {
	if !ownedByRoot(fi) || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must belong to root and be writable only by root (sudo chown root %s && sudo chmod go-w %s)", p, p, p)
	}
	return nil
}

func ActivePath() string {
	if ok, _ := Protected(); ok {
		return SystemPath()
	}
	return Path("config.json")
}
