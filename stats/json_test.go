package stats

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// A comparison over one shared task has SE = +Inf; it must still encode.
func TestComparisonJSONNonFinite(t *testing.T) {
	c := Paired(map[string]TaskOutcome{"x": {1, 1}}, map[string]TaskOutcome{"x": {0, 1}}, 0.05)
	if !math.IsInf(c.SE, 1) {
		t.Fatalf("precondition: SE = %v, want +Inf", c.SE)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"se":null`) || !strings.Contains(string(b), `"diff":-1`) {
		t.Fatalf("got %s", b)
	}
	var back Comparison
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tasks != 1 || back.Diff != -1 || !math.IsNaN(back.SE) {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestComparisonJSONFiniteUnchanged(t *testing.T) {
	c := Comparison{Tasks: 3, RateA: 0.5, RateB: 0.25, Diff: -0.25, SE: 0.1, T: -2.5, DF: 2, PRegress: 0.06, CILow: -0.5, CIHigh: 0}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"tasks":3,"pass1_a":0.5,"pass1_b":0.25,"diff":-0.25,"se":0.1,"t":-2.5,"df":2,"p_regression":0.06,"ci_low":-0.5,"ci_high":0}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	var back Comparison
	if err := json.Unmarshal(b, &back); err != nil || back != c {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestFloatsJSON(t *testing.T) {
	b, err := json.Marshal(Floats{1, math.NaN(), 0.5})
	if err != nil || string(b) != `[1,null,0.5]` {
		t.Fatalf("got %s %v", b, err)
	}
	var f Floats
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f) != 3 || f[0] != 1 || !math.IsNaN(f[1]) || f[2] != 0.5 {
		t.Fatalf("round trip: %v", f)
	}
}
