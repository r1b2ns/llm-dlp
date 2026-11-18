package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurivenancio30/llm-dlp/internal/config"
)

func TestProtectConfigCopiesTheUserConfig(t *testing.T) {
	src := filepath.Join(t.TempDir(), "config.json")
	dst := filepath.Join(t.TempDir(), "etc", "config.json")
	body := `{"upstream":"https://api.anthropic.com","internal_domains":["empresa.intra"]}`
	os.WriteFile(src, []byte(body), 0o600)
	created, cfg, err := protectConfig(src, dst)
	if err != nil || !created {
		t.Fatalf("created %v, err %v", created, err)
	}
	if len(cfg.InternalDomains) != 1 || cfg.InternalDomains[0] != "empresa.intra" {
		t.Errorf("config read back: %+v", cfg.InternalDomains)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != body {
		t.Errorf("protected copy: %s", b)
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("protected copy mode %v, want 0644 (the proxy reads it as the user)", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(dst))
	if di.Mode().Perm() != 0o755 {
		t.Errorf("folder mode %v", di.Mode().Perm())
	}
}

func TestProtectConfigKeepsAnExistingProtectedConfig(t *testing.T) {
	src := filepath.Join(t.TempDir(), "config.json")
	dst := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(dst, []byte(`{"upstream":"https://api.anthropic.com"}`), 0o644)
	os.WriteFile(src, []byte(`{"upstream":"https://attacker.invalid"}`), 0o600)
	created, cfg, err := protectConfig(src, dst)
	if err != nil || created {
		t.Fatalf("created %v, err %v", created, err)
	}
	if cfg.Upstream != "https://api.anthropic.com" {
		t.Errorf("upstream %q", cfg.Upstream)
	}
	if b, _ := os.ReadFile(dst); !strings.Contains(string(b), "api.anthropic.com") {
		t.Errorf("protected config replaced by the user one: %s", b)
	}
}

func TestProtectConfigWithoutUserConfigWritesTheDefault(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "config.json")
	created, cfg, err := protectConfig(filepath.Join(t.TempDir(), "missing.json"), dst)
	if err != nil || !created || !cfg.FailClosed {
		t.Fatalf("created %v, fail_closed %v, err %v", created, cfg.FailClosed, err)
	}
	if b, _ := os.ReadFile(dst); !strings.Contains(string(b), `"fail_closed": true`) {
		t.Errorf("default not written: %s", b)
	}
}

func TestProtectConfigRefusesAnInvalidUserConfig(t *testing.T) {
	src := filepath.Join(t.TempDir(), "config.json")
	dst := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(src, []byte(`{not json`), 0o600)
	if _, _, err := protectConfig(src, dst); err == nil {
		t.Fatal("invalid config accepted")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("protected config written from an invalid one: %v", err)
	}
}

func TestReleaseConfigMovesTheProtectedOneBack(t *testing.T) {
	sys := filepath.Join(t.TempDir(), "config.json")
	dst := filepath.Join(t.TempDir(), "llm-dlp", "config.json")
	os.MkdirAll(filepath.Dir(dst), 0o700)
	os.WriteFile(sys, []byte(`{"terms":["edited with sudoedit"]}`), 0o644)
	os.WriteFile(dst, []byte(`{"old":true}`), 0o600)
	bak, err := releaseConfig(sys, dst, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != `{"terms":["edited with sudoedit"]}` {
		t.Errorf("user config after release: %s", b)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Errorf("user config mode %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(sys); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("protected config still in place: %v", err)
	}
	if b, _ := os.ReadFile(bak); !strings.Contains(string(b), "sudoedit") {
		t.Errorf("protected backup %q: %s", bak, b)
	}
	olds, _ := filepath.Glob(dst + ".bak-*")
	if len(olds) != 1 {
		t.Fatalf("backups of the previous user config: %v", olds)
	}
	if b, _ := os.ReadFile(olds[0]); string(b) != `{"old":true}` {
		t.Errorf("previous user config backup: %s", b)
	}
}

func TestReleaseConfigWithoutProtectedConfig(t *testing.T) {
	bak, err := releaseConfig(filepath.Join(t.TempDir(), "config.json"), filepath.Join(t.TempDir(), "config.json"), os.Getuid(), os.Getgid())
	if err != nil || bak != "" {
		t.Errorf("bak %q, err %v", bak, err)
	}
}

func TestConfigLockWarningWhenTheConfigIsNotProtected(t *testing.T) {
	t.Setenv("LLM_DLP_HOME", t.TempDir())
	before, beforeDir := lockFile, config.SystemDir
	defer func() { lockFile, config.SystemDir = before, beforeDir }()
	config.SystemDir = t.TempDir()
	lockFile = filepath.Join(t.TempDir(), "managed-settings.json")
	if w := configLockWarning(); w != "" {
		t.Errorf("no lock: %q", w)
	}
	os.WriteFile(lockFile, []byte(`{}`), 0o644)
	if w := configLockWarning(); !strings.Contains(w, "sudo llm-dlp install-lock") {
		t.Errorf("lock without protected config: %q", w)
	}
}
