// Package gate decides whether a candidate agent version (B) regressed
// against a baseline (A), and explains the decision.
//
// Decision rule (one-sided level alpha on each side):
//
//	FAIL          the upper confidence bound of (B - A) is below -FailMargin:
//	              B is worse with confidence
//	PASS          the lower confidence bound is above -PassMargin: B is no
//	              worse than A by more than PassMargin (non-inferiority)
//	INCONCLUSIVE  neither: run more trials or tasks
//
// The comparison is paired by task and clustered at the task level (see
// package stats). Trials that ended for infrastructure reasons are excluded
// and counted, so a flaky harness does not look like a regression.
package gate

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"agentgate/graders"
	"agentgate/stats"
	"agentgate/trajectory"
)

type Config struct {
	Alpha      float64       `json:"alpha"`
	FailMargin float64       `json:"fail_margin"`
	PassMargin float64       `json:"pass_margin"`
	Rules      graders.Rules `json:"rules"`
	MaxK       int           `json:"max_k"` // pass^k reported up to this k
}

func DefaultConfig() Config {
	return Config{Alpha: 0.05, FailMargin: 0, PassMargin: 0.05, Rules: graders.DefaultRules(), MaxK: 4}
}

type Verdict string

const (
	Pass         Verdict = "PASS"
	Fail         Verdict = "FAIL"
	Inconclusive Verdict = "INCONCLUSIVE"
)

type TaskDelta struct {
	Task     string   `json:"task"`
	RateA    float64  `json:"rate_a"`
	RateB    float64  `json:"rate_b"`
	NewRules []string `json:"new_rules,omitempty"` // rules broken in B's failures but in none of A's trials
}

type Report struct {
	Base           string                      `json:"base"`
	Candidate      string                      `json:"candidate"`
	Verdict        Verdict                     `json:"verdict"`
	Overall        stats.Comparison            `json:"overall"`
	PerSuite       map[string]stats.Comparison `json:"per_suite"`
	PassKA         []float64                   `json:"pass_k_a"`
	PassKB         []float64                   `json:"pass_k_b"`
	InfraExcludedA int                         `json:"infra_excluded_a"`
	InfraExcludedB int                         `json:"infra_excluded_b"`
	Regressed      []TaskDelta                 `json:"regressed_tasks"`
	Improved       int                         `json:"improved_tasks"`
	RuleShift      map[string][2]int           `json:"rule_shift"` // rule -> trials violating it in A, B
	MDE            float64                     `json:"mde_80pct_power"`
	CostA          float64                     `json:"cost_usd_a"`
	CostB          float64                     `json:"cost_usd_b"`
}

// Outcomes turns trials into per-task outcomes, skipping infra failures.
// maxTrials < 0 keeps every trial; otherwise only trials with index < maxTrials.
func Outcomes(r trajectory.Run, maxTrials int) (map[string]stats.TaskOutcome, int) {
	out := map[string]stats.TaskOutcome{}
	infra := 0
	for _, t := range r.Trials {
		if maxTrials >= 0 && t.Trial >= maxTrials {
			continue
		}
		if t.Infra {
			infra++
			continue
		}
		k := t.Suite + "/" + t.Task
		o := out[k]
		o.Trials++
		if t.Pass {
			o.Successes++
		}
		out[k] = o
	}
	return out, infra
}

func Decide(c stats.Comparison, cfg Config) Verdict {
	switch {
	case c.Tasks < 2:
		return Inconclusive
	case c.CIHigh < -cfg.FailMargin:
		return Fail
	case c.CILow > -cfg.PassMargin:
		return Pass
	}
	return Inconclusive
}

