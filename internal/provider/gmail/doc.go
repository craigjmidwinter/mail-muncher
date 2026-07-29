// Package gmail implements the Gmail provider: OAuth against a user-supplied
// desktop OAuth client, cached token storage, and (in later files) message
// fetching via the Gmail API.
//
// # Authentication
//
// mail-muncher never ships an OAuth client of its own. The user creates a
// "Desktop app" OAuth client in the Google Cloud Console, downloads the client
// JSON, and points `gmail.credentials_file` at it; see docs/gmail-setup.md.
// Only the read-only scope is requested (see Scope).
//
// `mail-muncher auth --account <name>` runs the loopback flow: an ephemeral
// HTTP server is started on 127.0.0.1, its address is used as the OAuth
// redirect URI, the consent URL is printed (and best-effort opened in a
// browser), and the authorization code that Google redirects back is exchanged
// for a token. The token is written to `gmail.token_file` with mode 0600 by an
// atomic temp-file-plus-rename write. See Authorizer.
//
// # Runtime
//
// Everything after `auth` is non-interactive. Callers obtain credentials with
// TokenSource or NewClient, both of which read the cached token, refresh it
// automatically when it expires, and write the refreshed token back to disk so
// the next process starts from the newest one:
//
//	client, err := gmail.NewClient(ctx, opts)
//	if err != nil {
//		return err
//	}
//	svc, err := gmailapi.NewService(ctx, option.WithHTTPClient(client))
//
// A refresh that Google rejects (revoked access, expired refresh token,
// changed password) surfaces as an error matching ErrTokenRejected whose
// message tells the user to re-run `auth`.
package gmail
