---
title: Make the lint set fast enough to be a CI gate
date: "2026-08-20"
time: "19:42:01"
tags:
    - ci
    - hygiene
summary: 51 minutes on a GitHub runner, then exit 4 on its own timeout, while reporting 0 issues
type: task
status: doing
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

## Measured: gosec is NOT the culprit

The suspicion below was wrong, and correcting it is the point of this section.
Same machine (M-series laptop), same cold-cache conditions, whole repo:

| Linter set | Cold run |
| --- | --- |
| Full set, including `gosec` | ~26 min |
| Same set with `gosec` disabled | **25m 54s** |

Removing `gosec` bought essentially nothing. The cost is in the base set:
`staticcheck` and `unused` build whole-program facts across a graph that pulls
in grpc, otel and google-api-go-client, and that is the whole budget. `gosec`
rides along on analysis already being done, so it is close to free — there is
no reason to drop the linter that found the OAuth callback bug.

(51 min on the GitHub runner vs ~26 min locally is a two-core runner doing the
same work, not a separate problem.)

## The original, incorrect suspicion
`staticcheck`, `unused` and `gosec` all build whole-program SSA/type facts, and this module's graph pulls in grpc, otel and google-api-go-client. On a 2-core runner that is the whole budget. A local measurement with `gosec` disabled was started to isolate its share; whoever picks this up should finish that measurement rather than guess.

## What is worth preserving
Do not simply delete `gosec` and call it done. It is the linter that found the pass's only real bug — G705, the OAuth loopback callback interpolating Google's `error_description` into an HTML page unescaped. Whatever shape this takes has to keep that class of check running *somewhere*.

## Options, revised after the measurement
1. ~~Split `gosec` into a scheduled job.~~ **Dead.** The measurement above shows it saves nothing.
2. **Fix the caching.** Most promising lead; try this first. Run one saved only **579 KB** of `~/.cache/golangci-lint`, which cannot be holding whole-program facts — something about that cache is not working. `actions/setup-go` already sets `cache: true` for GOCACHE, so the gap is likely golangci-lint's own analysis cache not surviving between runs. If a warm cache brings it under a few minutes, the gate goes straight back in with the full set intact.
3. **Split by cost, not by linter identity.** Make the PR gate only the linters that need no type information (seconds), and run the full type-aware set nightly against main. Keeps a real per-PR gate and full coverage daily, at the price of finding type-aware issues a day late.
4. **Accept a nightly-only lint.** Simplest and honest: `make lint` locally plus one scheduled run. Close to where the repo sits today, plus the schedule.

Try 2 before 3 or 4 — a 579 KB cache is a smell, not a law of nature.

## State right now
`.golangci.yml` is committed and clean (`golangci-lint run` → 0 issues locally), all 19 findings it surfaced are fixed, and `make lint` runs the full set for anyone who has the binary. CI runs gofmt, vet and `test -race` as it always did. CONTRIBUTING says plainly that CI does not run the linter and why.
