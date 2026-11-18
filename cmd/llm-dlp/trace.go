package main

import (
	"fmt"
	"os"
	"time"

	"github.com/yurivenancio30/llm-dlp/internal/config"
	"github.com/yurivenancio30/llm-dlp/internal/proxy"
)

const (
	defaultTraceDuration = 30 * time.Minute
	maxTraceDuration     = 24 * time.Hour
)

func newTracer() (*proxy.Tracer, error) {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return nil, err
	}
	return proxy.NewTracer(config.Path("trace.json"), config.Path("trace.jsonl")), nil
}

func trace(args []string) error {
	t, err := newTracer()
	if err != nil {
		return err
	}
	return traceCommand(t, args)
}

func traceCommand(t *proxy.Tracer, args []string) error {
	if len(args) == 0 {
		if until, on := t.Until(); on {
			fmt.Printf("trace on until %s: %s\n", until.Local().Format("15:04"), t.File())
		} else {
			fmt.Println("trace off")
		}
		if fi, err := os.Stat(t.File()); err == nil {
			fmt.Printf("%s: %.1f MB\n", t.File(), float64(fi.Size())/(1<<20))
		}
		return nil
	}
	switch args[0] {
	case "on":
		d := defaultTraceDuration
		if len(args) > 1 {
			v, err := time.ParseDuration(args[1])
			if err != nil || v <= 0 {
				return fmt.Errorf("invalid duration %q (examples: 30m, 2h)", args[1])
			}
			d = v
		}
		if d > maxTraceDuration {
			return fmt.Errorf("at most %s", maxTraceDuration)
		}
		until, err := t.Enable(d)
		if err != nil {
			return err
		}
		fmt.Printf("✓ trace on until %s, in %s\n", until.Local().Format("15:04"), t.File())
		fmt.Println("  One JSON line per request: what went to the API (masked) and what the API answered (before the")
		fmt.Println("  pseudonyms are undone). Credentials are removed. It stops by itself; llm-dlp trace off stops now.")
		return nil
	case "off":
		if err := t.Disable(); err != nil {
			return err
		}
		fmt.Printf("✓ trace off (%s kept; delete it when done)\n", t.File())
		return nil
	}
	return fmt.Errorf("usage: llm-dlp trace [on [30m] | off]")
}
