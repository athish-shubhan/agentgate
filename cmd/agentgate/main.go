// Command agentgate is the CLI and service of the agent regression gate.
//
//	agentgate compare   -base A.json -cand B.json [-format tau2|run] [-md report.md] [-json report.json]
//	agentgate calibrate -data <tau2-bench results dir> -out results/
//	agentgate serve     -addr :8080 [-dsn postgres://...]
//	agentgate replay    record|replay a tool cassette through an HTTP proxy
package main

import (
	"encoding/json"
	"flag"
	"fmt"
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentgate {compare|calibrate|serve|replay} [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "calibrate":
		err = cmdCalibrate(os.Args[2:])
	case "serve":
		err = server.Main(os.Args[2:])
	case "replay":
		err = cmdReplay(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func load(path, format string) (trajectory.Run, error) {
	if format == "tau2" {
		return trajectory.LoadTau2(path, true)
	}
	var r trajectory.Run
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	base := fs.String("base", "", "baseline results")
	cand := fs.String("cand", "", "candidate results")
	format := fs.String("format", "tau2", "tau2 | run (agentgate JSON)")
	alpha := fs.Float64("alpha", 0.05, "one-sided level")
	failM := fs.Float64("fail-margin", 0, "FAIL if confident the drop exceeds this (fraction)")
	passM := fs.Float64("pass-margin", 0.05, "PASS if confident the drop is less than this (fraction)")
	md := fs.String("md", "", "write the PR comment here")
	js := fs.String("json", "", "write the full report here")
	fs.Parse(args)
	a, err := load(*base, *format)
	if err != nil {
		return err
	}
	b, err := load(*cand, *format)
	if err != nil {
		return err
	}
	cfg := gate.DefaultConfig()
	cfg.Alpha, cfg.FailMargin, cfg.PassMargin = *alpha, *failM, *passM
	rep := gate.Compare(a, b, cfg)
	text := rep.Markdown(cfg)
	fmt.Print(text)
	if *md != "" {
		os.WriteFile(*md, []byte(text), 0o644)
	}
	if *js != "" {
		bb, _ := json.MarshalIndent(rep, "", "  ")
		os.WriteFile(*js, bb, 0o644)
	}
	if rep.Verdict == gate.Fail {
		os.Exit(1) // a CI step fails on a regression
	}
	return nil
}

func cmdCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	data := fs.String("data", "../tau2-bench/data/tau2/results/final", "directory of tau2-bench result files")
	out := fs.String("out", "results", "output directory")
	reps := fs.Int("reps", 400, "resamples per experiment")
	fs.Parse(args)
	files, _ := filepath.Glob(filepath.Join(*data, "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no result files in %s", *data)
	}
	cfg := gate.DefaultConfig()
	os.MkdirAll(*out, 0o755)
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
	b, _ := json.MarshalIndent(res, "", "  ")
	return os.WriteFile(filepath.Join(*out, "calibration.json"), b, 0o644)
}
