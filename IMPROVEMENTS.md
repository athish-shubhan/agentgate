# Improvements log

## Round 1 (2026-11-05): JSON reports with undefined values, CLI exit codes and input checks, CI

### What changed

1. **Bug: reports with undefined values came out empty.** `encoding/json` rejects NaN and ±Inf. A comparison
   with fewer than 2 shared tasks has SE = +Inf, and pass^k is NaN when no task has k trials (any run with
   fewer than 4 trials per task, including `examples/run.json`). Because the error was ignored:
   - `agentgate compare -json r.json` wrote a **0-byte file** and exited 0 (the GitHub action then ran `jq` on it);
   - `POST /v1/compare` returned **HTTP 200 with an empty body** on both net/http and Hertz with the memory store,
     and **HTTP 500 `json: unsupported value: +Inf`** with the Postgres store.

   Fix: `stats.Comparison` and the new `stats.Floats` (used for `pass_k_a` / `pass_k_b`) encode non-finite
   values as `null` and decode `null` as NaN. Finite values encode exactly as before (a test pins the bytes).
   The PR comment shows `n/a` for those values, and the MDE line says `n/a (fewer than 2 shared tasks)`
   instead of a misleading `0.0 points`. Both HTTP front ends now encode before writing the status, so an
   encoding failure becomes a 500 with an error body, not an empty 200.
2. **Bug: CLI errors had the same exit code as a regression.** `log.Fatal` exits 1, which is also the FAIL
   exit code, so a missing results file looked like a regression in CI. Now: 0 PASS/INCONCLUSIVE,
   1 FAIL, 2 usage or input error. `compare` also rejects: missing `-base`/`-cand`, an unknown `-format`
   (it used to be read silently as `run`), a file that parses to zero trials (for example a tau2 file read
   with `-format run`, which produced a `NaN%` report), `alpha` outside (0, 0.5) or `pass-margin < fail-margin`
   (the same checks the HTTP API already had), and failed writes of `-md` / `-json` (previously ignored).
3. **Bug: a typo in `replay -mode` switched to record mode.** An unknown mode name mapped to the zero `Mode`,
   which is `Record`: the proxy would call the upstream and overwrite the cassette on exit. Unknown modes are
   now an error, record modes require `-upstream`, and the proxy server sets `ReadHeaderTimeout`.
4. **CI.** `.github/workflows/ci.yml` runs gofmt, `go vet`, `go test -race` (with a Postgres 16 service so
   `TestPGStore` runs instead of skipping), a CLI smoke test on `examples/run.json`, and golangci-lint v2.5.0.
   `.golangci.yml` keeps the default linters; the remaining unchecked errors in non-test code were fixed.
5. **README.** "No dependencies beyond `lib/pq`" was wrong (Hertz is a direct dependency). Added a Quick start
   that runs from a clean clone without downloading data, and documented the exit codes and `null` / `n/a`.

### Why it matters

The gate is meant to run in CI. Before this change it could report a broken pipeline as a regression,
silently lose its JSON report, and return an empty 200 from the service for small suites.

### How it was verified

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c     # main
     15 PASS:
      1 SKIP:                                     # TestPGStore, no DSN
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c     # this branch
     23 PASS:
      1 SKIP:
$ AGENTGATE_TEST_DSN='postgres://postgres@127.0.0.1:55432/agentgate?sslmode=disable' go test -count=1 -v ./... \
    | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c                               # local Postgres 16
     24 PASS:
$ go test -race -count=1 ./...                    # all ok
$ golangci-lint run ./...                         # main: 32 issues; branch: 0 issues
$ go vet ./... && gofmt -l .                      # clean
```

Coverage (`go test -cover ./...`): `cmd/agentgate` 0% -> 39.9%, `stats` 74.3% -> 76.8%, `server` 51.9% -> 52.4%,
`gate` 52.0% -> 50.3% (new formatting code is exercised from the CLI tests), others unchanged.

The new server test fails on main's code and passes with the fix:

```
# main's gate/server/stats code with the new test
server_test.go:144: tiny compare: 200 unexpected end of JSON input ""      (net/http, memory store)
server_test.go:144: tiny compare: 200 unexpected end of JSON input "\n"    (Hertz)
server_test.go:144: tiny compare: 500 <nil> "{\"error\": \"json: unsupported value: +Inf\"}"   (Postgres)
```

CLI before the fix (main):

```
$ agentgate compare -format run -base examples/run.json -cand examples/run.json -json /tmp/r.json; echo $?; wc -c /tmp/r.json
0
0 /tmp/r.json
$ agentgate compare -base nope.json -cand x; echo $?
open nope.json: no such file or directory
1
```

After: the same `-json` call writes a valid report (`"pass_k_a": [1, null, null, null]`, `"se": null`), and the
missing file exits 2.

Calibration (`results/`) does not serialize the changed types. I re-ran it on the published tau2-bench data
(`git clone --depth 1 --sparse https://github.com/sierra-research/tau2-bench`, `data/tau2/results/final`,
26 result files): `bin/agentgate calibrate -data .../final -out /tmp/recal` took 40 s. The output of this
branch matches main's code on all 820 numeric fields (largest difference below 1e-9; main itself differs from
run to run in the last floating-point digits because of map iteration order in float sums).

