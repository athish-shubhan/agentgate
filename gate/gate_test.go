package gate

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"agentgate/stats"
	"agentgate/trajectory"
)

// run draws n trials per task with per-task pass probability p[i] - drop.
func run(name string, p []float64, drop float64, n int, seed uint64) trajectory.Run {
	r := rand.New(rand.NewPCG(seed, 1))
	out := trajectory.Run{Name: name}
	for i, pi := range p {
		for k := 0; k < n; k++ {
			pass := r.Float64() < pi-drop
			out.Trials = append(out.Trials, trajectory.Trial{Suite: "s", Task: fmt.Sprint(i), Trial: k, Pass: pass})
		}
	}
	return out
}

func probs(n int, seed uint64) []float64 {
	r := rand.New(rand.NewPCG(seed, 2))
	p := make([]float64, n)
	for i := range p {
		p[i] = 0.2 + 0.8*r.Float64()
	}
	return p
}

func TestDecideBoundaries(t *testing.T) {
	cfg := DefaultConfig()
	if v := Decide(stats.Comparison{Tasks: 50, CILow: -0.10, CIHigh: -0.01}, cfg); v != Fail {
		t.Fatal(v)
	}
	if v := Decide(stats.Comparison{Tasks: 50, CILow: -0.04, CIHigh: 0.03}, cfg); v != Pass {
		t.Fatal(v)
	}
	if v := Decide(stats.Comparison{Tasks: 50, CILow: -0.08, CIHigh: 0.02}, cfg); v != Inconclusive {
		t.Fatal(v)
	}
	if v := Decide(stats.Comparison{Tasks: 1, CILow: -1, CIHigh: -0.9}, cfg); v != Inconclusive {
		t.Fatal("one task must never decide", v)
	}
}

func TestCompareCatchesLargeDropAndExcludesInfra(t *testing.T) {
	p := probs(200, 1)
	a := run("a", p, 0, 4, 2)
	b := run("b", p, 0.3, 4, 3)
	b.Trials[0].Infra, b.Trials[0].Pass = true, false
	rep := Compare(a, b, DefaultConfig())
	if rep.Verdict != Fail || rep.InfraExcludedB != 1 {
		t.Fatalf("verdict %s infra %d", rep.Verdict, rep.InfraExcludedB)
	}
	if len(rep.PassKA) != 4 || rep.PassKA[3] > rep.PassKA[0] {
		t.Fatalf("pass^k must not increase with k: %v", rep.PassKA)
	}
}

func TestSequentialStopsEarlyOnLargeDrop(t *testing.T) {
	p := probs(200, 4)
	a := run("a", p, 0, 4, 5)
	b := run("b", p, 0.4, 4, 6)
	s := Sequential(a, b, 4, stats.SequentialBounds(4, 0.05), DefaultConfig())
	if s.Verdict != Fail || s.TrialsUsed > 2 {
		t.Fatalf("%+v", s)
	}
}

// The full gate's false-FAIL rate on identical agents stays near alpha.
func TestAAFalseAlarmNearAlpha(t *testing.T) {
	p := probs(100, 7)
	fails := 0
	const reps = 400
	for i := 0; i < reps; i++ {
		a := run("a", p, 0, 2, uint64(1000+2*i))
		b := run("b", p, 0, 2, uint64(1001+2*i))
		if Compare(a, b, DefaultConfig()).Verdict == Fail {
			fails++
		}
	}
	if rate := float64(fails) / reps; rate > 0.09 {
		t.Fatalf("A/A false FAIL rate %.3f", rate)
	}
}
