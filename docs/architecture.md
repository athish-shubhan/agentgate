# Architecture

How the packages fit together. The statistics are in [`METHOD.md`](METHOD.md).

```mermaid
flowchart LR
    subgraph inputs
        T2["tau2-bench results JSON"]
        RJ["agentgate run JSON<br/>examples/run.json"]
    end
    T2 -->|trajectory.ParseTau2| RUN["trajectory.Run<br/>trials + messages"]
    RJ -->|json.Unmarshal| RUN

    RUN --> OUT["gate.Outcomes<br/>per-task successes / trials,<br/>infra failures excluded"]
    OUT --> PAIRED["stats.Paired<br/>task-clustered paired t"]
    PAIRED --> DECIDE["gate.Decide<br/>PASS / FAIL / INCONCLUSIVE"]
    RUN --> GRADE["graders.Grade<br/>loops, step budget, tool errors,<br/>write without confirmation"]
    DECIDE --> REP["gate.Report"]
    GRADE --> REP
    REP --> MD["Markdown PR comment"]
    REP --> JS["JSON report"]

    subgraph fe ["front ends"]
        CLI["cmd/agentgate compare<br/>exit 0 / 1 FAIL / 2 error"]
        SRV["server: net/http or Hertz<br/>memory or Postgres store"]
        ACT["action.yml<br/>GitHub composite action"]
    end
    CLI --> RUN
    SRV --> RUN
    ACT --> CLI

    CAL["calibrate<br/>A/A, swaps, injections,<br/>power curves"] -->|resamples published runs| OUT
    CAL --> SEQ["gate.Sequential<br/>O'Brien-Fleming looks"]
    REPLAY["replay: tool cassette<br/>+ HTTP proxy"] -.->|makes agent runs<br/>repeatable| T2
```

## Packages

| package | role |
|---|---|
| `trajectory` | data model (`Run`, `Trial`, `Message`) and the tau2-bench loader; marks infrastructure failures |
| `stats` | paired task-clustered comparison, naive baseline, pass^k, MDE, sequential bounds, kappa; JSON encodes undefined values as `null` |
| `gate` | per-task outcomes, the decision rule, `Compare` (report with per-suite results, pass^k, regressed tasks and the rules new in their failures), the sequential gate, Markdown |
| `graders` | deterministic trajectory rules used to explain a regression, not to decide it |
| `calibrate` | measures the gate on published tau2-bench trials; deterministic for a seed and independent of the data directory |
| `server` | transport-neutral core (`Server.PutRun`, `Compare`, ...) with two HTTP front ends and two stores |
| `replay` | record / replay / replay-or-record of tool calls, and an HTTP proxy in front of a tool server |
| `cmd/agentgate` | CLI: `compare`, `calibrate`, `serve`, `replay` |

## Request flow (service)

1. `PUT /v1/runs/{name}?format=tau2|run` parses the upload into a `trajectory.Run` and stores it.
2. `POST /v1/compare {"base","candidate",...}` loads both runs, runs `gate.Compare`, stores the report and
   returns it with the Markdown comment.
3. `GET /v1/reports/{id}[/comment]` reads a stored report back.

The CLI runs steps 1 and 2 in one process on two files; the GitHub action runs the CLI.
