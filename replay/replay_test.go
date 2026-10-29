package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordThenReplayKeepsOrderOfIdenticalCalls(t *testing.T) {
	rec := New(Record)
	n := 0
	up := func() (json.RawMessage, error) { n++; return json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)), nil }
	rec.Call("get", json.RawMessage(`{"a":1,"b":2}`), up)
	rec.Call("get", json.RawMessage(`{"b":2, "a":1}`), up) // same call, state changed upstream
	p := filepath.Join(t.TempDir(), "c.jsonl")
	if err := rec.Save(p); err != nil {
		t.Fatal(err)
	}
	rp, err := Load(p, Replay)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		got, err := rp.Call("get", json.RawMessage(`{"a":1,"b":2}`), nil)
		if err != nil || string(got) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Fatalf("call %d: %s %v", i, got, err)
		}
	}
	if _, err := rp.Call("get", json.RawMessage(`{"a":1,"b":2}`), nil); !errors.Is(err, ErrMiss) {
		t.Fatalf("third identical call should miss, got %v", err)
	}
	if n != 2 {
		t.Fatalf("upstream called %d times", n)
	}
}

func TestHTTPProxyRecordsAndReplays(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, `{"tool":%q,"args":%s}`, strings.TrimPrefix(r.URL.Path, "/tools/"), b)
	}))
	defer up.Close()
	c := New(ReplayOrRecord)
	px := httptest.NewServer(&Proxy{C: c, Upstream: up.URL})
	defer px.Close()
	for i := 0; i < 2; i++ {
		resp, err := http.Post(px.URL+"/tools/find_user", "application/json", strings.NewReader(`{"email":"a@b"}`))
		if err != nil || resp.StatusCode != 200 {
			t.Fatal(err, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if calls != 2 || c.Misses != 2 {
		t.Fatalf("record mode: upstream calls %d misses %d", calls, c.Misses)
	}
	// now replay strictly from what was recorded
	r2 := New(Replay)
	r2.entries, r2.recorded = c.entries, c.recorded
	px2 := httptest.NewServer(&Proxy{C: r2})
	defer px2.Close()
	resp, _ := http.Post(px2.URL+"/tools/find_user", "application/json", strings.NewReader(`{"email": "a@b"}`))
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"find_user"`) || calls != 2 {
		t.Fatalf("replay: %s (upstream calls %d)", b, calls)
	}
}
