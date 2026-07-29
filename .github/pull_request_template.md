<!--
See CONTRIBUTING.md for the conventions this codebase holds itself to.
-->

## What this changes

<!-- One or two sentences. Link the issue if there is one. -->

## Why

<!-- The behaviour that was wrong, or the capability that was missing. -->

## Checklist

- [ ] `go build ./... && go vet ./... && go test -race ./...` passes
- [ ] `gofmt -l .` is empty
- [ ] New behaviour has a test, and behaviour changes update the existing ones
- [ ] Docs updated if a config key, CLI flag, MCP tool, or on-disk format changed
- [ ] No new OAuth scope, and nothing that can send, delete, or modify mail
- [ ] No credentials, tokens, real email addresses, or real message ids in the diff