### Claims I could not verify

The committed `results/calibration.json` does not match what main's code produces now, in the A/A section only
(66 of 820 values; injections, swaps, power curves, reliability and rule validity all match). Re-run with main's
code and with this branch:

| A/A over 26 files | README / committed | re-run |
|---|---|---|
| paired false-FAIL, mean | 4.7% | 4.7% (0.0468) |
| paired false-FAIL, range | 2.8 to 7.8% (2.75 to 7.75) | 2.25 to 6.75% |
| naive false-FAIL, mean | 1.3% (0.0127) | 1.0% (0.0102) |

The README numbers are left unchanged; regenerating `results/` is a decision for the author.

### Files touched

`stats/json.go` (new), `stats/json_test.go` (new), `gate/gate.go`, `server/server.go`, `server/hertz.go`,
`server/server_test.go`, `cmd/agentgate/main.go`, `cmd/agentgate/replay.go`, `cmd/agentgate/main_test.go` (new),
`graders/graders.go`, `replay/http.go`, `.github/workflows/ci.yml` (new), `.golangci.yml` (new), `README.md`,
`IMPROVEMENTS.md` (new).

## Round 2 (2026-11-05): calibration made reproducible from any directory, calibrate tests, action hardened and run in CI

Stacked on round 1 (`improve/round-1`).

### What changed

1. **Bug: the A/A numbers depended on the directory the data was cloned into.** `calibrate.AA` seeded each
   file's random splits with `len(path)`, the length of the file's path string. Re-running the README's
   reproduce command with the data anywhere else gave different A/A numbers; this is why round 1 found that
   `results/calibration.json` did not match a re-run (66 of 820 values). Searching path lengths showed the
   committed results come from a 53-character data directory. The stream now depends only on the file name
   (`54 + len(name)`, equal to the old value for that directory), so **the published numbers are unchanged** and
   now reproduce from any location.
2. **Bit-for-bit reproducible calibration.** Several float sums iterated Go maps, whose order is random, so two
   runs differed in the last digits (e.g. `0.4100000000000001` vs `0.41`). `stats.Paired`, `stats.PassHatK`, the
   MDE in `gate.Compare` and three sums in `calibrate` now iterate sorted keys. Two runs are byte-identical, and
   `results/calibration.json` was regenerated (29 lines change, by at most 3.3e-16). The README's reproduce step
   now ends with `cmp`.
3. **Tests for `calibrate`** (0% coverage before; it produces every number in the README), on synthetic tau2-bench
   files: A/A independent of the data directory (fails on the old seeding), A/A false-FAIL rate near alpha,
   injected tool-schema break caught with the sequential gate stopping early, results identical for a seed, a
   worse model detected more often with more trials, reliability statistics.
4. **GitHub action hardened.** `action.yml` pasted `${{ inputs.* }}` into its shell scripts (the GitHub Actions
   script-injection pattern); inputs now arrive as environment variables. With round 1's exit codes, a bad input
   (exit 2) now sets `verdict=ERROR`, writes a short step summary instead of running `jq` on a missing report,
   and skips the PR comment.
5. **The action runs in CI.** A new `action` job uses `./` like a user's workflow would: a no-op comparison of
   `examples/run.json` must give `INCONCLUSIVE`, and a missing results file must fail the step with
   `verdict=ERROR`. The README's "written but not exercised on GitHub" is updated accordingly.

### How it was verified

Calibration on the published tau2-bench data (`git clone --depth 1 --sparse https://github.com/sierra-research/tau2-bench`,
`data/tau2/results/final`, 26 files), compared with the committed `results/calibration.json` (820 numeric values):

