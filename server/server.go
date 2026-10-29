// Package server is the gate as a service: CI uploads each agent version's
// results, asks for a comparison, and gets back a verdict plus the PR comment.
// Runs and reports are kept (Postgres, or memory for local use) so a team can
// see the history of gate decisions per candidate.
//
//	PUT  /v1/runs/{name}?format=tau2|run   body: results file
//	GET  /v1/runs
//	POST /v1/compare                       {"base","candidate","alpha","fail_margin","pass_margin"}
//	GET  /v1/reports?limit=N
//	GET  /v1/reports/{id}                  report JSON
//	GET  /v1/reports/{id}/comment          PR comment (Markdown)
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"agentgate/gate"
	"agentgate/trajectory"
)

const maxUpload = 512 << 20

type Server struct {
	Store Store
	Base  gate.Config // defaults for compare requests
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("PUT /v1/runs/{name}", s.putRun)
	mux.HandleFunc("GET /v1/runs", s.listRuns)
	mux.HandleFunc("POST /v1/compare", s.compare)
	mux.HandleFunc("GET /v1/reports", s.listReports)
	mux.HandleFunc("GET /v1/reports/{id}", s.getReport)
	mux.HandleFunc("GET /v1/reports/{id}/comment", s.getComment)
	return mux
}

// ---- transport-neutral core: both the net/http and the Hertz front ends
// call these, so the two servers cannot drift apart.

// Resp is a handler result: a status code and either a JSON value or text.
type Resp struct {
	Code int
	JSON any
	Text string // when non-empty: Markdown body
}

func errResp(err error) Resp {
	code := http.StatusInternalServerError
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.As(err, &mbe), errors.Is(err, errTooLarge):
		code = http.StatusRequestEntityTooLarge
	case errors.Is(err, errBadRequest):
		code = http.StatusBadRequest
	}
	return Resp{Code: code, JSON: map[string]string{"error": err.Error()}}
}

var (
	errBadRequest = errors.New("bad request")
	errTooLarge   = errors.New("request body too large")
)

func (s *Server) PutRun(ctx context.Context, name, format string, b []byte) Resp {
	var run trajectory.Run
	var err error
	switch format {
	case "", "tau2":
		run, err = trajectory.ParseTau2(b, true)
	case "run":
		err = json.Unmarshal(b, &run)
	default:
		err = fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return errResp(fmt.Errorf("%w: %v", errBadRequest, err))
	}
	if len(run.Trials) == 0 {
		return errResp(fmt.Errorf("%w: no trials", errBadRequest))
	}
	run.Name = name
	i, err := s.Store.PutRun(ctx, name, run)
	if err != nil {
		return errResp(err)
	}
	return Resp{Code: http.StatusCreated, JSON: i}
}

func (s *Server) ListRuns(ctx context.Context) Resp {
	l, err := s.Store.ListRuns(ctx)
	if err != nil {
		return errResp(err)
	}
	return Resp{Code: 200, JSON: l}
}

type CompareRequest struct {
	Base       string   `json:"base"`
	Candidate  string   `json:"candidate"`
	Alpha      *float64 `json:"alpha,omitempty"`
	FailMargin *float64 `json:"fail_margin,omitempty"`
	PassMargin *float64 `json:"pass_margin,omitempty"`
}

type CompareResponse struct {
	ReportInfo
	Report  gate.Report `json:"report"`
	Comment string      `json:"comment_markdown"`
}

func (s *Server) Compare(ctx context.Context, body []byte) Resp {
	var req CompareRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return errResp(fmt.Errorf("%w: %v", errBadRequest, err))
	}
	cfg := s.Base
	if req.Alpha != nil {
		cfg.Alpha = *req.Alpha
	}
	if req.FailMargin != nil {
		cfg.FailMargin = *req.FailMargin
	}
	if req.PassMargin != nil {
		cfg.PassMargin = *req.PassMargin
	}
	if cfg.Alpha <= 0 || cfg.Alpha >= 0.5 || cfg.PassMargin < cfg.FailMargin {
		return errResp(fmt.Errorf("%w: need 0 < alpha < 0.5 and pass_margin >= fail_margin", errBadRequest))
	}
	a, err := s.Store.GetRun(ctx, req.Base)
	if err != nil {
		return errResp(fmt.Errorf("base %q: %w", req.Base, err))
	}
	b, err := s.Store.GetRun(ctx, req.Candidate)
	if err != nil {
		return errResp(fmt.Errorf("candidate %q: %w", req.Candidate, err))
	}
	rep := gate.Compare(a, b, cfg)
	i, err := s.Store.PutReport(ctx, rep, cfg)
	if err != nil {
		return errResp(err)
	}
	return Resp{Code: 200, JSON: CompareResponse{ReportInfo: i, Report: rep, Comment: rep.Markdown(cfg)}}
}

func (s *Server) ListReports(ctx context.Context, limitStr string) Resp {
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	l, err := s.Store.ListReports(ctx, limit)
	if err != nil {
		return errResp(err)
	}
	return Resp{Code: 200, JSON: l}
}

func (s *Server) GetReport(ctx context.Context, idStr string, comment bool) Resp {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return errResp(fmt.Errorf("%w: bad id", errBadRequest))
	}
	rep, cfg, i, err := s.Store.GetReport(ctx, id)
	if err != nil {
		return errResp(err)
	}
	if comment {
		return Resp{Code: 200, Text: rep.Markdown(cfg)}
	}
	return Resp{Code: 200, JSON: map[string]any{"info": i, "report": rep}}
}

// ---- net/http front end

func write(w http.ResponseWriter, r Resp) {
	if r.Text != "" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(r.Code)
		io.WriteString(w, r.Text)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.Code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(r.JSON)
}

func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, max))
}

func (s *Server) putRun(w http.ResponseWriter, r *http.Request) {
	b, err := readBody(w, r, maxUpload)
	if err != nil {
		write(w, errResp(err))
		return
	}
	write(w, s.PutRun(r.Context(), r.PathValue("name"), r.URL.Query().Get("format"), b))
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) { write(w, s.ListRuns(r.Context())) }

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	b, err := readBody(w, r, 1<<20)
	if err != nil {
		write(w, errResp(err))
		return
	}
	write(w, s.Compare(r.Context(), b))
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	write(w, s.ListReports(r.Context(), r.URL.Query().Get("limit")))
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	write(w, s.GetReport(r.Context(), r.PathValue("id"), false))
}

func (s *Server) getComment(w http.ResponseWriter, r *http.Request) {
	write(w, s.GetReport(r.Context(), r.PathValue("id"), true))
}

// Main runs the service until SIGINT/SIGTERM.
func Main(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	front := fs.String("http", "std", "HTTP front end: std (net/http) | hertz (CloudWeGo Hertz)")
	dsn := fs.String("dsn", os.Getenv("AGENTGATE_DSN"), "Postgres DSN; empty keeps everything in memory")
	fs.Parse(args)
	ctx := context.Background()
	var st Store = NewMemStore()
	if *dsn != "" {
		db, err := sql.Open("postgres", *dsn)
		if err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := db.PingContext(pctx); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if st, err = NewPGStore(pctx, db); err != nil {
			return err
		}
		log.Printf("store: postgres")
	} else {
		log.Printf("store: memory")
	}
	defer st.Close()
	s := &Server{Store: st, Base: gate.DefaultConfig()}
	if *front == "hertz" {
		log.Printf("agentgate serving on %s (hertz)", *addr)
		return s.ServeHertz(*addr)
	}
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("agentgate serving on %s", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
