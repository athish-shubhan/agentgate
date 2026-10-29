// Package calibrate measures the gate on real agent runs: false alarms when
// nothing changed (A/A), detection of real model swaps, detection of
// injected regressions, and the trials a sequential gate saves.
//
// Data: the published tau2-bench result files (4 agent models x airline,
// retail, telecom variants, 4 trials per task, full trajectories).
package calibrate

import (
	"math"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"

	"agentgate/gate"
	"agentgate/graders"
	"agentgate/stats"
	"agentgate/trajectory"
)

// split returns two runs built from disjoint trials of the same run: for
// each task the 4 trials are shuffled and the first `per` go to A, the next
// `per` to B, renumbered 0..per-1 so the sequential gate can look at them in
// order.
func split(r trajectory.Run, per int, rng *rand.Rand) (trajectory.Run, trajectory.Run) {
	a := trajectory.Run{Name: r.Name + "#A", ExpectedTools: r.ExpectedTools}
	b := trajectory.Run{Name: r.Name + "#B", ExpectedTools: r.ExpectedTools}
	bt := r.ByTask()
	for _, key := range sortedKeys(bt) { // fixed order so a seed reproduces the split
		ts := bt[key]
		idx := rng.Perm(len(ts))
		for i := 0; i < per && i < len(idx); i++ {
			t := ts[idx[i]]
			t.Trial = i
			a.Trials = append(a.Trials, t)
		}
		for i := per; i < 2*per && i < len(idx); i++ {
			t := ts[idx[i]]
			t.Trial = i - per
			b.Trials = append(b.Trials, t)
		}
	}
	return a, b
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type AAResult struct {
	File            string  `json:"file"`
	Tasks           int     `json:"tasks"`
	PassRate        float64 `json:"pass_rate"`
	Reps            int     `json:"reps"`
	FalseFailPaired float64 `json:"false_fail_paired"`
	FalseFailNaive  float64 `json:"false_fail_naive"`
	PassPaired      float64 `json:"pass_paired"` // share of A/A splits the gate PASSed
}

// AA splits each file's trials 2+2 at random, many times, and counts how
// often each test calls the identical agent a regression.
func AA(files []string, reps int, cfg gate.Config, seed uint64) ([]AAResult, error) {
	var out []AAResult
	for _, f := range files {
		run, err := trajectory.LoadTau2(f, false)
		if err != nil {
			return nil, err
		}
		rng := rand.New(rand.NewPCG(seed, uint64(len(f))))
		res := AAResult{File: filepath.Base(f), Reps: reps}
		var ff, fn, ps int
		for i := 0; i < reps; i++ {
			a, b := split(run, 2, rng)
			oa, _ := gate.Outcomes(a, -1)
			ob, _ := gate.Outcomes(b, -1)
			c := stats.Paired(oa, ob, cfg.Alpha)
			res.Tasks = c.Tasks
			switch gate.Decide(c, cfg) {
			case gate.Fail:
				ff++
			case gate.Pass:
				ps++
			}
			if n := stats.NaiveUnpaired(oa, ob, cfg.Alpha); n.CIHigh < -cfg.FailMargin {
				fn++
			}
		}
		o, _ := gate.Outcomes(run, -1)
		var rates []float64
		for _, x := range o {
			rates = append(rates, x.Rate())
		}
		res.PassRate = stats.Mean(rates)
		res.FalseFailPaired = float64(ff) / float64(reps)
		res.FalseFailNaive = float64(fn) / float64(reps)
		res.PassPaired = float64(ps) / float64(reps)
		out = append(out, res)
	}
	return out, nil
}

type SwapResult struct {
	Pair    string  `json:"pair"`
	Suite   string  `json:"suite"`
	RateA   float64 `json:"rate_a"`
	RateB   float64 `json:"rate_b"`
	Verdict string  `json:"verdict_full_data"`
	CILow   float64 `json:"ci_low"`
	CIHigh  float64 `json:"ci_high"`
	// detection rate when re-running the gate on bootstrap resamples of the
	// tasks with only k trials per task (k = 1, 2, 4)
	FailRateByTrials  map[int]float64 `json:"fail_rate_by_trials"`
	NaiveFailByTrials map[int]float64 `json:"naive_fail_rate_by_trials"`
	Markdown          string          `json:"markdown"`
}

func bootstrapTasks(o map[string]stats.TaskOutcome, keys []string, pick []int) map[string]stats.TaskOutcome {
	m := make(map[string]stats.TaskOutcome, len(pick))
	for i, p := range pick {
		// distinct key per draw so resampled duplicates count separately
		m[keys[p]+"#"+itoa(i)] = o[keys[p]]
	}
	return m
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Swap compares two real runs (e.g. a model and its smaller sibling) and
// estimates how often the gate would catch the difference with fewer trials.
func Swap(baseFile, candFile string, reps int, cfg gate.Config, seed uint64) (SwapResult, error) {
	a, err := trajectory.LoadTau2(baseFile, true)
	if err != nil {
		return SwapResult{}, err
	}
	b, err := trajectory.LoadTau2(candFile, true)
	if err != nil {
		return SwapResult{}, err
	}
	rep := gate.Compare(a, b, cfg)
	r := SwapResult{Pair: a.Name + " -> " + b.Name, Suite: strings.SplitN(a.Name, "/", 2)[1], RateA: rep.Overall.RateA, RateB: rep.Overall.RateB,
		Verdict: string(rep.Verdict), CILow: rep.Overall.CILow, CIHigh: rep.Overall.CIHigh,
		FailRateByTrials: map[int]float64{}, NaiveFailByTrials: map[int]float64{}, Markdown: rep.Markdown(cfg)}
	rng := rand.New(rand.NewPCG(seed, 3))
	for _, k := range []int{1, 2, 4} {
		oa, _ := gate.Outcomes(a, k)
		ob, _ := gate.Outcomes(b, k)
		var keys []string
		for key := range oa {
			if _, ok := ob[key]; ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		fails, naive := 0, 0
		for i := 0; i < reps; i++ {
			pick := make([]int, len(keys))
			for j := range pick {
				pick[j] = rng.IntN(len(keys))
			}
			ba, bb := bootstrapTasks(oa, keys, pick), bootstrapTasks(ob, keys, pick)
			if gate.Decide(stats.Paired(ba, bb, cfg.Alpha), cfg) == gate.Fail {
				fails++
			}
			if stats.NaiveUnpaired(ba, bb, cfg.Alpha).CIHigh < -cfg.FailMargin {
				naive++
			}
		}
		r.FailRateByTrials[k] = float64(fails) / float64(reps)
		r.NaiveFailByTrials[k] = float64(naive) / float64(reps)
	}
	return r, nil
}

type InjectResult struct {
	File          string  `json:"file"`
	Injection     string  `json:"injection"`
	AffectedTasks float64 `json:"affected_tasks"`
	TrueDrop      float64 `json:"true_drop_points"` // expected pass-rate drop from the injection
	Reps          int     `json:"reps"`
	PowerPaired   float64 `json:"power_paired"`
	PowerNaive    float64 `json:"power_naive"`
	SeqPower      float64 `json:"sequential_power"`
	SeqMeanTrials float64 `json:"sequential_mean_trials_per_task"`
	FixedTrials   int     `json:"fixed_trials_per_task"`
}

// Injection degrades B for a subset of tasks: each passing trial of an
// affected task is flipped to a failure with probability p.
type Injection struct {
	Name string
	P    float64
	// Affected picks tasks; expected tools of the reference solution are
	// available (e.g. "tasks whose solution uses exchange_*").
	Affected func(task string, expected []string, rng *rand.Rand) bool
}

func DefaultInjections() []Injection {
	uses := func(prefix string) func(string, []string, *rand.Rand) bool {
		return func(_ string, exp []string, _ *rand.Rand) bool {
			for _, e := range exp {
				if strings.HasPrefix(e, prefix) {
					return true
				}
			}
			return false
		}
	}
	return []Injection{
		// a renamed parameter breaks every call to one tool family
		{Name: "tool schema change breaks exchange_* (all affected trials fail)", P: 1, Affected: uses("exchange_")},
		// a dropped policy line makes the agent wrong half the time on returns
		{Name: "dropped policy line: return_* tasks fail half the time", P: 0.5, Affected: uses("return_")},
		// a weaker model: diffuse damage across a random 20% of tasks
		{Name: "weaker model: random 20% of tasks fail half the time", P: 0.5, Affected: func(_ string, _ []string, r *rand.Rand) bool { return r.Float64() < 0.2 }},
		// a subtle change: 10% of tasks, a quarter of the time
		{Name: "subtle: random 10% of tasks fail a quarter of the time", P: 0.25, Affected: func(_ string, _ []string, r *rand.Rand) bool { return r.Float64() < 0.1 }},
	}
}

// Inject measures detection power: A and B are disjoint 2-trial halves of
// the same run, B degraded by the injection, repeated reps times.
func Inject(file string, inj Injection, reps int, cfg gate.Config, seed uint64) (InjectResult, error) {
	run, err := trajectory.LoadTau2(file, false)
	if err != nil {
		return InjectResult{}, err
	}
	rng := rand.New(rand.NewPCG(seed, 9))
	res := InjectResult{File: filepath.Base(file), Injection: inj.Name, Reps: reps, FixedTrials: 2}
	bounds := stats.SequentialBounds(2, cfg.Alpha)
	var pp, pn, sp int
	var trials, drop, aff float64
	for i := 0; i < reps; i++ {
		a, b := split(run, 2, rng)
		affected := map[string]bool{}
		for _, key := range sortedKeys(run.ExpectedTools) {
			if inj.Affected(key, run.ExpectedTools[key], rng) {
				affected[key] = true
			}
		}
		if len(run.ExpectedTools) == 0 { // fall back to tasks seen in trials
			for _, key := range sortedKeys(run.ByTask()) {
				if inj.Affected(key, nil, rng) {
					affected[key] = true
				}
			}
		}
		aff += float64(len(affected))
		oa0, _ := gate.Outcomes(b, -1)
		for j := range b.Trials {
			t := &b.Trials[j]
			if affected[t.Suite+"/"+t.Task] && t.Pass && rng.Float64() < inj.P {
				t.Pass, t.Reward = false, 0
			}
		}
		ob, _ := gate.Outcomes(b, -1)
		var d []float64
		for k, x := range oa0 {
			d = append(d, ob[k].Rate()-x.Rate())
		}
		drop += -stats.Mean(d)
		oa, _ := gate.Outcomes(a, -1)
		if gate.Decide(stats.Paired(oa, ob, cfg.Alpha), cfg) == gate.Fail {
			pp++
		}
		if stats.NaiveUnpaired(oa, ob, cfg.Alpha).CIHigh < -cfg.FailMargin {
			pn++
		}
		s := gate.Sequential(a, b, 2, bounds, cfg)
		if s.Verdict == gate.Fail {
			sp++
		}
		trials += float64(s.TrialsUsed)
	}
	res.PowerPaired = float64(pp) / float64(reps)
	res.PowerNaive = float64(pn) / float64(reps)
	res.SeqPower = float64(sp) / float64(reps)
	res.SeqMeanTrials = trials / float64(reps)
	res.TrueDrop = 100 * drop / float64(reps)
	res.AffectedTasks = aff / float64(reps)
	return res, nil
}

// CurvePoint is the gate at one trial budget, from the parametric model.
type CurvePoint struct {
	K                 int     `json:"trials_per_task"`
	PowerFixed        float64 `json:"power_fixed"`
	FalseAlarmFixed   float64 `json:"false_alarm_fixed"`
	PassUnderNull     float64 `json:"pass_rate_when_nothing_changed"`
	PowerSeq          float64 `json:"power_sequential"`
	FalseAlarmSeq     float64 `json:"false_alarm_sequential"`
	MeanTrialsSeqDrop float64 `json:"mean_trials_sequential_with_drop"`
	MeanTrialsSeqNull float64 `json:"mean_trials_sequential_no_change"`
}

// PowerCurve is a parametric check of trial budgets: per-task pass
// probabilities are taken from a real run (posterior mean with a Jeffreys
// prior), B loses `drop` of absolute pass rate on a random `frac` of tasks,
// and the gate is run with k trials per side, both as a fixed-size test and
// as a sequential test that looks after every trial (O'Brien-Fleming, K=k).
// A third arm with no change measures false alarms.
func PowerCurve(file string, frac, drop float64, ks []int, reps int, cfg gate.Config, seed uint64) ([]CurvePoint, error) {
	run, err := trajectory.LoadTau2(file, false)
	if err != nil {
		return nil, err
	}
	o, _ := gate.Outcomes(run, -1)
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ps []float64
	for _, k := range keys {
		x := o[k]
		ps = append(ps, (float64(x.Successes)+0.5)/(float64(x.Trials)+1))
	}
	rng := rand.New(rand.NewPCG(seed, 5))
	var out []CurvePoint
	for _, k := range ks {
		bounds := stats.SequentialBounds(k, cfg.Alpha)
		cp := CurvePoint{K: k}
		var hit, fa, pass0, shit, sfa int
		var tDrop, tNull float64
		for i := 0; i < reps; i++ {
			// per task, per trial outcomes for A, B (with drop) and B0 (no change)
			A := make([][]bool, len(ps))
			B := make([][]bool, len(ps))
			B0 := make([][]bool, len(ps))
			for j, p := range ps {
				pb := p
				if rng.Float64() < frac {
					pb = math.Max(0, p-drop)
				}
				A[j], B[j], B0[j] = make([]bool, k), make([]bool, k), make([]bool, k)
				for t := 0; t < k; t++ {
					A[j][t] = rng.Float64() < p
					B[j][t] = rng.Float64() < pb
					B0[j][t] = rng.Float64() < p
				}
			}
			upTo := func(x [][]bool, n int) map[string]stats.TaskOutcome {
				m := make(map[string]stats.TaskOutcome, len(x))
				for j := range x {
					s := 0
					for t := 0; t < n; t++ {
						if x[j][t] {
							s++
						}
					}
					m[itoa(j)] = stats.TaskOutcome{Successes: s, Trials: n}
				}
				return m
			}
			if gate.Decide(stats.Paired(upTo(A, k), upTo(B, k), cfg.Alpha), cfg) == gate.Fail {
				hit++
			}
			switch gate.Decide(stats.Paired(upTo(A, k), upTo(B0, k), cfg.Alpha), cfg) {
			case gate.Fail:
				fa++
			case gate.Pass:
				pass0++
			}
			seq := func(b [][]bool) (gate.Verdict, int) {
				for n := 1; n <= k; n++ {
					c := stats.Paired(upTo(A, n), upTo(b, n), cfg.Alpha)
					if c.Tasks < 2 {
						continue
					}
					if (c.Diff+cfg.FailMargin)/c.SE < -bounds[n-1] {
						return gate.Fail, n
					}
					if (c.Diff+cfg.PassMargin)/c.SE > bounds[n-1] {
						return gate.Pass, n
					}
				}
				return gate.Inconclusive, k
			}
			v, n := seq(B)
			if v == gate.Fail {
				shit++
			}
			tDrop += float64(n)
			v, n = seq(B0)
			if v == gate.Fail {
				sfa++
			}
			tNull += float64(n)
		}
		r := float64(reps)
		cp.PowerFixed, cp.FalseAlarmFixed, cp.PassUnderNull = float64(hit)/r, float64(fa)/r, float64(pass0)/r
		cp.PowerSeq, cp.FalseAlarmSeq = float64(shit)/r, float64(sfa)/r
		cp.MeanTrialsSeqDrop, cp.MeanTrialsSeqNull = tDrop/r, tNull/r
		out = append(out, cp)
	}
	return out, nil
}

// Reliability describes how repeatable a run is: the share of tasks whose
// trials disagree, test-retest agreement between trials of the same task
// (Cohen's kappa averaged over trial pairs), pass^k decay, and cost.
type Reliability struct {
	File            string    `json:"file"`
	Tasks           int       `json:"tasks"`
	Pass1           float64   `json:"pass1"`
	PassK           []float64 `json:"pass_k"`
	MixedTasks      float64   `json:"mixed_task_share"`
	TestRetestKappa float64   `json:"test_retest_kappa"`
	CostPerTrial    float64   `json:"agent_cost_usd_per_trial"`
	SecPerTrial     float64   `json:"seconds_per_trial"`
	InfraTrials     int       `json:"infra_trials"`
	Trials          int       `json:"trials"`
}

func Reliable(file string) (Reliability, error) {
	run, err := trajectory.LoadTau2(file, false)
	if err != nil {
		return Reliability{}, err
	}
	r := Reliability{File: filepath.Base(file), Trials: len(run.Trials)}
	o, infra := gate.Outcomes(run, -1)
	r.InfraTrials = infra
	r.Tasks = len(o)
	mixed := 0
	var rates []float64
	maxN := 0
	for _, x := range o {
		rates = append(rates, x.Rate())
		if x.Successes > 0 && x.Successes < x.Trials {
			mixed++
		}
		if x.Trials > maxN {
			maxN = x.Trials
		}
	}
	r.Pass1 = stats.Mean(rates)
	r.MixedTasks = float64(mixed) / float64(len(o))
	for k := 1; k <= maxN; k++ {
		r.PassK = append(r.PassK, stats.PassHatK(o, k))
	}
	// kappa between trial i and trial j over tasks that have both
	byTask := run.ByTask()
	var ks []float64
	for i := 0; i < maxN; i++ {
		for j := i + 1; j < maxN; j++ {
			var a, b []bool
			for _, ts := range byTask {
				var ti, tj *trajectory.Trial
				for x := range ts {
					if ts[x].Trial == i && !ts[x].Infra {
						ti = &ts[x]
					}
					if ts[x].Trial == j && !ts[x].Infra {
						tj = &ts[x]
					}
				}
				if ti != nil && tj != nil {
					a, b = append(a, ti.Pass), append(b, tj.Pass)
				}
			}
			if len(a) > 1 {
				ks = append(ks, stats.CohenKappa(a, b))
			}
		}
	}
	r.TestRetestKappa = stats.Mean(ks)
	var cost, sec float64
	for _, t := range run.Trials {
		cost += t.CostUSD
		sec += t.DurationSec
	}
	r.CostPerTrial = cost / float64(len(run.Trials))
	r.SecPerTrial = sec / float64(len(run.Trials))
	return r, nil
}

// RuleStat says how a trajectory rule relates to the task reward on real
// trials: how often it fires and the failure rate with and without it. A
// rule that fires often but does not move the failure rate is noise in a PR
// comment; one with a large lift is worth showing.
type RuleStat struct {
	Rule          string  `json:"rule"`
	Fired         int     `json:"trials_fired"`
	Trials        int     `json:"trials"`
	FailWhenFired float64 `json:"fail_rate_when_fired"`
	FailOtherwise float64 `json:"fail_rate_otherwise"`
}

func RuleValidity(files []string, rules graders.Rules) ([]RuleStat, error) {
	type acc struct{ fired, firedFail, other, otherFail int }
	m := map[string]*acc{}
	total := 0
	var all []string
	var trials [][]string
	var fails []bool
	for _, f := range files {
		run, err := trajectory.LoadTau2(f, true)
		if err != nil {
			return nil, err
		}
		for _, t := range run.Trials {
			if t.Infra {
				continue
			}
			total++
			seen := map[string]bool{}
			var rs []string
			for _, v := range graders.Grade(rules, t) {
				r := v.Rule
				if !seen[r] {
					seen[r] = true
					rs = append(rs, r)
					if m[r] == nil {
						m[r] = &acc{}
						all = append(all, r)
					}
				}
			}
			trials = append(trials, rs)
			fails = append(fails, !t.Pass)
		}
	}
	for i, rs := range trials {
		set := map[string]bool{}
		for _, r := range rs {
			set[r] = true
		}
		for _, r := range all {
			a := m[r]
			if set[r] {
				a.fired++
				if fails[i] {
					a.firedFail++
				}
			} else {
				a.other++
				if fails[i] {
					a.otherFail++
				}
			}
		}
	}
	sort.Strings(all)
	var out []RuleStat
	for _, r := range all {
		a := m[r]
		s := RuleStat{Rule: r, Fired: a.fired, Trials: total}
		if a.fired > 0 {
			s.FailWhenFired = float64(a.firedFail) / float64(a.fired)
		}
		if a.other > 0 {
			s.FailOtherwise = float64(a.otherFail) / float64(a.other)
		}
		out = append(out, s)
	}
	return out, nil
}
