package stats

import (
	"encoding/json"
	"math"
)

// encoding/json refuses NaN and ±Inf, and both occur legitimately here:
// pass^k is undefined when no task has k trials, and a comparison over fewer
// than two shared tasks has an infinite standard error. Without the methods
// below, marshalling such a report fails and callers that ignore the error
// write an empty file or an empty HTTP body. Non-finite values are written as
// null and read back as NaN.

func finite(x float64) *float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return nil
	}
	return &x
}

func orNaN(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// Floats is a []float64 whose non-finite entries encode as JSON null.
type Floats []float64

func (f Floats) MarshalJSON() ([]byte, error) {
	if f == nil {
		return []byte("null"), nil
	}
	out := make([]*float64, len(f))
	for i, x := range f {
		out[i] = finite(x)
	}
	return json.Marshal(out)
}

func (f *Floats) UnmarshalJSON(b []byte) error {
	var in []*float64
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	if in == nil {
		*f = nil
		return nil
	}
	*f = make(Floats, len(in))
	for i, p := range in {
		(*f)[i] = orNaN(p)
	}
	return nil
}

type comparisonJSON struct {
	Tasks    int      `json:"tasks"`
	RateA    *float64 `json:"pass1_a"`
	RateB    *float64 `json:"pass1_b"`
	Diff     *float64 `json:"diff"`
	SE       *float64 `json:"se"`
	T        *float64 `json:"t"`
	DF       int      `json:"df"`
	PRegress *float64 `json:"p_regression"`
	CILow    *float64 `json:"ci_low"`
	CIHigh   *float64 `json:"ci_high"`
}

// MarshalJSON writes non-finite fields (for example SE with fewer than two
// tasks) as null.
func (c Comparison) MarshalJSON() ([]byte, error) {
	return json.Marshal(comparisonJSON{
		Tasks: c.Tasks, RateA: finite(c.RateA), RateB: finite(c.RateB), Diff: finite(c.Diff),
		SE: finite(c.SE), T: finite(c.T), DF: c.DF, PRegress: finite(c.PRegress),
		CILow: finite(c.CILow), CIHigh: finite(c.CIHigh),
	})
}

// UnmarshalJSON reads null fields back as NaN.
func (c *Comparison) UnmarshalJSON(b []byte) error {
	var j comparisonJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*c = Comparison{
		Tasks: j.Tasks, RateA: orNaN(j.RateA), RateB: orNaN(j.RateB), Diff: orNaN(j.Diff),
		SE: orNaN(j.SE), T: orNaN(j.T), DF: j.DF, PRegress: orNaN(j.PRegress),
		CILow: orNaN(j.CILow), CIHigh: orNaN(j.CIHigh),
	}
	return nil
}
