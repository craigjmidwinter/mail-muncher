// Package imap fetches mail from any IMAP4rev1/rev2 server behind
// internal/provider.Provider.
//
// It is the low-friction onboarding path: an app password from the mail
// provider's own settings page, with no OAuth client to register, no consent
// screen, and no verification. See docs/configuration.md for the config block.
//
// # Read-only, structurally
//
// Every folder is opened with EXAMINE rather than SELECT, and every body is
// fetched with BODY.PEEK[] rather than BODY[]. Either alone would be enough on
// a compliant server; both are used because nothing on the server side is
// obliged to protect a client from itself, and the one thing this tool must
// never do to a mailbox it does not own is mark mail read. There is no code
// path here that issues STORE, APPEND, EXPUNGE, or a non-PEEK body fetch.
//
// # Incremental sync
//
// Each mailbox is tracked independently in provider.SyncState.Extra under two
// keys written as a pair:
//
//	imap.<mailbox>.uidvalidity = "1650000000"
//	imap.<mailbox>.last_uid    = "48213"
//
// A cycle that finds the stored UIDVALIDITY still current fetches
// `UID FETCH <last_uid+1>:*`. A cycle that finds it changed — or absent, which
// is the first-ever run — throws the stored UID away and resyncs from
// `initial_lookback`, because UIDVALIDITY changing is the protocol saying every
// UID you remember now names a different message. Trusting the old cursor
// through that would skip or duplicate mail silently, so the cursor is not
// trusted.
//
// # Message identity
//
// A message's provider id is `<account>:<mailbox>:<uidvalidity>:<uid>`. The
// UIDVALIDITY is part of it deliberately: after a validity change, UID 5 is a
// different message, so an id that omitted it would collide with the old one
// and the sinks would skip the new message as already stored. The cost is that
// a validity change re-archives the mail it resyncs under fresh filenames —
// duplicated mail is recoverable, silently dropped mail is not.
//
// Threading is left to internal/model: IMAP has no native conversation id, so
// RawMessage.ThreadID is empty and model.Parse synthesizes one from the
// References chain.
package imap
