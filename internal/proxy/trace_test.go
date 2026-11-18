package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yurivenancio30/llm-dlp/internal/config"
	"github.com/yurivenancio30/llm-dlp/internal/mask"
)

func buildTraced(t *testing.T, tr *Tracer, upstream http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	cfg := config.Default()
	cfg.InternalDomains = []string{"empresa-ficticia"}
	key := []byte("0123456789abcdef0123456789abcdef")
	ps, _ := mask.LoadPeople(t.TempDir() + "/p.json")
	m, err := mask.NewMasker(cfg, key, ps, nil)
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(upstream)
	cfg.Upstream = up.URL
	px := httptest.NewServer(New(cfg, m, nil, log.New(io.Discard, "", 0)).WithTrace(tr))
	t.Cleanup(func() { up.Close(); px.Close() })
	return px, up
}

func newTestTracer(t *testing.T) *Tracer {
	dir := t.TempDir()
	return NewTracer(filepath.Join(dir, "trace.json"), filepath.Join(dir, "trace.jsonl"))
}

func traceLines(t *testing.T, tr *Tracer) []TraceRecord {
	t.Helper()
	f, err := os.Open(tr.File())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []TraceRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var r TraceRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("invalid trace line: %v: %s", err, sc.Bytes())
		}
		out = append(out, r)
	}
	return out
}

func echoPseudonymJSON(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	p := rePseudo.FindString(string(b))
	w.Header().Set("content-type", "application/json")
	w.Header().Set("set-cookie", "session=upstream-cookie")
	json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "falei com " + p}}})
}

func TestTraceRecordsWhatLeftMaskedWithoutCredentials(t *testing.T) {
	tr := newTestTracer(t)
	if _, err := tr.Enable(time.Hour); err != nil {
		t.Fatal(err)
	}
	px, _ := buildTraced(t, tr, echoPseudonymJSON)
	req, _ := http.NewRequest("POST", px.URL+"/v1/messages?beta=true&key=query-secret-123", bytes.NewReader(requestBody(false)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer oauth-secret-456")
	req.Header.Set("x-api-key", "sk-ant-secret-789")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(got), email) {
		t.Errorf("client did not get the real value back: %s", got)
	}

	raw, _ := os.ReadFile(tr.File())
	for _, leak := range []string{email, cpf, host, "oauth-secret-456", "sk-ant-secret-789", "query-secret-123", "upstream-cookie"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("trace contains %q", leak)
		}
	}
	if fi, _ := os.Stat(tr.File()); fi.Mode().Perm() != 0o600 {
		t.Errorf("trace mode %v", fi.Mode().Perm())
	}
	recs := traceLines(t, tr)
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	if r.Method != "POST" || r.Path != "/v1/messages" || r.Format != "anthropic" || r.Status != 200 || r.Replacements == 0 {
		t.Errorf("record: %+v", r)
	}
	if !strings.Contains(r.Target, "beta=true") || !strings.Contains(r.Target, "key=%5Bredacted%5D") {
		t.Errorf("target %q", r.Target)
	}
	if r.RequestHeaders["Authorization"][0] != redacted || r.RequestHeaders["X-Api-Key"][0] != redacted {
		t.Errorf("credential headers: %v", r.RequestHeaders)
	}
	if r.RequestHeaders["Anthropic-Version"][0] != "2023-06-01" {
		t.Errorf("ordinary header lost: %v", r.RequestHeaders)
	}
	reqBody, _ := json.Marshal(r.RequestBody)
	respBody, _ := json.Marshal(r.ResponseBody)
	if !rePseudo.Match(reqBody) || !rePseudo.Match(respBody) {
		t.Errorf("pseudonyms missing:\nrequest %s\nresponse %s", reqBody, respBody)
	}
}

func TestTraceRecordsTheStreamBeforeUnmasking(t *testing.T) {
	tr := newTestTracer(t)
	tr.Enable(time.Hour)
	px, _ := buildTraced(t, tr, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p := rePseudo.FindString(string(b))
		w.Header().Set("content-type", "text/event-stream")
		sse(w, "falei com "+p, "", "", 5)
	})
	resp, err := http.Post(px.URL+"/v1/messages", "application/json", bytes.NewReader(requestBody(true)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(got), "joao") {
		t.Errorf("stream not unmasked for the client: %s", got)
	}
	recs := traceLines(t, tr)
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	s, ok := recs[0].ResponseBody.(string)
	if !ok || !strings.Contains(s, "event:") || strings.Contains(s, email) {
		t.Errorf("stream in the trace: %T %v", recs[0].ResponseBody, recs[0].ResponseBody)
	}
}

func TestTraceOffOrExpiredWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Tracer)
	}{
		{"never enabled", func(*Tracer) {}},
		{"expired", func(tr *Tracer) {
			tr.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
			tr.Enable(time.Hour)
			tr.now = time.Now
		}},
		{"disabled", func(tr *Tracer) { tr.Enable(time.Hour); tr.Disable() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestTracer(t)
			tc.setup(tr)
			px, _ := buildTraced(t, tr, echoPseudonymJSON)
			resp, err := http.Post(px.URL+"/v1/messages", "application/json", bytes.NewReader(requestBody(false)))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if _, err := os.Stat(tr.File()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("trace written: %v", err)
			}
		})
	}
}

func TestTraceRecordsUpstreamFailure(t *testing.T) {
	tr := newTestTracer(t)
	tr.Enable(time.Hour)
	px, up := buildTraced(t, tr, echoPseudonymJSON)
	up.Close()
	resp, err := http.Post(px.URL+"/v1/messages", "application/json", bytes.NewReader(requestBody(false)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	recs := traceLines(t, tr)
	if len(recs) != 1 || recs[0].Error == "" || recs[0].Status != 0 || recs[0].RequestBody == nil {
		t.Fatalf("records: %+v", recs)
	}
	if b, _ := json.Marshal(recs[0].RequestBody); strings.Contains(string(b), email) {
		t.Errorf("real value in the failed request: %s", b)
	}
}

func TestTraceRotates(t *testing.T) {
	tr := newTestTracer(t)
	tr.maxFile = 600
	for i := 0; i < 5; i++ {
		if err := tr.Write(TraceRecord{Method: "POST", Path: fmt.Sprintf("/v1/messages/%d", i), RequestBody: strings.Repeat("x", 80)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(tr.File() + ".1"); err != nil {
		t.Errorf("no rotated file: %v", err)
	}
	if fi, _ := os.Stat(tr.File()); fi.Size() > 600 {
		t.Errorf("current trace %d bytes", fi.Size())
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:pass@api.example/v1?key=abc&alt=sse": "https://api.example/v1?alt=sse&key=%5Bredacted%5D",
		"https://api.anthropic.com/v1/messages":            "https://api.anthropic.com/v1/messages",
		"https://x.example/p?access_token=t&x=1":           "https://x.example/p?access_token=%5Bredacted%5D&x=1",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptureBufferTruncates(t *testing.T) {
	c := &captureBuffer{max: 4}
	n, err := c.Write([]byte("abcdef"))
	if n != 6 || err != nil || c.buf.String() != "abcd" || !c.truncated {
		t.Errorf("n %d err %v buf %q truncated %v", n, err, c.buf.String(), c.truncated)
	}
}
