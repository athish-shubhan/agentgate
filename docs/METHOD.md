# AgentGate method note

## The question

Agent version A is on main. A pull request produces version B. Both ran the same task suite, several trials per task. Is B worse, and by how much, with a known false-alarm rate?

## Why a paired, task-clustered test

Two facts about agent evals decide the method.

1. **Trials of the same task are correlated.** A hard task is hard on every trial. In the tau2-bench default-mode files, 31 to 51% of tasks give mixed results across 4 identical trials; the rest always pass or always fail. Treating 456 trials as 456 independent coin flips overstates the sample size.
2. **A and B run the same tasks.** Task difficulty is shared, so most of the variation in pass rate is common to both versions and cancels in the difference.

So the unit of analysis is the task. For each task `i` the gate computes `d_i = rate_B(i) - rate_A(i)` and runs a one-sample t-test on the `d_i`: mean difference, standard error `sd(d) / sqrt(n_tasks)`, `n_tasks - 1` degrees of freedom. This is the paired, cluster-robust comparison recommended in Miller, "Adding Error Bars to Evals" (2024), specialised to equal trials per task.

The naive alternative (two-proportion z-test over all trials, A and B treated as independent samples) gets the variance wrong in the other direction here: it ignores the shared difficulty, overstates the variance of the difference, and loses power. On the tau2-bench data it raised 1.3% false alarms instead of 5% and caught 52% instead of 69% of a "weaker model" injection (`results/SUMMARY.md`, sections 1 and 3).

## The decision rule

The gate reports a one-sided `1 - 2*alpha` interval for the mean per-task difference (90% for alpha 0.05) and returns:

- **FAIL** if the whole interval is below `-fail_margin` (default 0): B is worse, at level alpha.
- **PASS** if the whole interval is above `-pass_margin` (default 5 points): any drop is smaller than the margin we tolerate.
- **INCONCLUSIVE** otherwise: not enough evidence either way. The comment says how many points the current design can detect, so the team knows whether to add trials.

Three outcomes instead of two is deliberate. A two-outcome gate has to treat "no evidence of a regression" as a pass, which is how underpowered gates wave regressions through.

## How many trials (power and MDE)

The minimum detectable effect at 80% power with one-sided alpha:

`MDE = (z_{1-alpha} + z_{0.8}) * sd(d) / sqrt(n_tasks)`

The comment prints it for the actual data (6.5 points for gpt-4.1 vs gpt-4.1-mini on retail with 4 trials per task). For planning, `calibrate.PowerCurve` simulates gates from per-task pass probabilities taken from a real run. On retail (114 tasks), a 10-point regression needs 4 trials per task for 98% power; a 5-point regression needs 8 for 89%.

## Sequential stopping

Running every trial when the answer is obvious after one wastes money. `gate.Sequential` looks after every trial and stops when the statistic crosses an O'Brien-Fleming boundary `c_k = c * sqrt(K/k)` in either direction. `c` is found by Monte Carlo on the joint distribution of the look statistics so the overall one-sided false-FAIL rate is alpha (fixed seed, reproducible). The boundary is strict early, which keeps peeking from inflating false alarms; the measured false-FAIL rate of the sequential gate stayed at 3.5 to 5.3% across budgets.

On a 10-point regression capped at 4 trials it used 2.44 trials per task on average (39% fewer) at the same power. It saves little when the true effect sits near a margin or is zero, because then the evidence is genuinely needed.

## pass^k

`pass^k` is the probability that all k trials of a task pass, estimated without bias as `C(c, k) / C(n, k)` per task (the tau-bench definition) and averaged over tasks. It is reported for k = 1..4 because reliability, not best-case success, is what a customer sees. The gate's verdict uses pass@1 differences; pass^k is context.

## Infrastructure failures

Trials that ended with `infrastructure_error`, `agent_error_api` or `user_error` say nothing about the agent. They are excluded per trial before the per-task rates are computed, and the comment states how many were excluded on each side. If infra failures cluster on one side, that shows up in the counts instead of as a fake regression.

## Variance from the simulated user

tau2-bench's user is itself an LLM, so some trial-to-trial variance is the user's. The method does not need to separate the two: repeated trials and pairing by task handle both. To see how much it matters, `results/SUMMARY.md` section 6 compares telecom with and without the user simulator (`no-user` mode). For o4-mini the share of tasks with mixed outcomes falls from 33% to 11%; for gpt-4.1 it does not fall. No-user mode also changes the task, so this is suggestive only.

## Trajectory graders

The verdict uses the task reward, which in tau2-bench is a deterministic final-state check (database match plus required outputs). Trajectory rules in `graders/` explain *why* a candidate got worse: loops (same tool, same arguments, 3 times), step budget, too many tool errors, consequential writes without an explicit user confirmation, and flagged tools such as human hand-off. The PR comment lists rules that appear in B's failures on a regressed task but in none of A's trials on that task.

Rules are checked for validity against reward on 5,360 default-mode trials (`SUMMARY.md` section 7). Hand-off and step-budget rules carry real signal (fail rate 68% and 83% when fired vs 29% and 42% otherwise). The keyword-based confirmation rule fires often (883 trials) but moves the failure rate least (52% vs 42%), so it is shown but should not be trusted alone.

Model-graded rubrics are not implemented. If added, they should be validated the same way plus an agreement check against a small human-labelled sample (`stats.CohenKappa` is there for that).

## Record and replay

`replay/` is a cassette of tool calls keyed by tool name and canonical JSON arguments. Identical calls are kept in order, so a second identical call gets the second recorded result and state changes between calls are reproduced. It runs as an HTTP tool proxy (`agentgate replay -mode record|replay|replay-or-record`), so a harness becomes replayable by pointing its tool URL at the proxy. When only a grader changes, the gate re-grades stored trajectories and makes no model or tool calls.

## Calibration protocol

All on published tau2-bench results, no model calls:

- **A/A**: split each file's 4 trials per task into 2 + 2 at random, 400 times; every FAIL is a false alarm.
- **Injected regressions**: same split, B degraded by flipping passing trials of affected tasks (tasks whose reference solution uses a tool family, or a random subset) with a set probability.
- **Real model swaps**: gpt-4.1, claude-3.7-sonnet and o4-mini vs gpt-4.1-mini on each domain, with bootstrap detection rates at 1, 2 and 4 trials.
- **Power curves**: parametric simulation from real per-task pass rates.

All experiments use fixed seeds and sorted iteration, so `agentgate calibrate` reproduces `results/calibration.json`.

## Adding a suite

Any harness can feed the gate by writing the agentgate JSON format (`examples/run.json`): one record per trial with `suite`, `task`, `trial` index, `pass`, optional `reward`, `cost_usd`, `duration_sec`, `termination`, `infra_error`, and `messages` (role, content, tool calls) if you want trajectory rules. `expected_tools` per task is optional and only used by tool-family injections. Then:

```
agentgate compare -format run -base main.json -cand pr.json
```

For tau2-bench, pass the results file directly with `-format tau2`.
