package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agentgate/gate"
	"agentgate/trajectory"
)

// synthRun builds a run where task i passes with probability rates[i] over n trials.
func synthRun(name string, rates []float64, n int) trajectory.Run {
	r := trajectory.Run{Name: name}
	for i, p := range rates {
		for k := 0; k < n; k++ {
			pass := float64(k) < p*float64(n)
			rw := 0.0
			if pass {
				rw = 1
			}
			r.Trials = append(r.Trials, trajectory.Trial{Suite: "s", Task: fmt.Sprint(i), Trial: k, Reward: rw, Pass: pass})
		}
	}
	return r
}

func flat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func exercise(t *testing.T, st Store) {
	ts := httptest.NewServer((&Server{Store: st, Base: gate.DefaultConfig()}).Handler())
	defer ts.Close()
	exerciseURL(t, ts.URL)
}

// The Hertz front end must behave exactly like the net/http one.
func TestHertzFrontEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h := (&Server{Store: NewMemStore(), Base: gate.DefaultConfig()}).HertzEngine(addr)
	go h.Run()
	defer h.Shutdown(context.Background())
	for i := 0; i < 100; i++ {
		if r, err := http.Get("http://" + addr + "/healthz"); err == nil {
			r.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	exerciseURL(t, "http://"+addr)
}

func exerciseURL(t *testing.T, base string) {
	ts := struct{ URL string }{base}
	put := func(name string, run trajectory.Run) {
		b, _ := json.Marshal(run)
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/runs/"+name+"?format=run", bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("put %s: %v %d %s", name, err, resp.StatusCode, body)
		}
		resp.Body.Close()
	}
	// base passes 80% everywhere; "bad" drops every task by 50 points; "same" is identical
	put("base", synthRun("base", flat(0.75, 60), 4))
	put("bad", synthRun("bad", flat(0.25, 60), 4))
	put("same", synthRun("same", flat(0.75, 60), 4))

	cmp := func(body string) (CompareResponse, int) {
		resp, err := http.Post(ts.URL+"/v1/compare", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var cr CompareResponse
		json.NewDecoder(resp.Body).Decode(&cr)
		return cr, resp.StatusCode
	}
	cr, code := cmp(`{"base":"base","candidate":"bad"}`)
	if code != 200 || cr.Verdict != gate.Fail || !strings.Contains(cr.Comment, "FAIL") {
		t.Fatalf("bad: code %d verdict %s", code, cr.Verdict)
	}
	cr2, _ := cmp(`{"base":"base","candidate":"same"}`)
	if cr2.Verdict != gate.Pass {
		t.Fatalf("same: verdict %s", cr2.Verdict)
	}
	if _, code := cmp(`{"base":"base","candidate":"missing"}`); code != 404 {
		t.Fatalf("missing run: %d", code)
	}
	if _, code := cmp(`{"base":"base","candidate":"bad","alpha":0.9}`); code != 400 {
		t.Fatalf("bad alpha: %d", code)
	}
	resp, _ := http.Get(fmt.Sprintf("%s/v1/reports/%d/comment", ts.URL, cr.ID))
	md, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(md), "FAIL") {
		t.Fatalf("comment: %d %s", resp.StatusCode, md)
	}
	resp, _ = http.Get(ts.URL + "/v1/reports?limit=10")
	var l []ReportInfo
	json.NewDecoder(resp.Body).Decode(&l)
	if len(l) < 2 || l[0].ID != cr2.ID {
		t.Fatalf("list: %+v", l)
	}
	resp, _ = http.Get(ts.URL + "/v1/runs")
	var runs []RunInfo
	json.NewDecoder(resp.Body).Decode(&runs)
	if len(runs) < 3 {
		t.Fatalf("runs: %+v", runs)
	}
	// One shared task and two trials: SE is infinite and pass^3, pass^4 are
	// undefined. The response must still be a complete JSON report (it used
	// to be HTTP 200 with an empty body).
	put("tiny-a", synthRun("tiny-a", []float64{1}, 2))
	put("tiny-b", synthRun("tiny-b", []float64{0.5}, 2))
	resp, err := http.Post(ts.URL+"/v1/compare", "application/json", strings.NewReader(`{"base":"tiny-a","candidate":"tiny-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var tiny CompareResponse
	if err := json.Unmarshal(raw, &tiny); err != nil || resp.StatusCode != 200 {
		t.Fatalf("tiny compare: %d %v %q", resp.StatusCode, err, raw)
	}
	if tiny.Verdict != gate.Inconclusive || tiny.Report.Overall.Tasks != 1 || !strings.Contains(string(raw), `"se": null`) {
		t.Fatalf("tiny compare: %s", raw)
	}
	resp, _ = http.Get(fmt.Sprintf("%s/v1/reports/%d", ts.URL, tiny.ID))
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !json.Valid(raw) {
		t.Fatalf("tiny report: %d %q", resp.StatusCode, raw)
	}

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/runs/x?format=tau2", strings.NewReader(`{"hello":1}`))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Fatalf("garbage upload: %d", resp.StatusCode)
	}
}

func TestMemStore(t *testing.T) { exercise(t, NewMemStore()) }

// Runs against a real Postgres when AGENTGATE_TEST_DSN is set, e.g.
// postgres://postgres:postgres@127.0.0.1:5432/agentgate?sslmode=disable
func TestPGStore(t *testing.T) {
	dsn := os.Getenv("AGENTGATE_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTGATE_TEST_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`DROP TABLE IF EXISTS agentgate_runs, agentgate_reports`)
	st, err := NewPGStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exercise(t, st)
}

// Error responses carry the right status and a JSON error body, on both
// front ends' shared core.
func TestErrorStatuses(t *testing.T) {
	ts := httptest.NewServer((&Server{Store: NewMemStore(), Base: gate.DefaultConfig()}).Handler())
	defer ts.Close()
	req := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	cases := []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/v1/reports/abc", "", 400},
		{"GET", "/v1/reports/999", "", 404},
		{"GET", "/v1/reports/999/comment", "", 404},
		{"PUT", "/v1/runs/x?format=yaml", "{}", 400},
		{"PUT", "/v1/runs/x?format=run", `{"name":"x","trials":[]}`, 400},
		{"PUT", "/v1/runs/x?format=run", `{not json`, 400},
		{"POST", "/v1/compare", `{not json`, 400},
		{"POST", "/v1/compare", `{"base":"a","candidate":"b"}`, 404},
		{"POST", "/v1/compare", `{"base":"a","candidate":"b","pass_margin":0,"fail_margin":0.1}`, 400},
		{"POST", "/v1/compare", `{"base":"` + strings.Repeat("x", 2<<20) + `"}`, 413},
	}
	for _, c := range cases {
		code, m := req(c.method, c.path, c.body)
		if code != c.code || m["error"] == nil {
			t.Errorf("%s %s: %d %v, want %d with an error message", c.method, c.path[:min(len(c.path), 40)], code, m, c.code)
		}
	}
	if code, _ := req("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz %d", code)
	}
}

func TestListReportsLimit(t *testing.T) {
	s := &Server{Store: NewMemStore(), Base: gate.DefaultConfig()}
	ctx := context.Background()
	a, b := synthRun("a", flat(0.5, 10), 2), synthRun("b", flat(0.5, 10), 2)
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	s.PutRun(ctx, "a", "run", ab)
	s.PutRun(ctx, "b", "run", bb)
	for i := 0; i < 5; i++ {
		if r := s.Compare(ctx, []byte(`{"base":"a","candidate":"b"}`)); r.Code != 200 {
			t.Fatalf("compare: %+v", r)
		}
	}
	for limit, want := range map[string]int{"2": 2, "": 5, "0": 5, "-3": 5, "junk": 5, "100000": 5} {
		r := s.ListReports(ctx, limit)
		if l := r.JSON.([]ReportInfo); len(l) != want {
			t.Errorf("limit %q: %d reports, want %d", limit, len(l), want)
		}
	}
}
