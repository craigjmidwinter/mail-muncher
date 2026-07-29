// Package sink writes matched messages to their destinations, such as raw
// .eml files and rendered Markdown documents.
//
// # Layout
//
// Every sink files a message under the rule's destination directory by the
// message's date, in UTC:
//
//	<rule.dest>/<YYYY>/<MM>/<basename>.eml
//	<rule.dest>/<YYYY>/<MM>/<basename>.md
//	<rule.dest>/<YYYY>/<MM>/<basename>.attachments/   (markdown, when there are attachments)
//
// The basename is shared by all formats (see Basename), so the .eml and the
// .md of one message sit side by side and sort together.
//
// # Idempotency
//
// The basename embeds a digest of the account and provider message id, which
// makes the destination path a pure function of the message's identity. A sink
// therefore reads "the file is already there" as "this message was archived by
// an earlier run" and skips it, writing nothing. That is what makes re-running
// safe: overlapping cron invocations, a replay after state loss, or a
// deliberate re-scan all converge on the same tree.
//
// The check is not a check-then-write, though. A message file is published by
// writing a temp file in the destination directory and then hard-linking it to
// its final name, which the kernel refuses if the name is taken. "Already
// there" is therefore decided at the instant the name is claimed: two writers
// racing the same message cannot both win, and neither can overwrite a file
// that appeared after the other looked. A run that dies mid-write leaves
// neither a partial file nor a temp file behind, because the content is
// complete before the name exists at all.
//
// # Filesystem trust
//
// The destination tree is not assumed to be exclusively this program's. It is
// read, and may be written near, by autonomous agents; a hostile or merely
// confused neighbour with write access could plant links in it. So nothing here
// follows one: existence checks are Lstat rather than Stat, the year, month and
// attachments directories are created a component at a time and refuse an
// existing symlink instead of descending through it, and files are published
// with primitives that cannot write through a link at the target name. A
// symlink anywhere in the layout is an error for that one message — logged and
// counted, never a silent skip. The rule's own dest: is exempt: the operator
// chose that path and may legitimately point it at another volume.
//
// Everything is created owner-only, 0700 directories and 0600 files. Archived
// mail is private correspondence, and decoded attachments are the same
// documents that were private in the mailbox.
//
// # Dry runs
//
// Sink.Path and Sink.Plan answer "where would this go?" without writing
// anything — Path does no I/O at all, Plan adds a single existence check — so
// a --dry-run can report the exact path a real run would produce.
//
// # Fidelity
//
// The .eml is the fidelity copy: model.Message.Raw, byte for byte. The
// Markdown rendering is a convenience for reading and grepping, and does lose
// things — most visibly, inline images referenced with `cid:` are left as
// unresolved links (`![alt](cid:...)`) rather than being written out and
// re-pointed at the attachments directory.
package sink
