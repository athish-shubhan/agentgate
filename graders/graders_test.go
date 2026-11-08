package graders

import (
	"encoding/json"
	"strings"
	"testing"

	"agentgate/trajectory"
)

func call(name, args string) trajectory.Message {
	return trajectory.Message{Role: "assistant", ToolCalls: []trajectory.ToolCall{{Name: name, Args: json.RawMessage(args)}}}
}

func has(v []Violation, rule string) bool {
	for _, x := range v {
		if x.Rule == rule {
			return true
		}
	}
	return false
}

func TestRules(t *testing.T) {
	r := DefaultRules()
	tr := trajectory.Trial{Messages: []trajectory.Message{
		{Role: "user", Content: "I want to book a flight"}, // "book" must not count as "ok"
		call("book_reservation", `{"a":1,"b":2}`),
		{Role: "user", Content: "Yes, please go ahead."},
		call("cancel_reservation", `{"id":"X"}`),
		call("get_user", `{"b":2,"a":1}`), call("get_user", `{"a":1,"b":2}`), call("get_user", `{"a":1, "b":2}`),
		call("transfer_to_human_agents", `{}`),
	}}
	v := Grade(r, tr)
	if !has(v, "write_without_confirmation") || !has(v, "loop") || !has(v, "flag:transfer_to_human_agents") {
		t.Fatalf("%+v", v)
	}
	n := 0
	for _, x := range v {
		if x.Rule == "write_without_confirmation" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("only the unconfirmed booking should be flagged, got %d: %+v", n, v)
	}
}

// Model-written user turns use typographic apostrophes; "that’s right" must
// count as confirmation like "that's right".
func TestConfirmationWithTypographicApostrophe(t *testing.T) {
	r := DefaultRules()
	r.ConfirmWords = []string{"that's right"}
	for _, u := range []string{"That’s right, go on", "that's right"} {
		if !confirmed(r, strings.ToLower(u)) {
			t.Errorf("%q not counted as confirmation", u)
		}
	}
	if confirmed(r, "that is wrong") {
		t.Error("false positive")
	}
}
