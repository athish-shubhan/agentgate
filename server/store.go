package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"agentgate/gate"
	"agentgate/trajectory"
)

var ErrNotFound = errors.New("not found")

type RunInfo struct {
	Name    string    `json:"name"`
	Trials  int       `json:"trials"`
	Tasks   int       `json:"tasks"`
	Created time.Time `json:"created_at"`
}

type ReportInfo struct {
	ID        int64        `json:"id"`
	Base      string       `json:"base"`
	Candidate string       `json:"candidate"`
	Verdict   gate.Verdict `json:"verdict"`
	Created   time.Time    `json:"created_at"`
}

// Store keeps uploaded runs and the reports computed from them.
type Store interface {
	PutRun(ctx context.Context, name string, r trajectory.Run) (RunInfo, error)
	GetRun(ctx context.Context, name string) (trajectory.Run, error)
	ListRuns(ctx context.Context) ([]RunInfo, error)
	PutReport(ctx context.Context, rep gate.Report, cfg gate.Config) (ReportInfo, error)
	GetReport(ctx context.Context, id int64) (gate.Report, gate.Config, ReportInfo, error)
	ListReports(ctx context.Context, limit int) ([]ReportInfo, error)
	Close() error
}

func info(name string, r trajectory.Run, t time.Time) RunInfo {
	return RunInfo{Name: name, Trials: len(r.Trials), Tasks: len(r.ByTask()), Created: t}
}

// ---- in-memory

type memRun struct {
	run  trajectory.Run
	info RunInfo
}

type memReport struct {
	rep  gate.Report
	cfg  gate.Config
	info ReportInfo
}

type MemStore struct {
	mu      sync.Mutex
	runs    map[string]memRun
	reports []memReport
}

func NewMemStore() *MemStore { return &MemStore{runs: map[string]memRun{}} }

func (m *MemStore) PutRun(_ context.Context, name string, r trajectory.Run) (RunInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := info(name, r, time.Now().UTC())
	m.runs[name] = memRun{r, i}
	return i, nil
}

func (m *MemStore) GetRun(_ context.Context, name string) (trajectory.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[name]
	if !ok {
		return trajectory.Run{}, ErrNotFound
	}
	return r.run, nil
}

func (m *MemStore) ListRuns(context.Context) ([]RunInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RunInfo{}
	for _, r := range m.runs {
		out = append(out, r.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemStore) PutReport(_ context.Context, rep gate.Report, cfg gate.Config) (ReportInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := ReportInfo{ID: int64(len(m.reports) + 1), Base: rep.Base, Candidate: rep.Candidate, Verdict: rep.Verdict, Created: time.Now().UTC()}
	m.reports = append(m.reports, memReport{rep, cfg, i})
	return i, nil
}

func (m *MemStore) GetReport(_ context.Context, id int64) (gate.Report, gate.Config, ReportInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || id > int64(len(m.reports)) {
		return gate.Report{}, gate.Config{}, ReportInfo{}, ErrNotFound
	}
	r := m.reports[id-1]
	return r.rep, r.cfg, r.info, nil
}

func (m *MemStore) ListReports(_ context.Context, limit int) ([]ReportInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ReportInfo{}
	for i := len(m.reports) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.reports[i].info)
	}
	return out, nil
}

func (m *MemStore) Close() error { return nil }

// ---- Postgres

const schema = `
CREATE TABLE IF NOT EXISTS agentgate_runs (
	name       text PRIMARY KEY,
	trials     int NOT NULL,
	tasks      int NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	body       jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS agentgate_reports (
	id         bigserial PRIMARY KEY,
	base       text NOT NULL,
	candidate  text NOT NULL,
	verdict    text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	config     jsonb NOT NULL,
	body       jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS agentgate_reports_candidate ON agentgate_reports (candidate, created_at DESC);
`

type PGStore struct{ db *sql.DB }

func NewPGStore(ctx context.Context, db *sql.DB) (*PGStore, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	return &PGStore{db}, nil
}

func (p *PGStore) PutRun(ctx context.Context, name string, r trajectory.Run) (RunInfo, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return RunInfo{}, err
	}
	i := info(name, r, time.Time{})
	err = p.db.QueryRowContext(ctx, `
		INSERT INTO agentgate_runs (name, trials, tasks, body) VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE SET trials = EXCLUDED.trials, tasks = EXCLUDED.tasks, body = EXCLUDED.body, created_at = now()
		RETURNING created_at`, name, i.Trials, i.Tasks, b).Scan(&i.Created)
	return i, err
}

func (p *PGStore) GetRun(ctx context.Context, name string) (trajectory.Run, error) {
	var b []byte
	var r trajectory.Run
	err := p.db.QueryRowContext(ctx, `SELECT body FROM agentgate_runs WHERE name = $1`, name).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

func (p *PGStore) ListRuns(ctx context.Context) ([]RunInfo, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT name, trials, tasks, created_at FROM agentgate_runs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunInfo{}
	for rows.Next() {
		var i RunInfo
		if err := rows.Scan(&i.Name, &i.Trials, &i.Tasks, &i.Created); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (p *PGStore) PutReport(ctx context.Context, rep gate.Report, cfg gate.Config) (ReportInfo, error) {
	rb, err := json.Marshal(rep)
	if err != nil {
		return ReportInfo{}, err
	}
	cb, _ := json.Marshal(cfg)
	i := ReportInfo{Base: rep.Base, Candidate: rep.Candidate, Verdict: rep.Verdict}
	err = p.db.QueryRowContext(ctx, `
		INSERT INTO agentgate_reports (base, candidate, verdict, config, body) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`, rep.Base, rep.Candidate, string(rep.Verdict), cb, rb).Scan(&i.ID, &i.Created)
	return i, err
}

func (p *PGStore) GetReport(ctx context.Context, id int64) (gate.Report, gate.Config, ReportInfo, error) {
	var rb, cb []byte
	var rep gate.Report
	var cfg gate.Config
	i := ReportInfo{ID: id}
	var v string
	err := p.db.QueryRowContext(ctx, `SELECT base, candidate, verdict, created_at, config, body FROM agentgate_reports WHERE id = $1`, id).
		Scan(&i.Base, &i.Candidate, &v, &i.Created, &cb, &rb)
	if errors.Is(err, sql.ErrNoRows) {
		return rep, cfg, i, ErrNotFound
	}
	if err != nil {
		return rep, cfg, i, err
	}
	i.Verdict = gate.Verdict(v)
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return rep, cfg, i, err
	}
	return rep, cfg, i, json.Unmarshal(rb, &rep)
}

func (p *PGStore) ListReports(ctx context.Context, limit int) ([]ReportInfo, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, base, candidate, verdict, created_at FROM agentgate_reports ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportInfo{}
	for rows.Next() {
		var i ReportInfo
		var v string
		if err := rows.Scan(&i.ID, &i.Base, &i.Candidate, &v, &i.Created); err != nil {
			return nil, err
		}
		i.Verdict = gate.Verdict(v)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (p *PGStore) Close() error { return p.db.Close() }
