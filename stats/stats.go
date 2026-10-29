// Package stats holds the statistics behind the gate: a paired, task-
// clustered comparison of two agent versions, pass^k, group-sequential
// boundaries, power analysis, and inter-rater agreement.
//
// Why paired and clustered (Miller, "Adding Error Bars to Evals", 2024):
// trials of the same task are correlated (a hard task is hard for every
// trial), and the two versions run on the same tasks. The unit of
// independence is the task, so the comparison is over per-task differences
// in success rate, and its standard error comes from how those differences
// vary across tasks. Treating every trial as an independent coin flip, or
// comparing the two versions as unpaired samples, gets the variance wrong.
package stats

import (
	"math"
	"math/rand/v2"
	"sort"
)

// TaskOutcome is one task's trials for one version.
type TaskOutcome struct {
	Successes int
	Trials    int
}

func (t TaskOutcome) Rate() float64 {
	if t.Trials == 0 {
		return math.NaN()
	}
	return float64(t.Successes) / float64(t.Trials)
}

// Comparison is the result of a paired test of B against A.
type Comparison struct {
	Tasks    int     `json:"tasks"`
	RateA    float64 `json:"pass1_a"`
	RateB    float64 `json:"pass1_b"`
	Diff     float64 `json:"diff"` // mean over tasks of rate_B - rate_A
	SE       float64 `json:"se"`
	T        float64 `json:"t"`
	DF       int     `json:"df"`
	PRegress float64 `json:"p_regression"` // one-sided P(diff this low | no regression)
	CILow    float64 `json:"ci_low"`       // two-sided (1 - 2 alpha) interval
	CIHigh   float64 `json:"ci_high"`
}

// Paired compares B to A over the tasks both ran. alpha is one-sided.
func Paired(a, b map[string]TaskOutcome, alpha float64) Comparison {
	var ds, ra, rb []float64
	for k, x := range a {
		y, ok := b[k]
		if !ok || x.Trials == 0 || y.Trials == 0 {
			continue
		}
		ra = append(ra, x.Rate())
		rb = append(rb, y.Rate())
		ds = append(ds, y.Rate()-x.Rate())
	}
	c := Comparison{Tasks: len(ds), RateA: Mean(ra), RateB: Mean(rb), Diff: Mean(ds), DF: len(ds) - 1}
	if len(ds) < 2 {
		c.SE = math.Inf(1)
		c.PRegress = 0.5
		return c
	}
	c.SE = StdDev(ds) / math.Sqrt(float64(len(ds)))
	if c.SE == 0 {
		// identical per-task differences: no variance to speak of
		c.SE = 1e-12
	}
	c.T = c.Diff / c.SE
	c.PRegress = StudentTCDF(c.T, float64(c.DF))
	q := StudentTQuantile(1-alpha, float64(c.DF))
	c.CILow, c.CIHigh = c.Diff-q*c.SE, c.Diff+q*c.SE
	return c
}

// NaiveUnpaired treats every trial as an independent Bernoulli draw and the
// two versions as independent samples: the textbook two-proportion z-test.
// It is here as the baseline the paired test is compared against.
func NaiveUnpaired(a, b map[string]TaskOutcome, alpha float64) Comparison {
	var sa, na, sb, nb int
	tasks := 0
	for k, x := range a {
		y, ok := b[k]
		if !ok {
			continue
		}
		tasks++
		sa += x.Successes
		na += x.Trials
		sb += y.Successes
		nb += y.Trials
	}
	pa, pb := float64(sa)/float64(na), float64(sb)/float64(nb)
	se := math.Sqrt(pa*(1-pa)/float64(na) + pb*(1-pb)/float64(nb))
	if se == 0 {
		se = 1e-12
	}
	c := Comparison{Tasks: tasks, RateA: pa, RateB: pb, Diff: pb - pa, SE: se, DF: -1}
	c.T = c.Diff / se
	c.PRegress = NormalCDF(c.T)
	q := NormalQuantile(1 - alpha)
	c.CILow, c.CIHigh = c.Diff-q*se, c.Diff+q*se
	return c
}

