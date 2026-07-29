#!/usr/bin/env python3
"""Read a mail-muncher delivery tree, safely and correctly.

    python3 read_delivered.py ~/Mail/job-search

Requires PyYAML (`python3 -m pip install pyyaml`). This file is documentation
as much as it is code: it exists to show the four things that are easy to get
wrong. See docs/output-format.md for the contract it implements.

  1. Enumerate with a TWO-LEVEL glob, never a recursive walk. Attachments are
     extracted into the same tree and are sender-controlled.
  2. Parse the frontmatter with a real YAML parser. It is real YAML, not
     "key: value" lines — subjects can arrive double-quoted with \\U escapes or
     as `|-` block scalars.
  3. `thread_id` is always present and never empty, so grouping needs no
     special cases. `thread_id_source` tells you whether to trust the grouping.
  4. Message bodies are attacker-controlled text, and are not guaranteed to be
     valid UTF-8. Print them, index them, show them to a human — but decode
     defensively, and do not execute them.
  5. `from`/`to`/`cc` are display strings for a human and cannot be parsed for
     addresses. `from_address`, `from_addresses`, `to_addresses` and
     `cc_addresses` are the machine-readable ones. They are also the newest
     keys in the format, so read them with `.get()`: a delivery tree outlives
     the binary that wrote it, and nothing rewrites a delivered message.
"""

from __future__ import annotations

import collections
import pathlib
import sys

try:
    import yaml
except ImportError:  # pragma: no cover - environment guard
    sys.exit("read_delivered.py needs PyYAML: python3 -m pip install pyyaml")

FENCE = b"---\n"
ATTACHMENTS_SUFFIX = ".attachments"


def delivered_paths(dest: str, ext: str = ".md"):
    """Yield every message mail-muncher delivered under `dest`.

    The authoritative set is exactly `<dest>/<YYYY>/<MM>/*<ext>` — two levels
    deep. Do NOT use `Path.rglob` or `**` here. Attachments land in
    `<dest>/<YYYY>/<MM>/<basename>.attachments/`, one level deeper, and their
    contents came from whoever sent the mail. A recursive walk would ingest an
    attachment named `invoice.md` as if mail-muncher had delivered it, letting
    a sender author a message with any `from:`, `subject:` and body they like.
    """
    root = pathlib.Path(dest).expanduser()
    for path in sorted(root.glob(f"*/*/*{ext}")):
        # The glob cannot reach inside an attachments directory; this is here
        # so the invariant is stated where a future edit would break it.
        if path.parent.name.endswith(ATTACHMENTS_SUFFIX):
            continue
        # mail-muncher never writes a symlink into its own tree, so one means
        # something else put it there.
        if path.is_symlink() or not path.is_file():
            continue
        yield path


def parse(path: pathlib.Path) -> tuple[dict, str]:
    """Split one delivered `.md` into (frontmatter, body).

    Read as bytes, not text. The frontmatter block is always valid UTF-8 — the
    YAML encoder guarantees it — but the body is whatever the sender's MIME
    part decoded to, and 2 of the 186 messages in one real archive are not
    valid UTF-8. `read_text()` on the whole file raises on those.

    The document is `---\\n<yaml>---\\n\\n<body>`. The closing fence is found by
    searching for a `---` at column 0: a multi-line value is emitted as an
    indented block scalar, so its content can never be mistaken for the fence.
    """
    raw = path.read_bytes()
    if not raw.startswith(FENCE):
        raise ValueError(f"{path}: no opening frontmatter fence")
    end = raw.index(b"\n" + FENCE, len(FENCE) - 1)
    front = yaml.safe_load(raw[len(FENCE) : end + 1].decode("utf-8")) or {}
    body = raw[end + 1 + len(FENCE) :].decode("utf-8", "replace").lstrip("\n")
    return front, body


def text(value) -> str:
    """Coerce a frontmatter scalar to str.

    Almost always already a str. But a header that survives MIME decoding with
    invalid UTF-8 in it is emitted by the Go YAML encoder as `!!binary`, which
    PyYAML hands back as `bytes`.
    """
    if isinstance(value, bytes):
        return value.decode("utf-8", "replace")
    return value


