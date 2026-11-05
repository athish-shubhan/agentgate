package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// An upstream that answers with plain text used to make Save fail, and the
// failed Save truncated the cassette to 0 bytes.
func TestNonJSONUpstreamBodySavesAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	rec := New(Record)
	if _, err := rec.Call("good", json.RawMessage(`{"a":1}`), func() (json.RawMessage, error) { return json.RawMessage(`{"ok":true}`), nil }); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Call("text", json.RawMessage(`{}`), func() (json.RawMessage, error) { return json.RawMessage("OK, done"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(path); err != nil {
		t.Fatalf("save with a text body: %v", err)
	}
	rp, err := Load(path, Replay)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rp.Call("text", json.RawMessage(`{}`), nil); err != nil || string(got) != "OK, done" {
		t.Fatalf("text body replay: %q %v", got, err)
	}
	if got, err := rp.Call("good", json.RawMessage(`{ "a" : 1 }`), nil); err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("json body replay: %q %v", got, err)
	}
}

// A Save that fails leaves the previous cassette intact and no temp files.
func TestFailedSaveKeepsOldCassette(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	c := New(Record)
	_, _ = c.Call("t", json.RawMessage(`{}`), func() (json.RawMessage, error) { return json.RawMessage(`1`), nil })
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c.recorded = append(c.recorded, Entry{Tool: "broken", Args: json.RawMessage(`{`)}) // unencodable
	if err := c.Save(path); err == nil {
		t.Fatal("want an encoding error")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) || len(before) == 0 {
		t.Fatalf("cassette changed by a failed save: %q -> %q", before, after)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
}

func TestBadArgsAndBadCassetteLine(t *testing.T) {
	c := New(Record)
	if _, err := c.Call("t", json.RawMessage(`not json`), func() (json.RawMessage, error) { return nil, nil }); !errors.Is(err, ErrBadArgs) {
		t.Fatalf("got %v", err)
	}
	path := filepath.Join(t.TempDir(), "c.jsonl")
	if err := os.WriteFile(path, []byte("{\"tool\":\"a\",\"args\":{}}\n\n{oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, Replay); err == nil || !strings.Contains(err.Error(), "c.jsonl:3:") {
		t.Fatalf("want the line number of the bad entry, got %v", err)
	}
}

func TestProxyRejectsNonJSONArguments(t *testing.T) {
	ts := httptest.NewServer(&Proxy{C: New(Replay)})
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/tools/x", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