// PassHatK is the unbiased estimate of P(all k trials of a task pass),
// averaged over tasks: C(c, k) / C(n, k) per task with c passes of n trials
// (the tau-bench definition). Tasks with fewer than k trials are skipped.
func PassHatK(tasks map[string]TaskOutcome, k int) float64 {
	var s float64
	n := 0
	for _, t := range tasks {
		if t.Trials < k {
			continue
		}
		s += binom(t.Successes, k) / binom(t.Trials, k)
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return s / float64(n)
}

func binom(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	r := 1.0
	for i := 0; i < k; i++ {
		r *= float64(n-i) / float64(i+1)
	}
	return r
}

// MDE is the smallest drop in pass rate the paired test detects with the
// given power at one-sided level alpha, for a per-task difference standard
// deviation sdDiff over n tasks.
func MDE(sdDiff float64, n int, alpha, power float64) float64 {
	return (NormalQuantile(1-alpha) + NormalQuantile(power)) * sdDiff / math.Sqrt(float64(n))
}

// SequentialBounds returns one-sided critical values c_1..c_K for K equally
// spaced looks such that P(any Z_k < -c_k | no difference) = alpha, with
// O'Brien-Fleming shape c_k = c * sqrt(K/k) (strict early, close to the
// fixed-sample value at the end). Found by Monte Carlo on the canonical
// joint distribution of the look statistics (independent increments), with
// a fixed seed so the boundaries are reproducible.
func SequentialBounds(K int, alpha float64) []float64 {
	if K <= 1 {
		return []float64{NormalQuantile(1 - alpha)}
	}
	const sims = 200000
	r := rand.New(rand.NewPCG(7, 11))
	// minimum over looks of Z_k * sqrt(k/K) for each simulated path; the
	// boundary is crossed at some look iff that minimum is below -c.
	mins := make([]float64, sims)
	for i := range mins {
		s := 0.0
		m := math.Inf(1)
		for k := 1; k <= K; k++ {
			s += r.NormFloat64()
			z := s / math.Sqrt(float64(k))
			if v := z * math.Sqrt(float64(k)/float64(K)); v < m {
				m = v
			}
		}
		mins[i] = m
	}
	sort.Float64s(mins)
	c := -mins[int(alpha*sims)]
	out := make([]float64, K)
	for k := 1; k <= K; k++ {
		out[k-1] = c * math.Sqrt(float64(K)/float64(k))
	}
	return out
}

// CohenKappa is agreement between two raters on binary labels beyond chance.
func CohenKappa(a, b []bool) float64 {
	n := float64(len(a))
	if n == 0 || len(a) != len(b) {
		return math.NaN()
	}
	var agree, pa, pb float64
	for i := range a {
		if a[i] == b[i] {
			agree++
		}
		if a[i] {
			pa++
		}
		if b[i] {
			pb++
		}
	}
	po := agree / n
	pe := (pa/n)*(pb/n) + (1-pa/n)*(1-pb/n)
	if pe == 1 {
		return 1
	}
	return (po - pe) / (1 - pe)
}

// ---- small numeric helpers (no external dependencies)

func Mean(x []float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func StdDev(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m := Mean(x)
	s := 0.0
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return math.Sqrt(s / float64(len(x)-1))
}

func NormalCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

// NormalQuantile inverts NormalCDF (Acklam's rational approximation refined by
// one Newton step; error below 1e-9).
func NormalQuantile(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	var x float64
	switch {
	case p < 0.02425:
		q := math.Sqrt(-2 * math.Log(p))
		x = (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > 1-0.02425:
		q := math.Sqrt(-2 * math.Log(1-p))
		x = -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	default:
		q := p - 0.5
		r := q * q
		x = (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q / (((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	}
	e := NormalCDF(x) - p
	u := e * math.Sqrt(2*math.Pi) * math.Exp(x*x/2)
	return x - u/(1+x*u/2)
}

// StudentTCDF via the regularized incomplete beta function.
func StudentTCDF(t, df float64) float64 {
	if df <= 0 {
		return NormalCDF(t)
	}
	x := df / (df + t*t)
	ib := 0.5 * regIncBeta(df/2, 0.5, x)
	if t > 0 {
		return 1 - ib
	}
	return ib
}

func StudentTQuantile(p, df float64) float64 {
	if df <= 0 {
		return NormalQuantile(p)
	}
	lo, hi := -50.0, 50.0
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		if StudentTCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// regIncBeta is I_x(a, b) by Lentz's continued fraction.
func regIncBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	lgab, _ := math.Lgamma(a + b)
	front := math.Exp(lgab - lga - lgb + a*math.Log(x) + b*math.Log(1-x))
	if x > (a+1)/(a+b+2) {
		return 1 - regIncBeta(b, a, 1-x)
	}
	const tiny = 1e-300
	f, c, d := 1.0, 1.0, 0.0
	for i := 0; i <= 400; i++ {
		m := float64(i / 2)
		var num float64
		switch {
		case i == 0:
			num = 1
		case i%2 == 0:
			num = m * (b - m) * x / ((a + 2*m - 1) * (a + 2*m))
		default:
			num = -((a + m) * (a + b + m) * x) / ((a + 2*m) * (a + 2*m + 1))
		}
		d = 1 + num*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		d = 1 / d
		c = 1 + num/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		cd := c * d
		f *= cd
		if math.Abs(1-cd) < 1e-12 {
			break
		}
	}
	return front * (f - 1) / a
}
