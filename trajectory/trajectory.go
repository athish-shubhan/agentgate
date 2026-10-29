// Package trajectory is the agent-run data model the gate works on, and a
// loader for tau2-bench result files (github.com/sierra-research/tau2-bench).
package trajectory

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ToolCall is one tool invocation by the agent.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

// Message is one turn of a trajectory.
type Message struct {
	Role      string     `json:"role"` // assistant, user, tool
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolError bool       `json:"tool_error,omitempty"` // for role=tool
}

// Trial is one run of one task.
type Trial struct {
	Suite       string    `json:"suite"`
	Task        string    `json:"task"`
	Trial       int       `json:"trial"`
	Reward      float64   `json:"reward"`
	Pass        bool      `json:"pass"`
	Termination string    `json:"termination"`
	CostUSD     float64   `json:"cost_usd"`
	DurationSec float64   `json:"duration_sec"`
	Infra       bool      `json:"infra_error,omitempty"` // failed for reasons outside the agent
	Messages    []Message `json:"messages,omitempty"`
}

// Run is everything one agent version produced on a suite.
type Run struct {
	Name   string  `json:"name"`
	Trials []Trial `json:"trials"`
	// ExpectedTools lists, per suite/task, the tools the reference solution
	// calls (from tau2-bench evaluation criteria), when known.
	ExpectedTools map[string][]string `json:"expected_tools,omitempty"`
}

// ByTask groups trials per task.
func (r Run) ByTask() map[string][]Trial {
	m := map[string][]Trial{}
	for _, t := range r.Trials {
		m[t.Suite+"/"+t.Task] = append(m[t.Suite+"/"+t.Task], t)
	}
	for k := range m {
		sort.Slice(m[k], func(i, j int) bool { return m[k][i].Trial < m[k][j].Trial })
	}
	return m
}

// ---- tau2-bench

type tauFile struct {
	Tasks []struct {
		ID                 string `json:"id"`
		EvaluationCriteria *struct {
			Actions []struct {
				Name string `json:"name"`
			} `json:"actions"`
		} `json:"evaluation_criteria"`
	} `json:"tasks"`
	Info struct {
		NumTrials int `json:"num_trials"`
		AgentInfo struct {
			LLM string `json:"llm"`
		} `json:"agent_info"`
		EnvironmentInfo struct {
			DomainName string `json:"domain_name"`
		} `json:"environment_info"`
	} `json:"info"`
	Simulations []struct {
		TaskID      string  `json:"task_id"`
		Trial       int     `json:"trial"`
		Duration    float64 `json:"duration"`
		Termination string  `json:"termination_reason"`
		AgentCost   float64 `json:"agent_cost"`
		RewardInfo  *struct {
			Reward float64 `json:"reward"`
		} `json:"reward_info"`
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"tool_calls"`
			Error any `json:"error"`
		} `json:"messages"`
	} `json:"simulations"`
}

// infraTerminations are endings that say nothing about the agent's quality.
var infraTerminations = map[string]bool{"infrastructure_error": true, "agent_error_api": true, "user_error": true}

// LoadTau2 reads a tau2-bench results JSON. A trial passes when its reward is
// 1 (tau2-bench rewards are 0/1 products of checks). keepMessages=false
// drops trajectories to save memory when only rewards are needed.
func LoadTau2(path string, keepMessages bool) (Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Run{}, err
	}
	r, err := ParseTau2(b, keepMessages)
	if err != nil {
		return Run{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// ParseTau2 is LoadTau2 on bytes already in memory.
func ParseTau2(b []byte, keepMessages bool) (Run, error) {
	var f tauFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Run{}, err
	}
	if f.Info.EnvironmentInfo.DomainName == "" && len(f.Simulations) == 0 {
		return Run{}, fmt.Errorf("not a tau2-bench results file")
	}
	suite := f.Info.EnvironmentInfo.DomainName
	run := Run{Name: f.Info.AgentInfo.LLM + "/" + suite, ExpectedTools: map[string][]string{}}
	for _, t := range f.Tasks {
		if t.EvaluationCriteria == nil {
			continue
		}
		for _, a := range t.EvaluationCriteria.Actions {
			run.ExpectedTools[suite+"/"+t.ID] = append(run.ExpectedTools[suite+"/"+t.ID], a.Name)
		}
	}
	for _, s := range f.Simulations {
		t := Trial{Suite: suite, Task: s.TaskID, Trial: s.Trial, Termination: s.Termination,
			CostUSD: s.AgentCost, DurationSec: s.Duration, Infra: infraTerminations[s.Termination]}
		if s.RewardInfo != nil {
			t.Reward = s.RewardInfo.Reward
		}
		t.Pass = t.Reward >= 1-1e-9
		if keepMessages {
			for _, m := range s.Messages {
				msg := Message{Role: m.Role}
				if c, ok := m.Content.(string); ok {
					msg.Content = c
				}
				for _, tc := range m.ToolCalls {
					msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Arguments})
				}
				if m.Role == "tool" {
					switch e := m.Error.(type) {
					case bool:
						msg.ToolError = e
					case string:
						msg.ToolError = strings.EqualFold(e, "true")
					}
				}
				t.Messages = append(t.Messages, msg)
			}
		}
		run.Trials = append(run.Trials, t)
	}
	return run, nil
}
