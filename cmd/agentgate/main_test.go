package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentgate/gate"
	"agentgate/trajectory"
)

// writeRun writes a run in agentgate JSON format where task i passes
// round(rates[i]*n) of n trials.
func writeRun(t *testing.T, dir, name string, rates []float64, n int) string {
	t.Helper()
	r := trajectory.Run{Name: name}
	for i, p := range rates {
		for k := 0; k < n; k++ {
			r.Trials = append(r.Trials, trajectory.Trial{Suite: "s", Task: fmt.Sprint(i), Trial: k, Pass: float64(k) < p*float64(n)})
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func flat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCompareExitCodes(t *testing.T) {
	dir := t.TempDir()
	base := writeRun(t, dir, "base", flat(0.75, 60), 4)
	bad := writeRun(t, dir, "bad", flat(0.25, 60), 4)
	same := writeRun(t, dir, "same", flat(0.75, 60), 4)

	if code, out, _ := runCLI("compare", "-format", "run", "-base", base, "-cand", bad); code != exitFail || !strings.Contains(out, "FAIL") {
		t.Fatalf("regression: exit %d\n%s", code, out)
	}
	if code, out, _ := runCLI("compare", "-format", "run", "-base", base, "-cand", same); code != exitOK || !strings.Contains(out, "PASS") {
		t.Fatalf("no change: exit %d\n%s", code, out)
	}
}

// Input problems must exit 2, never 1, so CI does not report them as a
// regression of the agent.
func TestCompareInputErrorsExit2(t *testing.T) {
	dir := t.TempDir()
	base := writeRun(t, dir, "base", flat(0.75, 10), 2)
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing file", []string{"-format", "run", "-base", base, "-cand", filepath.Join(dir, "nope.json")}, "no such file"},
		{"missing flag", []string{"-format", "run", "-base", base}, "-base and -cand are required"},
		{"bad format", []string{"-format", "runn", "-base", base, "-cand", base}, `unknown -format "runn"`},
		{"no trials", []string{"-format", "run", "-base", base, "-cand", empty}, "no trials"},
		{"tau2 file with -format run", []string{"-format", "run", "-base", base, "-cand", "../../README.md"}, "README.md"},
		{"bad alpha", []string{"-format", "run", "-base", base, "-cand", base, "-alpha", "0.7"}, "alpha"},
		{"margins", []string{"-format", "run", "-base", base, "-cand", base, "-pass-margin", "0", "-fail-margin", "0.1"}, "pass-margin"},
		{"unwritable md", []string{"-format", "run", "-base", base, "-cand", base, "-md", filepath.Join(dir, "no", "dir", "c.md")}, "no such file"},
		{"unknown flag", []string{"-bogus"}, "bogus"},
	}
	for _, c := range cases {
		code, _, stderr := runCLI(append([]string{"compare"}, c.args...)...)
		if code != exitError || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %q (want exit 2 containing %q)", c.name, code, stderr, c.want)
		}
	}
	if code, _, _ := runCLI("nope"); code != exitError {
		t.Errorf("unknown command: exit %d", code)
	}
	if code, _, _ := runCLI(); code != exitError {
		t.Errorf("no command: exit %d", code)
	}
}

// With 1 trial per task pass^2..pass^4 are undefined (NaN). The JSON report
// used to come out as an empty file because encoding/json rejects NaN.
func TestCompareJSONReportWithUndefinedPassK(t *testing.T) {
	dir := t.TempDir()
	base := writeRun(t, dir, "base", flat(1, 5), 1)
	cand := writeRun(t, dir, "cand", flat(1, 5), 1)
	js, md := filepath.Join(dir, "r.json"), filepath.Join(dir, "c.md")
	code, out, stderr := runCLI("compare", "-format", "run", "-base", base, "-cand", cand, "-json", js, "-md", md)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	b, err := os.ReadFile(js)
	if err != nil {
		t.Fatal(err)
	}
	var rep gate.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("report is not valid JSON (%v): %q", err, b)
	}
	if rep.Overall.Tasks != 5 || len(rep.PassKA) != 4 || rep.PassKA[0] != 1 || !strings.Contains(string(b), "null") {
		t.Fatalf("report: %s", b)
	}
	comment, _ := os.ReadFile(md)
	if string(comment) != out || !strings.Contains(out, "n/a") {
		t.Fatalf("comment file differs from stdout or lacks n/a:\n%s", comment)
	}
}

func TestCompareExampleRun(t *testing.T) {
	code, out, stderr := runCLI("compare", "-format", "run", "-base", "../../examples/run.json", "-cand", "../../examples/run.json")
	if code != exitOK || !strings.Contains(out, "INCONCLUSIVE") || !strings.Contains(out, "fewer than 2 shared tasks") {
		t.Fatalf("exit %d %s\n%s", code, stderr, out)
	}
}

// A typo in -mode used to select Record (the zero Mode) silently, which
// overwrites the cassette on exit.
func TestReplayRejectsUnknownMode(t *testing.T) {
	err := cmdReplay([]string{"-mode", "replay-only", "-cassette", filepath.Join(t.TempDir(), "c.jsonl")})
	if err == nil || !strings.Contains(err.Error(), `unknown -mode "replay-only"`) {
		t.Fatalf("got %v", err)
	}
	err = cmdReplay([]string{"-mode", "record"})
	if err == nil || !strings.Contains(err.Error(), "needs -upstream") {
		t.Fatalf("record without upstream: %v", err)
	}
}
