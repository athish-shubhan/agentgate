// Command agentgate is the CLI and service of the agent regression gate.
//
//	agentgate compare   -base A.json -cand B.json [-format tau2|run] [-md report.md] [-json report.json]
//	                    exit 0 PASS/INCONCLUSIVE, 1 FAIL, 2 usage or input error
//	agentgate calibrate -data <tau2-bench results dir> -out results/
//	agentgate serve     -addr :8080 [-dsn postgres://...]
//	agentgate replay    record|replay a tool cassette through an HTTP proxy
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentgate/calibrate"
	"agentgate/gate"
	"agentgate/server"
	"agentgate/trajectory"
)

// Exit codes: 0 PASS or INCONCLUSIVE, 1 FAIL (regression), 2 usage or input
// error. Keeping errors off exit code 1 means a CI job can tell a regression
// from a missing or malformed results file.
const (
	exitOK    = 0
	exitFail  = 1
	exitError = 2
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("agentgate: ")
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: agentgate {compare|calibrate|serve|replay} [flags]")
		return exitError
	}
	var err error
	switch args[0] {
	case "compare":
		var v gate.Verdict
		v, err = cmdCompare(args[1:], stdout, stderr)
		if err == nil && v == gate.Fail {
			return exitFail // a CI step fails on a regression
		}
	case "calibrate":
		err = cmdCalibrate(args[1:])
	case "serve":
		err = server.Main(args[1:])
	case "replay":
		err = cmdReplay(args[1:])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		fmt.Fprintln(stderr, "agentgate:", err)
		return exitError
	}
	return exitOK
}

func load(path, format string) (trajectory.Run, error) {
	var r trajectory.Run
	var err error
	switch format {
	case "tau2":
		r, err = trajectory.LoadTau2(path, true)
	case "run":
		var b []byte
		if b, err = os.ReadFile(path); err == nil {
			if err = json.Unmarshal(b, &r); err != nil {
				err = fmt.Errorf("%s: %w", path, err)
			}
		}
	default:
		return r, fmt.Errorf("unknown -format %q (want tau2 or run)", format)
	}
	if err != nil {
		return r, err
	}
	if len(r.Trials) == 0 {
		return r, fmt.Errorf("%s: no trials (wrong -format?)", path)
	}
	return r, nil
}

func cmdCompare(args []string, stdout, stderr io.Writer) (gate.Verdict, error) {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "baseline results (required)")
	cand := fs.String("cand", "", "candidate results (required)")
	format := fs.String("format", "tau2", "tau2 | run (agentgate JSON)")
	alpha := fs.Float64("alpha", 0.05, "one-sided level, 0 < alpha < 0.5")
	failM := fs.Float64("fail-margin", 0, "FAIL if confident the drop exceeds this (fraction)")
	passM := fs.Float64("pass-margin", 0.05, "PASS if confident the drop is less than this (fraction)")
	md := fs.String("md", "", "write the PR comment here")
	js := fs.String("json", "", "write the full report here")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *base == "" || *cand == "" {
		return "", errors.New("compare: -base and -cand are required")
	}
	if *alpha <= 0 || *alpha >= 0.5 || *passM < *failM {
		return "", errors.New("compare: need 0 < -alpha < 0.5 and -pass-margin >= -fail-margin")
	}
	a, err := load(*base, *format)
	if err != nil {
		return "", err
	}
	b, err := load(*cand, *format)
	if err != nil {
		return "", err
	}
	cfg := gate.DefaultConfig()
	cfg.Alpha, cfg.FailMargin, cfg.PassMargin = *alpha, *failM, *passM
	rep := gate.Compare(a, b, cfg)
	text := rep.Markdown(cfg)
	if *md != "" {
		if err := os.WriteFile(*md, []byte(text), 0o644); err != nil {
			return "", err
		}
	}
	if *js != "" {
		bb, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode report: %w", err)
		}
		if err := os.WriteFile(*js, append(bb, '\n'), 0o644); err != nil {
			return "", err
		}
	}
	if _, err := io.WriteString(stdout, text); err != nil {
		return "", err
	}
	return rep.Verdict, nil
}

func cmdCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	data := fs.String("data", "../tau2-bench/data/tau2/results/final", "directory of tau2-bench result files")
	out := fs.String("out", "results", "output directory")
	reps := fs.Int("reps", 400, "resamples per experiment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(*data, "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no result files in %s", *data)
	}
	cfg := gate.DefaultConfig()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	res := map[string]any{"alpha": cfg.Alpha, "fail_margin": cfg.FailMargin, "pass_margin": cfg.PassMargin, "files": len(files)}

	log.Printf("A/A on %d files", len(files))
	aa, err := calibrate.AA(files, *reps, cfg, 1)
	if err != nil {
		return err
	}
	res["aa"] = aa

	find := func(model, suite string) string {
		for _, f := range files {
			b := filepath.Base(f)
			if strings.HasPrefix(b, model+"_"+suite+"_") {
				return f
			}
		}
		return ""
	}
	var swaps []calibrate.SwapResult
	for _, s := range []string{"retail", "airline", "telecom"} {
		for _, pair := range [][2]string{{"gpt-4.1-2025-04-14", "gpt-4.1-mini-2025-04-14"}, {"claude-3-7-sonnet-20250219", "gpt-4.1-mini-2025-04-14"}, {"o4-mini-2025-04-16", "gpt-4.1-mini-2025-04-14"}} {
			a, b := find(pair[0], s), find(pair[1], s)
			if a == "" || b == "" {
				continue
			}
			log.Printf("swap %s -> %s on %s", pair[0], pair[1], s)
			r, err := calibrate.Swap(a, b, *reps, cfg, 2)
			if err != nil {
				return err
			}
			swaps = append(swaps, r)
		}
	}
	res["swaps"] = swaps

	var inj []calibrate.InjectResult
	for _, s := range []string{"retail", "airline"} {
		f := find("gpt-4.1-2025-04-14", s)
		for _, in := range calibrate.DefaultInjections() {
			if s == "airline" && (strings.Contains(in.Name, "exchange_") || strings.Contains(in.Name, "return_")) {
				continue // retail tool families
			}
			log.Printf("inject %q into %s", in.Name, s)
			r, err := calibrate.Inject(f, in, *reps, cfg, 3)
			if err != nil {
				return err
			}
			inj = append(inj, r)
		}
	}
	res["injections"] = inj

	log.Printf("power curves")
	curves := map[string]any{}
	for _, d := range []float64{0.05, 0.10} {
		c, err := calibrate.PowerCurve(find("gpt-4.1-2025-04-14", "retail"), 1.0, d, []int{1, 2, 4, 8, 16}, *reps, cfg, 4)
		if err != nil {
			return err
		}
		curves[fmt.Sprintf("drop_%.0f_points_all_tasks", 100*d)] = c
	}
	res["power_curves_retail_gpt41"] = curves

	log.Printf("reliability")
	var rel []calibrate.Reliability
	for _, f := range files {
		r, err := calibrate.Reliable(f)
		if err != nil {
			return err
		}
		rel = append(rel, r)
	}
	res["reliability"] = rel

	log.Printf("rule validity")
	var def []string
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "_default_") || strings.Contains(filepath.Base(f), "_base_") {
			def = append(def, f)
		}
	}
	rv, err := calibrate.RuleValidity(def, cfg.Rules)
	if err != nil {
		return err
	}
	res["rule_validity_default_mode_files"] = rv
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("encode calibration results: %w", err)
	}
	return os.WriteFile(filepath.Join(*out, "calibration.json"), b, 0o644)
}
