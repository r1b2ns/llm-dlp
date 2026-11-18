package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	traceMaxFile = 50 << 20
	traceMaxBody = 16 << 20
	redacted     = "[redacted]"
)

type Tracer struct {
	state, file string
	maxFile     int64
	now         func() time.Time
	mu          sync.Mutex
}

func NewTracer(state, file string) *Tracer {
	return &Tracer{state: state, file: file, maxFile: traceMaxFile, now: time.Now}
}

func (t *Tracer) File() string { return t.file }

type traceState struct {
	Until time.Time `json:"until"`
}

func (t *Tracer) Until() (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	b, err := os.ReadFile(t.state)
	if err != nil {
		return time.Time{}, false
	}
	var s traceState
	if json.Unmarshal(b, &s) != nil {
		return time.Time{}, false
	}
	return s.Until, t.now().Before(s.Until)
}

func (t *Tracer) Enable(d time.Duration) (time.Time, error) {
	until := t.now().Add(d)
	b, _ := json.Marshal(traceState{Until: until})
	tmp := t.state + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return until, err
	}
	return until, os.Rename(tmp, t.state)
}

func (t *Tracer) Disable() error {
	if err := os.Remove(t.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type TraceRecord struct {
	Time              time.Time           `json:"time"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	Target            string              `json:"target"`
	Format            string              `json:"format,omitempty"`
	SentUnmasked      bool                `json:"sent_unmasked,omitempty"`
	Replacements      int                 `json:"replacements"`
	RequestHeaders    map[string][]string `json:"request_headers"`
	RequestBody       any                 `json:"request_body,omitempty"`
	Status            int                 `json:"status,omitempty"`
	Error             string              `json:"error,omitempty"`
	ResponseHeaders   map[string][]string `json:"response_headers,omitempty"`
	ResponseBody      any                 `json:"response_body,omitempty"`
	ResponseTruncated bool                `json:"response_truncated,omitempty"`
	DurationMS        int64               `json:"duration_ms"`
}

func (t *Tracer) Write(r TraceRecord) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if fi, err := os.Stat(t.file); err == nil && fi.Size()+int64(len(line)) > t.maxFile {
		if err := os.Rename(t.file, t.file+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(t.file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func traceBody(b []byte) any {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

func secretName(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	for _, w := range []string{"key", "token", "secret", "password", "signature", "auth"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func redactHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		if secretName(k) {
			out[k] = []string{redacted}
		} else {
			out[k] = vs
		}
	}
	return out
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redacted
	}
	u.User = nil
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if secretName(k) {
				q[k] = []string{redacted}
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

type captureBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *captureBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

type teeBody struct {
	io.Reader
	io.Closer
}

func captureResponse(resp *http.Response) *captureBuffer {
	c := &captureBuffer{max: traceMaxBody}
	resp.Body = teeBody{io.TeeReader(resp.Body, c), resp.Body}
	return c
}
