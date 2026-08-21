---
title: Make the lint set fast enough to be a CI gate
date: "2026-08-20"
time: "19:42:01"
tags:
    - ci
    - hygiene
summary: 51 minutes on a GitHub runner, then exit 4 on its own timeout, while reporting 0 issues
type: task
status: todo
effort: M
epic: run-modes-and-operations
---

Raised by the 2026-08-20 standards pass, which added the gate, measured it, and then removed it.

## What happened
The `Lint` step went into CI's `build` job and turned a sub-3-minute job into a 52-minute failure:

    0 issues.
    level=error msg="Timeout exceeded: try increasing it by passing --timeout option"
    golangci-lint exit with code 4
    Ran golangci-lint in 3069974ms

Note `0 issues` — the code was clean. CI went red purely on the clock.

## Why raising the timeout is not the fix
30m was already the configured timeout, raised from 10m before the commit shipped, after a local cold run measured over 25 minutes. The runner still took 51. Raising it again would produce a green 51-minute gate, which is worse than no gate: it delays every PR and nobody would wait for it.

The action's cache is not the answer either. Run 1 saved **579 KB** of `~/.cache/golangci-lint` — far too little to be caching whole-program analysis facts, so run 2 would not have been meaningfully cheaper.

## The suspected cost
`staticcheck`, `unused` and `gosec` all build whole-program SSA/type facts, and this module's graph pulls in grpc, otel and google-api-go-client. On a 2-core runner that is the whole budget. A local measurement with `gosec` disabled was started to isolate its share; whoever picks this up should finish that measurement rather than guess.

## What is worth preserving
Do not simply delete `gosec` and call it done. It is the linter that found the pass's only real bug — G705, the OAuth loopback callback interpolating Google's `error_description` into an HTML page unescaped. Whatever shape this takes has to keep that class of check running *somewhere*.

## Options, roughly in order of appeal
1. Isolate the expensive linter and split it into its own job that runs on a schedule (weekly) or on `workflow_dispatch`, leaving the fast set as the PR gate. Keeps a real gate on every PR and keeps gosec's coverage.
2. Cut the enabled set to the fast ones for CI and keep the full set for `make lint` locally. Costs the local/CI parity the config comment currently promises, so the comment would need to change too.
3. Investigate whether `--concurrency`, `GOGC`, or a warmed `actions/cache` over `~/.cache/golangci-lint` **and** GOCACHE together bring it under a few minutes. Worth one timeboxed attempt before falling back to 1 or 2.

## State right now
`.golangci.yml` is committed and clean (`golangci-lint run` → 0 issues locally), all 19 findings it surfaced are fixed, and `make lint` runs the full set for anyone who has the binary. CI runs gofmt, vet and `test -race` as it always did. CONTRIBUTING says plainly that CI does not run the linter and why.
