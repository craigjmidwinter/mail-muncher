---
title: Config loader and validation
date: "2026-07-28"
time: "15:33:35"
tags:
    - config
summary: YAML schema for accounts + rules, path expansion, validate command
type: task
status: done
effort: M
epic: core-skeleton-and-config
---

## Context
All flexibility lives in one YAML config. Rules reference an externally-managed domain-list file (maintained by another app) — the config only stores the *path*.

## Spec
Implement `internal/config` with `gopkg.in/yaml.v3`. Target schema:

```yaml
state_dir: ~/.local/state/mail-muncher   # default if omitted

accounts:
  - name: personal            # unique, required
    provider: gmail           # only "gmail" recognized for now
    gmail:
      credentials_file: ~/.config/mail-muncher/credentials.json
      token_file: ~/.config/mail-muncher/token.json
      query: ""               # optional Gmail server-side pre-filter

rules:                        # evaluated in order; FIRST MATCH WINS
  - name: job-search          # unique, required
    account: personal         # optional; default = all accounts
    match:                    # a match node, see filter-engine epic
      any:
        - from_domains_file: ~/.local/share/jobsearch/domains.txt
    dest: ~/Mail/job-search   # required; created if missing
    formats: [eml]            # subset of [eml, markdown]; default [eml]
```

- Expand `~` and `$ENV_VAR` in every path field at load time.
- `Load(path) (*Config, error)` returns typed structs. Unknown YAML keys are an error (use `yaml.Decoder.KnownFields(true)`).
- Validation (shared by `validate` command and `run`/`daemon` startup): unique names; every rule's `account` exists; `formats` values legal; `dest` non-empty. Missing `from_domains_file` at validate time is a WARNING not an error (the other app may not have written it yet).
- `validate` command prints all problems and exits non-zero on errors.
- The `match:` node can be parsed as opaque `yaml.Node` for now if the filter engine task hasn't landed; leave a TODO hook.

## Acceptance
- Unit tests: happy path, unknown key rejection, tilde/env expansion, duplicate rule name, bad format value.
- `mail-muncher validate --config testdata/config.yml` works end to end.
