// Package graders checks trajectories against deterministic rules. The gate's
// pass/fail decision uses the task reward (a final-state check such as
// tau2-bench's database match); these rules explain *why* a version got
// worse: which behaviours appear in the new version's failures.
package graders

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentgate/trajectory"
)

type Rules struct {
	MaxAssistantTurns int      `json:"max_assistant_turns"` // step budget
	LoopThreshold     int      `json:"loop_threshold"`      // same tool + same args this many times
	WritePrefixes     []string `json:"write_tool_prefixes"` // consequential tools
	ConfirmWords      []string `json:"confirm_words"`       // what counts as the user agreeing
	FlagTools         []string `json:"flag_tools"`          // tools worth surfacing (e.g. human handoff)
	MaxToolErrors     int      `json:"max_tool_errors"`
}

func DefaultRules() Rules {
	return Rules{MaxAssistantTurns: 30, LoopThreshold: 3, MaxToolErrors: 2,
		WritePrefixes: []string{"cancel_", "modify_", "exchange_", "return_", "book_", "update_", "toggle_", "enable_", "disable_", "refuel_", "send_"},
		ConfirmWords:  []string{"yes", "confirm", "go ahead", "proceed", "sure", "correct", "please do", "that's right", "ok", "okay", "do it"},
		FlagTools:     []string{"transfer_to_human_agents"}}
}

type Violation struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

// Grade returns every rule a trial breaks.
func Grade(r Rules, t trajectory.Trial) []Violation {
	var out []Violation
	turns := 0
	seen := map[string]int{}
	errs := 0
	lastUser := ""
	for _, m := range t.Messages {
		switch m.Role {
		case "user":
			lastUser = strings.ToLower(m.Content)
		case "tool":
			if m.ToolError {
				errs++
			}
		case "assistant":
			turns++
			for _, tc := range m.ToolCalls {
				key := tc.Name + string(canonical(tc.Args))
				seen[key]++
				if seen[key] == r.LoopThreshold {
					out = append(out, Violation{"loop", fmt.Sprintf("%s called %d times with the same arguments", tc.Name, r.LoopThreshold)})
				}
				for _, f := range r.FlagTools {
					if tc.Name == f {
						out = append(out, Violation{"flag:" + f, "called " + f})
					}
				}
				if isWrite(r, tc.Name) && !confirmed(r, lastUser) {
					out = append(out, Violation{"write_without_confirmation", tc.Name + " without an explicit yes in the previous user turn"})
				}
			}
		}
	}
	if r.MaxAssistantTurns > 0 && turns > r.MaxAssistantTurns {
		out = append(out, Violation{"step_budget", fmt.Sprintf("%d assistant turns > %d", turns, r.MaxAssistantTurns)})
	}
	if r.MaxToolErrors > 0 && errs > r.MaxToolErrors {
		out = append(out, Violation{"tool_errors", fmt.Sprintf("%d tool errors", errs)})
	}
	if t.Termination == "max_steps" {
		out = append(out, Violation{"max_steps", "hit the harness step limit"})
	}
	return out
}

func isWrite(r Rules, name string) bool {
	for _, p := range r.WritePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// confirmed matches whole words or phrases ("ok" must not match "book").
func confirmed(r Rules, user string) bool {
	norm := " " + strings.Join(strings.FieldsFunc(user, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '\'')
	}), " ") + " "
	for _, w := range r.ConfirmWords {
		if strings.Contains(norm, " "+w+" ") {
			return true
		}
	}
	return false
}

// canonical re-encodes JSON with sorted keys so equal arguments compare equal.
func canonical(raw json.RawMessage) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v) // encoding/json sorts map keys
	return b
}