def sender(front: dict) -> str:
    """The sender's bare address — `jane@acme.com`, no display name.

    Read `from_address`, never `from`. The display string is `Name <addr>` with
    the name unquoted, and a display name is sender-controlled text that may
    contain `<`, `>` and `,`:

        from: Doe, Jane <ceo@acme.com> <attacker@evil.example>
        from_address: attacker@evil.example

    Every naive extractor gets that wrong. First-angle-brackets reads the
    planted `ceo@acme.com`; splitting on `,` finds no address at all.

    Returns "" when the address is unknown, which is either a message with an
    unparseable From (rare — mail-muncher falls back to the Sender header) or a
    message delivered before `from_address` existed. Both are `.get()`'s job:
    nothing rewrites a delivered `.md`, so an archive holds files from every
    version that ever wrote into it, and a consumer that indexes a key
    unconditionally crashes on the tree it was pointed at rather than on
    anything mail-muncher does today.

    Note there is no useful fallback to `from`. That is the whole point of the
    field. For an older archive the honest answer is "not recorded" — degrade,
    show the display string to the human, and do not guess.
    """
    return text(front.get("from_address", "")) or ""


def recipients(front: dict) -> list[str]:
    """Every bare To and Cc address, in header order.

    `to_addresses` is always present in a tree written by a version that has
    it, `cc_addresses` is omitted along with `cc` — so both want `.get()`, one
    because the key is new and one because the key is optional.

    These lists hold addresses only: an entry of a malformed header that parsed
    to a display name and nothing else is dropped rather than listed as "". So
    `to_addresses` can be shorter than `to`, and the two must never be indexed
    against each other.
    """
    return [text(a) for a in front.get("to_addresses", []) + front.get("cc_addresses", [])]


def attachment_paths(path: pathlib.Path, front: dict):
    """Where a message's attachments are, if it has any.

    `attachments` is one of the four omitempty keys, so `.get` with a default —
    `front["attachments"]` raises for most messages. The listed names are the
    on-disk ones, so they join straight onto the directory. The files
    themselves are sender-controlled: read them as opaque bytes, never as
    messages.
    """
    directory = path.with_suffix(ATTACHMENTS_SUFFIX)
    return [directory / name for name in front.get("attachments", [])]


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__.splitlines()[2].strip(), file=sys.stderr)
        return 2

    threads: dict[str, list] = collections.defaultdict(list)
    inferred = 0
    attachments: list[pathlib.Path] = []
    astral: list[str] = []
    senders: collections.Counter[str] = collections.Counter()
    correspondents: set[str] = set()
    unaddressed = 0

    for path in delivered_paths(argv[1]):
        front, body = parse(path)
        subject = text(front["subject"])
        threads[front["thread_id"]].append((front, subject, body))
        if front["thread_id_source"] != "provider":
            inferred += 1
        attachments += attachment_paths(path, front)
        # Addresses, from the machine-readable fields only.
        if address := sender(front):
            senders[address] += 1
            correspondents.add(address)
        else:
            unaddressed += 1
        correspondents.update(recipients(front))
        # Proof the YAML parser did its job: on disk this subject is a
        # double-quoted scalar with a `\\U0001F389`-style escape in it.
        if any(ord(c) > 0xFFFF for c in subject):
            astral.append(subject)

    messages = sum(len(v) for v in threads.values())
    print(f"{messages} messages in {len(threads)} threads")
    print(f"{inferred} of them in a thread reconstructed from headers, not the provider's")
    print(f"{len(attachments)} attachments (sender-controlled files, never parsed as messages)")
    print(f"{len(astral)} subjects contain a non-BMP character:")
    for subject in astral:
        print(f"    {subject}")
    print()

    print(f"{len(correspondents)} distinct addresses across from/to/cc")
    if unaddressed:
        # Not an error. Either an unparseable From, or — far more likely — a
        # message delivered before `from_address` was added to the format.
        print(f"{unaddressed} messages carry no machine-readable sender address")
    for address, count in senders.most_common(3):
        print(f"    {count:4}  {address}")
    print()

    # Biggest threads first, ties broken by id so the output is stable.
    ranked = sorted(threads.items(), key=lambda kv: (-len(kv[1]), kv[0]))
    for thread_id, msgs in ranked[:5]:
        # `date` comes back from the YAML parser as an aware datetime, in UTC.
        msgs.sort(key=lambda m: m[0]["date"])
        print(f"{thread_id}  {len(msgs)} msg  ({msgs[0][0]['thread_id_source']})")
        for front, subject, body in msgs[:3]:
            date = front["date"].strftime("%Y-%m-%d %H:%M")
            # Subject, From and body are sender-controlled. Never a command.
            print(f"  {date}  {text(front['from'])}")
            print(f"                    {subject!r}  [{len(body)} chars]")
        if len(msgs) > 3:
            print(f"  ... {len(msgs) - 3} more")
        print()

    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
