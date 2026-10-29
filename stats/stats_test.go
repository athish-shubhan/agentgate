package stats

import (
	"math"
	"math/rand/v2"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestDistributions(t *testing.T) {
	near(t, "Phi^-1(0.975)", NormalQuantile(0.975), 1.959964, 1e-6)
	near(t, "Phi^-1(0.05)", NormalQuantile(0.05), -1.644854, 1e-6)
	near(t, "T_10(2.0)", StudentTCDF(2.0, 10), 0.963306, 1e-5)
	near(t, "T_3(-1.5)", StudentTCDF(-1.5, 3), 0.1152919326, 1e-8) // scipy.stats.t.cdf(-1.5, 3)
	near(t, "t_0.95,20", StudentTQuantile(0.95, 20), 1.724718, 1e-5)
}

func TestPassHatK(t *testing.T) {
	tasks := map[string]TaskOutcome{"a": {4, 4}, "b": {2, 4}, "c": {0, 4}}
	near(t, "pass^1", PassHatK(tasks, 1), (1+0.5+0)/3, 1e-12)
	near(t, "pass^2", PassHatK(tasks, 2), (1+1.0/6+0)/3, 1e-12) // C(2,2)/C(4,2) = 1/6
	near(t, "pass^4", PassHatK(tasks, 4), 1.0/3, 1e-12)
}

func TestKappa(t *testing.T) {
	a := []bool{true, true, false, false, true, false, true, false}
	near(t, "kappa(a,a)", CohenKappa(a, a), 1, 1e-12)
	b := []bool{true, false, false, false, true, false, true, true}
	near(t, "kappa", CohenKappa(a, b), 0.5, 1e-12)
}

// Under no difference, the paired test's one-sided false-alarm rate must be
// close to alpha even when tasks differ wildly in difficulty.
func TestPairedCalibratedUnderNull(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	const reps, tasks, trials = 3000, 100, 4
	alarms := 0
	for i := 0; i < reps; i++ {
		a, b := map[string]TaskOutcome{}, map[string]TaskOutcome{}
		for k := 0; k < tasks; k++ {
			p := r.Float64() // per-task difficulty
			sa, sb := 0, 0
			for j := 0; j < trials; j++ {
				if r.Float64() < p {
					sa++
				}
				if r.Float64() < p {
					sb++
				}
			}
			a[string(rune(k))] = TaskOutcome{sa, trials}
			b[string(rune(k))] = TaskOutcome{sb, trials}
		}
		if Paired(a, b, 0.05).PRegress < 0.05 {
			alarms++
		}
	}
	rate := float64(alarms) / reps
	if rate < 0.03 || rate > 0.07 {
		t.Fatalf("false-alarm rate %.3f, want about 0.05", rate)
	}
}

// The sequential boundaries keep the overall false-alarm rate at alpha.
func TestSequentialBoundsControlAlpha(t *testing.T) {
	const K, alpha = 4, 0.05
	c := SequentialBounds(K, alpha)
	if !(c[0] > c[1] && c[1] > c[2] && c[2] > c[3]) {
		t.Fatalf("boundaries should shrink: %v", c)
	}
	if c[K-1] < NormalQuantile(1-alpha) {
		t.Fatalf("final boundary %v below the fixed-sample value", c[K-1])
	}
	r := rand.New(rand.NewPCG(99, 1))
	alarms := 0
	const reps = 100000
	for i := 0; i < reps; i++ {
		s := 0.0
		for k := 1; k <= K; k++ {
			s += r.NormFloat64()
			if s/math.Sqrt(float64(k)) < -c[k-1] {
				alarms++
				break
			}
		}
	}
	near(t, "sequential alpha", float64(alarms)/reps, alpha, 0.004)
}
