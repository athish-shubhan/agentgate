package trajectory

import "testing"

const tau2Snippet = `{
 "info": {"num_trials": 2, "agent_info": {"llm": "m"}, "environment_info": {"domain_name": "retail"}},
 "tasks": [{"id": "7", "evaluation_criteria": {"actions": [{"name": "find_user"}, {"name": "return_items"}]}}],
 "simulations": [
  {"task_id": "7", "trial": 0, "termination_reason": "user_stop", "agent_cost": 0.1, "duration": 30,
   "reward_info": {"reward": 1.0},
   "messages": [{"role": "user", "content": "hi"},
                {"role": "assistant", "content": null, "tool_calls": [{"id": "a", "name": "find_user", "arguments": {"email": "x"}}]},
                {"role": "tool", "content": "not found", "error": true}]},
  {"task_id": "7", "trial": 1, "termination_reason": "infrastructure_error", "reward_info": {"reward": 0.0}, "messages": []}
 ]}`

func TestParseTau2(t *testing.T) {
	r, err := ParseTau2([]byte(tau2Snippet), true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "m/retail" || len(r.Trials) != 2 {
		t.Fatalf("%+v", r)
	}
	t0, t1 := r.Trials[0], r.Trials[1]
	if !t0.Pass || t0.CostUSD != 0.1 || len(t0.Messages) != 3 || !t0.Messages[2].ToolError || t0.Messages[1].ToolCalls[0].Name != "find_user" {
		t.Fatalf("trial 0: %+v", t0)
	}
	if t1.Pass || !t1.Infra {
		t.Fatalf("trial 1 should be an infra failure: %+v", t1)
	}
	if got := r.ExpectedTools["retail/7"]; len(got) != 2 || got[1] != "return_items" {
		t.Fatalf("expected tools %v", got)
	}
	if _, err := ParseTau2([]byte(`{"x":1}`), false); err == nil {
		t.Fatal("garbage should not parse as tau2")
	}
}
