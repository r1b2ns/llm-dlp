package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSystemDir(t *testing.T, root bool) string {
	t.Helper()
	t.Setenv("LLM_DLP_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "etc-llm-dlp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir, oldOwner := SystemDir, ownedByRoot
	SystemDir = dir
	ownedByRoot = func(os.FileInfo) bool { return root }
	t.Cleanup(func() { SystemDir, ownedByRoot = oldDir, oldOwner })
	return dir
}

func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path("config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithoutSystemConfigReadsTheUserOne(t *testing.T) {
	withSystemDir(t, true)
	writeUserConfig(t, `{"upstream":"https://user.example"}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://user.example" {
		t.Errorf("upstream %q", c.Upstream)
	}
	if ActivePath() != Path("config.json") {
		t.Errorf("active path %q", ActivePath())
	}
}

func TestLoadPrefersTheProtectedConfig(t *testing.T) {
	dir := withSystemDir(t, true)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"upstream":"https://api.anthropic.com","fail_closed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserConfig(t, `{"upstream":"https://attacker.invalid","fail_closed":false}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://api.anthropic.com" || !c.FailClosed {
		t.Errorf("the user config won over the protected one: upstream %q, fail_closed %v", c.Upstream, c.FailClosed)
	}
	if ActivePath() != SystemPath() {
		t.Errorf("active path %q", ActivePath())
	}
}

func TestLoadWithProtectedConfigDoesNotWriteTheUserOne(t *testing.T) {
	dir := withSystemDir(t, true)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path("config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load wrote %s while the config is protected (%v)", Path("config.json"), err)
	}
}

func TestLoadRefusesAProtectedConfigOthersCanChange(t *testing.T) {
	cases := []struct {
		name      string
		root      bool
		fileMode  os.FileMode
		dirMode   os.FileMode
		wantInErr string
	}{
		{"not owned by root", false, 0o644, 0o755, "must belong to root"},
		{"world writable file", true, 0o666, 0o755, "config.json must belong to root"},
		{"group writable folder", true, 0o644, 0o775, "etc-llm-dlp must belong to root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := withSystemDir(t, tc.root)
			p := filepath.Join(dir, "config.json")
			if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, tc.fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.dirMode); err != nil {
				t.Fatal(err)
			}
			writeUserConfig(t, `{}`)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("Load error %v, want one containing %q", err, tc.wantInErr)
			}
		})
	}
}

func TestLoadRefusesASymlinkAsProtectedConfig(t *testing.T) {
	dir := withSystemDir(t, true)
	writeUserConfig(t, `{"upstream":"https://attacker.invalid"}`)
	if err := os.Symlink(Path("config.json"), filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("Load error %v", err)
	}
}

func TestWritesRespectTheProtectedConfig(t *testing.T) {
	dir := withSystemDir(t, true)
	sys := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sys, []byte(`{"upstream":"https://api.anthropic.com"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserConfig(t, `{"campos_extras":[]}`)
	if _, err := SetFields(map[string]any{"upstream": "https://attacker.invalid"}); err == nil || !strings.Contains(err.Error(), "sudoedit") {
		t.Errorf("SetFields error %v", err)
	}
	if bak, err := MigrateFile(false, false); err != nil || bak != "" {
		t.Errorf("MigrateFile: %q %v", bak, err)
	}
	if b, _ := os.ReadFile(sys); string(b) != `{"upstream":"https://api.anthropic.com"}` {
		t.Errorf("protected config changed: %s", b)
	}
	if b, _ := os.ReadFile(Path("config.json")); string(b) != `{"campos_extras":[]}` {
		t.Errorf("user config changed: %s", b)
	}
}
