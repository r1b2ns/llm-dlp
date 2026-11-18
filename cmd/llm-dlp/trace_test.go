package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurivenancio30/llm-dlp/internal/proxy"
)

func TestTraceCommand(t *testing.T) {
	dir := t.TempDir()
	tr := proxy.NewTracer(filepath.Join(dir, "trace.json"), filepath.Join(dir, "trace.jsonl"))
	if _, on := tr.Until(); on {
		t.Fatal("trace on before enabling")
	}
	if err := traceCommand(tr, []string{"on", "10m"}); err != nil {
		t.Fatal(err)
	}
	if _, on := tr.Until(); !on {
		t.Error("trace off after on")
	}
	if err := traceCommand(tr, nil); err != nil {
		t.Fatal(err)
	}
	if err := traceCommand(tr, []string{"off"}); err != nil {
		t.Fatal(err)
	}
	if _, on := tr.Until(); on {
		t.Error("trace on after off")
	}
	for _, args := range [][]string{{"on", "abc"}, {"on", "-5m"}, {"on", "25h"}, {"maybe"}} {
		if err := traceCommand(tr, args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if err := traceCommand(tr, []string{"on", "25h"}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("limit error: %v", err)
	}
}