// Compare runs the full gate.
func Compare(a, b trajectory.Run, cfg Config) Report {
	oa, ia := Outcomes(a, -1)
	ob, ib := Outcomes(b, -1)
	rep := Report{Base: a.Name, Candidate: b.Name, InfraExcludedA: ia, InfraExcludedB: ib, PerSuite: map[string]stats.Comparison{},
		RuleShift: map[string][2]int{}}
	rep.Overall = stats.Paired(oa, ob, cfg.Alpha)
	rep.Verdict = Decide(rep.Overall, cfg)
	// per suite
	suites := map[string]bool{}
	for k := range oa {
		suites[strings.SplitN(k, "/", 2)[0]] = true
	}
	for s := range suites {
		sa, sb := map[string]stats.TaskOutcome{}, map[string]stats.TaskOutcome{}
		for k, v := range oa {
			if strings.HasPrefix(k, s+"/") {
				sa[k] = v
			}
		}
		for k, v := range ob {
			if strings.HasPrefix(k, s+"/") {
				sb[k] = v
			}
		}
		rep.PerSuite[s] = stats.Paired(sa, sb, cfg.Alpha)
	}
	for k := 1; k <= cfg.MaxK; k++ {
		rep.PassKA = append(rep.PassKA, stats.PassHatK(oa, k))
		rep.PassKB = append(rep.PassKB, stats.PassHatK(ob, k))
	}
	// power: smallest drop this many tasks can detect at 80% power
	var ds []float64
	for k, x := range oa {
		if y, ok := ob[k]; ok {
			ds = append(ds, y.Rate()-x.Rate())
		}
	}
	if len(ds) > 1 {
		rep.MDE = stats.MDE(stats.StdDev(ds), len(ds), cfg.Alpha, 0.8)
	}
	// diagnosis: per task, which rules appear in B's failures but not in A
	ta, tb := a.ByTask(), b.ByTask()
	rulesIn := func(ts []trajectory.Trial, onlyFailures bool) map[string]bool {
		m := map[string]bool{}
		for _, t := range ts {
			if onlyFailures && t.Pass {
				continue
			}
			for _, v := range graders.Grade(cfg.Rules, t) {
				m[v.Rule] = true
			}
		}
		return m
	}
	for _, t := range a.Trials {
		for _, r := range uniqRules(graders.Grade(cfg.Rules, t)) {
			s := rep.RuleShift[r]
			s[0]++
			rep.RuleShift[r] = s
		}
		rep.CostA += t.CostUSD
	}
	for _, t := range b.Trials {
		for _, r := range uniqRules(graders.Grade(cfg.Rules, t)) {
			s := rep.RuleShift[r]
			s[1]++
			rep.RuleShift[r] = s
		}
		rep.CostB += t.CostUSD
	}
	for k, x := range oa {
		y, ok := ob[k]
		if !ok {
			continue
		}
		switch {
		case y.Rate() < x.Rate()-0.25:
			inA := rulesIn(ta[k], false)
			var nr []string
			for r := range rulesIn(tb[k], true) {
				if !inA[r] {
					nr = append(nr, r)
				}
			}
			sort.Strings(nr)
			rep.Regressed = append(rep.Regressed, TaskDelta{k, x.Rate(), y.Rate(), nr})
		case y.Rate() > x.Rate()+0.25:
			rep.Improved++
		}
	}
	sort.Slice(rep.Regressed, func(i, j int) bool {
		di := rep.Regressed[i].RateA - rep.Regressed[i].RateB
		dj := rep.Regressed[j].RateA - rep.Regressed[j].RateB
		if di != dj {
			return di > dj
		}
		return rep.Regressed[i].Task < rep.Regressed[j].Task
	})
	return rep
}

func uniqRules(v []graders.Violation) []string {
	m := map[string]bool{}
	var out []string
	for _, x := range v {
		if !m[x.Rule] {
			m[x.Rule] = true
			out = append(out, x.Rule)
		}
	}
	return out
}