| code | data directory | values differing by more than 1e-9 |
|---|---|---|
| round 1 (old seeding) | `/tmp/tau2-bench/data/tau2/results/final` (39 chars) | 66 (all in the A/A section) |
| round 1 (old seeding) | `../tau2-bench/data/tau2/results/final` (README's path, 37 chars) | 70 |
| round 1 (old seeding) | a 53-character path | **0** |
| this round | `/tmp/tau2-bench/data/tau2/results/final` | **0** (largest difference 3.3e-16) |

```
$ bin/agentgate calibrate -data /tmp/tau2-bench/data/tau2/results/final -out /tmp/recal-det1   # twice
$ cmp /tmp/recal-det1/calibration.json /tmp/recal-det2/calibration.json && echo BYTE-IDENTICAL
BYTE-IDENTICAL
```

So the README's A/A row (4.7% mean, range 2.8 to 7.8%, naive 1.3%) is confirmed, and the round 1 entry under
"Claims I could not verify" is resolved: the claims were right and the harness was path-dependent.

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # round 1 -> this round
     23 PASS: -> 28 PASS:   (1 SKIP: TestPGStore without AGENTGATE_TEST_DSN)
$ go test -race -count=1 ./...      # all ok
$ golangci-lint run ./...           # 0 issues
```

Coverage: `calibrate` 0% -> 58.1%, `stats` 76.8% -> 77.6%. `TestAAIndependentOfDirectory` on the old seeding:
`calibrate_test.go:105: same data, different directory`.

The action job runs on GitHub in this PR's CI; locally the two cases exit 0 and 2 as the job expects.

### Files touched

`calibrate/calibrate.go`, `calibrate/calibrate_test.go` (new), `stats/stats.go`, `gate/gate.go`, `action.yml`,
`.github/workflows/ci.yml`, `results/calibration.json`, `README.md`, `IMPROVEMENTS.md`.

## Round 3 (2026-11-05): tool cassette could be wiped by one text response; server error tests; architecture doc

Stacked on round 2 (`improve/round-2`).

### What changed

1. **Bug: one non-JSON tool response wiped the whole cassette.** The replay proxy stored an upstream body as
   `json.RawMessage`. If a tool server answered with plain text, every later `Save` failed in the JSON encoder,
   and `Save` truncated the cassette file before writing, so the file was left at **0 bytes**:

   ```
   second Save err=json: error calling MarshalJSON for type json.RawMessage: invalid character 'O' looking for
   beginning of value; file before 44 bytes, after 0 bytes
   ```

   Now a body that is not JSON is stored in a new `text` field and replayed byte for byte; `Save` writes a
   temporary file next to the cassette, syncs it and renames it over the old one, so a failed save leaves the old
   cassette intact and no temp file; `Load` names the bad line (`c.jsonl:3: ...`). Arguments that are not JSON are
   rejected (HTTP 400 from the proxy, `ErrBadArgs` from `Call`): they could neither be matched canonically nor
   saved. Existing cassettes load unchanged.
2. **Server error responses tested**: bad and unknown report ids (400, 404), unknown upload format, empty or
   malformed runs, malformed or oversized compare requests (400, 404, 413), margin validation, and the `limit`
   parameter of `GET /v1/reports` (default 50, capped at 1000, junk ignored).
3. **`docs/architecture.md`**: a mermaid diagram of the data flow from results files through `gate` to the PR
   comment, the three front ends, and where `calibrate` and `replay` sit; a table of packages. Linked from the
   README.

### How it was verified

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # round 2 -> this round
     28 PASS: -> 34 PASS:   (1 SKIP: TestPGStore without AGENTGATE_TEST_DSN)
$ go test -race -count=1 ./...      # all ok
$ golangci-lint run ./...           # 0 issues
```

Coverage: `replay` 80.0% -> 83.6%, `server` 52.4% -> 57.6%. New replay tests: text body saved and replayed,
failed save keeps the old file, bad arguments, bad cassette line with its number, proxy returns 400 for non-JSON
arguments.

### Not verified

The mermaid diagram was not rendered locally (no mermaid renderer in this environment); labels are quoted to avoid
parser issues. Calibration results are unaffected (no code on that path changed).

### Files touched

`replay/replay.go`, `replay/http.go`, `replay/replay_test.go`, `server/server_test.go`, `docs/architecture.md` (new),
`README.md`, `IMPROVEMENTS.md`.
