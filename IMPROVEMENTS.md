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
run to run at the 1e-15 level because of map iteration order in float sums).

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
