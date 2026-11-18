package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yurivenancio30/llm-dlp/internal/config"
)

func sudoUser() (*user.User, error) {
	u := os.Getenv("SUDO_USER")
	if u == "" {
		return nil, errors.New("SUDO_USER is not set")
	}
	return user.Lookup(u)
}

func userConfigPath(u *user.User) string {
	return filepath.Join(u.HomeDir, ".config", "llm-dlp", "config.json")
}

func protectConfig(src, dst string) (bool, config.Config, error) {
	cfg := config.Default()
	if b, err := os.ReadFile(dst); err == nil {
		return false, cfg, config.Read(b, &cfg)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, cfg, err
	}
	b, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		b, err = json.MarshalIndent(cfg, "", "  ")
		b = append(b, '\n')
	}
	if err != nil {
		return false, cfg, err
	}
	if err := config.Read(b, &cfg); err != nil {
		return false, cfg, fmt.Errorf("%s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, cfg, err
	}
	if err := os.Chmod(filepath.Dir(dst), 0o755); err != nil {
		return false, cfg, err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false, cfg, err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return false, cfg, err
	}
	return true, cfg, os.Rename(tmp, dst)
}

func releaseConfig(sys, dst string, uid, gid int) (string, error) {
	b, err := os.ReadFile(sys)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	if dst != "" {
		dir := filepath.Dir(dst)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return "", err
		}
		if old, err := os.ReadFile(dst); err == nil && !bytes.Equal(old, b) {
			bak := dst + ".bak-" + stamp
			if err := os.WriteFile(bak, old, 0o600); err != nil {
				return "", err
			}
			if err := os.Chown(bak, uid, gid); err != nil {
				return "", err
			}
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return "", err
		}
		if err := os.Chown(tmp, uid, gid); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return "", err
		}
	}
	bak := sys + ".bak-" + stamp
	return bak, os.Rename(sys, bak)
}

func configLockWarning() string {
	protected, err := config.Protected()
	if err != nil {
		return "protected configuration unusable: " + err.Error()
	}
	if _, lerr := os.Stat(lockFile); lerr == nil && !protected {
		return "the lock is installed but " + config.Path("config.json") +
			" can still be changed by any program running as you: run sudo llm-dlp install-lock"
	}
	return ""
}

func lockConfig(remove bool) error {
	u, uerr := sudoUser()
	if remove {
		dst, uid, gid := "", -1, -1
		if uerr == nil {
			dst = userConfigPath(u)
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
		}
		bak, err := releaseConfig(config.SystemPath(), dst, uid, gid)
		if err != nil || bak == "" {
			return err
		}
		if dst != "" {
			fmt.Printf("✓ configuration moved back to %s (the protected one kept at %s)\n", dst, bak)
		} else {
			fmt.Printf("✓ protected configuration removed (kept at %s); %s applies again\n", bak, config.Path("config.json"))
		}
		return nil
	}
	src := config.Path("config.json")
	if uerr == nil {
		src = userConfigPath(u)
	}
	created, cfg, err := protectConfig(src, config.SystemPath())
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("✓ configuration protected at %s (copied from %s): only root changes it\n", config.SystemPath(), src)
	} else {
		fmt.Printf("✓ configuration already protected at %s (kept as it is)\n", config.SystemPath())
	}
	fmt.Printf("  To change it: sudoedit %s and then llm-dlp reload\n", config.SystemPath())
	for _, f := range reducedProtection(cfg) {
		fmt.Println("⚠️  check it, protection reduced by this configuration: " + f)
	}
	return nil
}
