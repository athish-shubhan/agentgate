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
