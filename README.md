# AgentGate

A regression gate for LLM agents. Given results for agent version A (main) and B (a pull request) on the same task suite, AgentGate decides **PASS / FAIL / INCONCLUSIVE** with a controlled false-alarm rate, says how big a drop the data could have detected, and writes a PR comment that points at the tasks and behaviours that got worse.

Go; third-party dependencies are `lib/pq` (Postgres) and CloudWeGo Hertz (optional second HTTP front end). CLI, HTTP service (Postgres or in-memory), GitHub composite action, and a calibration harness that measures the gate on 10,832 published tau2-bench trials.

## Results (measured; no model calls, all from published tau2-bench trials)

Full tables: [`results/SUMMARY.md`](results/SUMMARY.md). Method: [`docs/METHOD.md`](docs/METHOD.md).

| what | result |
|---|---|
| A/A false-FAIL rate, paired test, 26 files x 400 splits | **4.7%** mean (target 5%; range 2.8 to 7.8%) |
| Injected tool-schema break (exchange_*, 18.8-point drop), 2 trials/task | **100%** caught, sequential gate stopped after 1.04 trials/task |
| Injected dropped policy line (return_*, 11.2 points) | **96%** caught (naive unpaired test: 90%) |
| Injected "weaker model" (random 20% of tasks, 7.3 points) | **69%** caught (naive: 52%) |
| Real swap gpt-4.1 -> gpt-4.1-mini, retail | FAIL, -8.1 points (90% CI -12.5 to -3.8) |
| Real swap gpt-4.1 -> gpt-4.1-mini, telecom | PASS: mini is *better* (+9.7 points) |
| Trials for 98% power on a 10-point drop (retail, 114 tasks) | 4 per task; sequential gate: **2.44** on average (39% fewer) |
| Gate cost estimate, retail, 4 trials x 114 tasks, candidate side | $26.52 agent cost (from recorded per-trial costs), 8 trial-hours; sequential: $16.14 |

![injection power](results/injection_power.png)
![power curve](results/power_curve.png)

Things the calibration showed that I did not expect:

- **The naive test is not flaky here, it is blind.** Treating trials as independent and A, B as unpaired gave *fewer* false alarms (1.3%) than the paired test, because it overstates the variance of the difference. It paid for that with power: 52% vs 69% on the weaker-model injection, 6% vs 18% on airline.
- **Clearing a PASS margin costs more than catching a FAIL.** With 4 trials a no-op change PASSes only 65% of the time (INCONCLUSIVE otherwise). That is why the gate has three outcomes.
- **A third to a half of tasks are flaky.** 31 to 51% of tasks give mixed results over 4 identical trials. Removing the simulated user cut that from 33% to 11% for o4-mini on telecom but not for gpt-4.1.

## What is here

```
stats/       paired task-clustered t-test, naive baseline, pass^k, MDE, O'Brien-Fleming bounds, Cohen's kappa (tests against SciPy values)
gate/        outcomes, decision rule, Compare -> Report, Markdown PR comment, sequential gate
graders/     trajectory rules: loops, step budget, tool errors, write without confirmation, flagged tools
trajectory/  data model + tau2-bench results loader (infra failures marked, expected tools from evaluation criteria)
replay/      tool-call cassette (record / replay / replay-or-record) + HTTP tool proxy
server/      HTTP API (one core, net/http and Hertz front ends), Postgres and in-memory stores
calibrate/   A/A, real swaps, injections, power curves, reliability, rule validity
cmd/agentgate  CLI: compare, calibrate, serve, replay
action.yml   GitHub composite action (build, compare, comment, fail on FAIL)
bench/plot.py  results/calibration.json -> figures + SUMMARY.md
```

## Usage

```
go build -o bin/agentgate ./cmd/agentgate

# gate two tau2-bench result files (exit code 1 on FAIL)
bin/agentgate compare -base main.json -cand pr.json -md comment.md -json report.json

# any harness: write examples/run.json format and use -format run
bin/agentgate compare -format run -base main.json -cand pr.json

# reproduce results/ (about a minute on a 2-vCPU machine; timing not re-measured elsewhere)
git clone https://github.com/sierra-research/tau2-bench ../tau2-bench
bin/agentgate calibrate -data ../tau2-bench/data/tau2/results/final -out results
python3 bench/plot.py results/calibration.json results

# service
bin/agentgate serve -addr 127.0.0.1:8080 -dsn 'postgres://user:pass@host/agentgate?sslmode=disable'
bin/agentgate serve -http hertz -addr 127.0.0.1:8080        # same API on CloudWeGo Hertz
curl -X PUT --data-binary @main.json 'localhost:8080/v1/runs/main?format=tau2'
curl -X PUT --data-binary @pr.json   'localhost:8080/v1/runs/pr-123?format=tau2'
curl -d '{"base":"main","candidate":"pr-123"}' localhost:8080/v1/compare
curl localhost:8080/v1/reports/1/comment

# tool replay proxy in front of a real tool server
bin/agentgate replay -mode replay-or-record -cassette tools.jsonl -upstream http://tools:9000 -addr 127.0.0.1:8181
```

Example PR comment (real data): [`results/example_pr_comment.md`](results/example_pr_comment.md).

Tests: `go test -race ./...` (set `AGENTGATE_TEST_DSN` to also run the Postgres store test).

## Honest scope

- **No new agent runs.** Every number is a reanalysis or resample of published tau2-bench trials (4 models, 4 trials per task, user simulator gpt-4.1). Injected regressions are simulated by flipping recorded passes to failures; the "tool schema change" and "dropped policy line" names describe what the injection models, not a real prompt edit that was run.
- **Suites:** tau2-bench only. No other suites (e.g. Terminal-Bench) are built; any harness can feed the gate through the JSON format.
- **Model-graded rubrics:** not implemented. Trajectory rules are deterministic and validated against reward (`SUMMARY.md` section 7).
- **GitHub action:** written but not exercised on GitHub; it runs the same `compare` command used here.
- **Service:** no authentication; it binds to 127.0.0.1 by default, so put it behind your own auth proxy before exposing it. Run names are limited to 128 characters, and the in-memory store is unbounded. Two HTTP front ends over one transport-neutral core: `net/http` and CloudWeGo Hertz (`agentgate serve -http hertz`); the same test suite runs against both. No Kitex/Thrift API. Postgres store tested against Postgres 16 locally.
- Cost per gate covers agent tokens only (user-simulator cost excluded). The "15 minutes with 32 sandboxes" figure is arithmetic on recorded trial durations, not a measured run.
