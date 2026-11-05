package calibrate

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"agentgate/gate"
)

// writeTau2 writes a synthetic tau2-bench results file: `tasks` tasks with 4
// trials each, task i passing each trial with probability p[i] (drawn with
// seed). Tasks with even index have an exchange_* tool in their reference
// solution, odd ones a return_* tool.
func writeTau2(t *testing.T, dir, name string, tasks int, scale float64, seed uint64) string {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 1))
	type action struct {
		Name string `json:"name"`
	}
	type task struct {
		ID   string `json:"id"`
		Eval struct {
			Actions []action `json:"actions"`
		} `json:"evaluation_criteria"`
	}
	type sim struct {
		TaskID string  `json:"task_id"`
		Trial  int     `json:"trial"`
		Term   string  `json:"termination_reason"`
		Cost   float64 `json:"agent_cost"`
		Dur    float64 `json:"duration"`
		Reward struct {
			Reward float64 `json:"reward"`
		} `json:"reward_info"`
	}
	var f struct {
		Tasks []task `json:"tasks"`
		Info  struct {
			NumTrials int `json:"num_trials"`
			Agent     struct {
				LLM string `json:"llm"`
			} `json:"agent_info"`
			Env struct {
				Domain string `json:"domain_name"`
			} `json:"environment_info"`
		} `json:"info"`
		Sims []sim `json:"simulations"`
	}
	f.Info.NumTrials, f.Info.Agent.LLM, f.Info.Env.Domain = 4, name, "retail"
	pr := rand.New(rand.NewPCG(99, 2)) // per-task difficulty shared by every file
	for i := 0; i < tasks; i++ {
		id := fmt.Sprint(i)
		tk := task{ID: id}
		tool := "return_items"
		if i%2 == 0 {
			tool = "exchange_items"
		}
		tk.Eval.Actions = []action{{"find_user"}, {tool}}
		f.Tasks = append(f.Tasks, tk)
		p := scale * (0.2 + 0.8*pr.Float64())
		for k := 0; k < 4; k++ {
			s := sim{TaskID: id, Trial: k, Term: "user_stop", Cost: 0.01, Dur: 10}
			if r.Float64() < p {
				s.Reward.Reward = 1
			}
			f.Sims = append(f.Sims, s)
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"_retail_default_4trials.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The A/A result must not depend on where the data lives. It used to be
// seeded with len(path), so cloning tau2-bench into another directory
// changed the published A/A numbers.
func TestAAIndependentOfDirectory(t *testing.T) {
	base := t.TempDir()
	short := writeTau2(t, filepath.Join(base, "a"), "m", 60, 1, 1)
	long := writeTau2(t, filepath.Join(base, "a", "much", "longer", "directory", "name"), "m", 60, 1, 1)
	cfg := gate.DefaultConfig()
	r1, err := AA([]string{short}, 50, cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := AA([]string{long}, 50, cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r1[0] != r2[0] {
		t.Fatalf("same data, different directory:\n%+v\n%+v", r1[0], r2[0])
	}
}

func TestAAFalseAlarmsNearAlpha(t *testing.T) {
	f := writeTau2(t, t.TempDir(), "m", 120, 1, 2)
	res, err := AA([]string{f}, 300, gate.DefaultConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	r := res[0]
	if r.Tasks != 120 || r.Reps != 300 {
		t.Fatalf("%+v", r)
	}
	if r.FalseFailPaired > 0.12 {
		t.Fatalf("A/A false FAIL rate %.3f for alpha 0.05", r.FalseFailPaired)
	}
	if r.FalseFailNaive > r.FalseFailPaired+0.05 {
		t.Fatalf("naive test should not alarm more than the paired one here: %+v", r)
	}
}

// Every trial of the exchange_* tasks failing (half the suite) must be caught
// nearly always, with the sequential gate stopping early.
func TestInjectToolBreakIsCaught(t *testing.T) {
	f := writeTau2(t, t.TempDir(), "m", 80, 1, 3)
	inj := DefaultInjections()[0]
	res, err := Inject(f, inj, 100, gate.DefaultConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.AffectedTasks != 40 {
		t.Fatalf("affected %v, want the 40 exchange_* tasks", res.AffectedTasks)
	}
	if res.PowerPaired < 0.95 || res.SeqPower < 0.95 || res.TrueDrop < 20 {
		t.Fatalf("%+v", res)
	}
	if res.SeqMeanTrials < 1 || res.SeqMeanTrials > 2 {
		t.Fatalf("sequential trials per task %v outside [1, 2]", res.SeqMeanTrials)
	}
	again, _ := Inject(f, inj, 100, gate.DefaultConfig(), 1)
	if again != res {
		t.Fatalf("same seed, different result:\n%+v\n%+v", res, again)
	}
}

func TestSwapDetectsWorseModel(t *testing.T) {
	dir := t.TempDir()
	a := writeTau2(t, dir, "big", 100, 1, 4)
	b := writeTau2(t, dir, "small", 100, 0.6, 5)
	r, err := Swap(a, b, 100, gate.DefaultConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "FAIL" || r.Suite != "retail" || r.RateB >= r.RateA || r.CIHigh >= 0 {
		t.Fatalf("%+v", r)
	}
	if r.FailRateByTrials[4] < r.FailRateByTrials[1] || r.FailRateByTrials[4] < 0.9 {
		t.Fatalf("detection should grow with trials and be near 1 at 4: %v", r.FailRateByTrials)
	}
}

func TestReliable(t *testing.T) {
	f := writeTau2(t, t.TempDir(), "m", 50, 1, 6)
	r, err := Reliable(f)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tasks != 50 || r.Trials != 200 || r.InfraTrials != 0 || math.Abs(r.CostPerTrial-0.01) > 1e-12 || math.Abs(r.SecPerTrial-10) > 1e-9 {
		t.Fatalf("%+v", r)
	}
	if len(r.PassK) != 4 || r.PassK[0] != r.Pass1 || r.PassK[3] > r.PassK[0] {
		t.Fatalf("pass^k must start at pass^1 and not increase: %v (pass1 %v)", r.PassK, r.Pass1)
	}
	if r.MixedTasks <= 0 || r.MixedTasks >= 1 {
		t.Fatalf("mixed share %v", r.MixedTasks)
	}
}