// Markdown renders the report as a pull-request comment.
func (r Report) Markdown(cfg Config) string {
	var b strings.Builder
	icon := map[Verdict]string{Pass: "PASS", Fail: "FAIL (regression)", Inconclusive: "INCONCLUSIVE (needs more trials)"}[r.Verdict]
	o := r.Overall
	fmt.Fprintf(&b, "### AgentGate: %s\n\n", icon)
	fmt.Fprintf(&b, "`%s` vs `%s` on %d tasks. Pass rate %.1f%% -> %.1f%% (%+.1f points, %.0f%% CI %+.1f to %+.1f, one-sided p = %.3g).\n\n",
		r.Base, r.Candidate, o.Tasks, 100*o.RateA, 100*o.RateB, 100*o.Diff, 100*(1-2*cfg.Alpha), 100*o.CILow, 100*o.CIHigh, o.PRegress)
	fmt.Fprintf(&b, "Rule: FAIL if the whole interval is below %s points; PASS if it is entirely above %s points; otherwise INCONCLUSIVE. "+
		"With these tasks the smallest drop detectable at 80%% power is %.1f points.\n\n", signedPts(-cfg.FailMargin), signedPts(-cfg.PassMargin), 100*r.MDE)
	b.WriteString("| suite | tasks | A | B | diff | CI |\n|---|---|---|---|---|---|\n")
	var ss []string
	for s := range r.PerSuite {
		ss = append(ss, s)
	}
	sort.Strings(ss)
	for _, s := range ss {
		c := r.PerSuite[s]
		fmt.Fprintf(&b, "| %s | %d | %.1f%% | %.1f%% | %+.1f | %+.1f to %+.1f |\n", s, c.Tasks, 100*c.RateA, 100*c.RateB, 100*c.Diff, 100*c.CILow, 100*c.CIHigh)
	}
	row := func(label string, cells []string) {
		fmt.Fprintf(&b, "| %s | %s |\n", label, strings.Join(cells, " | "))
	}
	var hdr, sep, ca, cb []string
	for k := range r.PassKA {
		hdr = append(hdr, fmt.Sprintf("pass^%d", k+1))
		sep = append(sep, "---")
		ca = append(ca, fmt.Sprintf("%.3f", r.PassKA[k]))
		if k < len(r.PassKB) {
			cb = append(cb, fmt.Sprintf("%.3f", r.PassKB[k]))
		}
	}
	b.WriteString("\n")
	row("", hdr)
	row("---", sep)
	row("A", ca)
	row("B", cb)
	b.WriteString("\n")
	if len(r.Regressed) > 0 {
		cnt := map[string]int{}
		for _, t := range r.Regressed {
			for _, x := range t.NewRules {
				cnt[x]++
			}
		}
		fmt.Fprintf(&b, "**%d tasks got worse by more than 25 points** (%d got better).", len(r.Regressed), r.Improved)
		if len(cnt) > 0 {
			type kv struct {
				k string
				v int
			}
			var kvs []kv
			for k, v := range cnt {
				kvs = append(kvs, kv{k, v})
			}
			sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v || kvs[i].v == kvs[j].v && kvs[i].k < kvs[j].k })
			b.WriteString(" New behaviour in their failures:")
			for i, x := range kvs {
				if i == 4 {
					break
				}
				fmt.Fprintf(&b, " `%s` in %d", x.k, x.v)
				if i < len(kvs)-1 && i < 3 {
					b.WriteString(",")
				}
			}
			b.WriteString(".")
		}
		b.WriteString("\n\n| task | A | B | new in B's failures |\n|---|---|---|---|\n")
		for i, t := range r.Regressed {
			if i == 10 {
				fmt.Fprintf(&b, "| ... %d more | | | |\n", len(r.Regressed)-10)
				break
			}
			fmt.Fprintf(&b, "| %s | %.0f%% | %.0f%% | %s |\n", t.Task, 100*t.RateA, 100*t.RateB, strings.Join(t.NewRules, ", "))
		}
		b.WriteString("\n")
	}
	if r.InfraExcludedA+r.InfraExcludedB > 0 {
		fmt.Fprintf(&b, "Excluded as infrastructure failures: %d trials of A, %d of B.\n\n", r.InfraExcludedA, r.InfraExcludedB)
	}
	if r.CostA > 0 || r.CostB > 0 {
		fmt.Fprintf(&b, "Agent cost: $%.2f -> $%.2f.\n", r.CostA, r.CostB)
	}
	return b.String()
}

// signedPts formats a fraction as signed points without printing "-0.0".
func signedPts(f float64) string {
	if math.Abs(f) < 5e-4 {
		return "0.0"
	}
	return fmt.Sprintf("%+.1f", 100*f)
}

// ---- sequential gate

// SequentialResult says when a group-sequential gate stopped.
type SequentialResult struct {
	Verdict    Verdict `json:"verdict"`
	Looks      int     `json:"looks_used"`
	TrialsUsed int     `json:"trials_per_task_used"`
}

// Sequential evaluates after 1, 2, ..., K trials per task and stops as soon
// as an O'Brien-Fleming boundary is crossed in either direction. Each look
// uses all trials so far. bounds from stats.SequentialBounds(K, alpha).
func Sequential(a, b trajectory.Run, K int, bounds []float64, cfg Config) SequentialResult {
	for k := 1; k <= K; k++ {
		oa, _ := Outcomes(a, k)
		ob, _ := Outcomes(b, k)
		c := stats.Paired(oa, ob, cfg.Alpha)
		if c.Tasks < 2 || math.IsInf(c.SE, 0) {
			continue
		}
		if c.T < -bounds[k-1]-cfg.FailMargin/c.SE {
			return SequentialResult{Fail, k, k}
		}
		if (c.Diff+cfg.PassMargin)/c.SE > bounds[k-1] {
			return SequentialResult{Pass, k, k}
		}
	}
	return SequentialResult{Inconclusive, K, K}
}
