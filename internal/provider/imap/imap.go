package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/provider"
)

// DefaultDialTimeout bounds the TCP connect and TLS handshake.
const DefaultDialTimeout = 30 * time.Second

// ExtraPrefix namespaces this provider's keys in provider.SyncState.Extra.
const ExtraPrefix = "imap."

var _ provider.Provider = (*Provider)(nil)

// Options describes one IMAP account's connection and fetch behaviour. Only
// Host, Username and PasswordCmd have no working default.
type Options struct {
	// Host is the IMAP server hostname.
	Host string
	// Port is the IMAP port. Zero means config.DefaultIMAPPort.
	Port int
	// Username is the login name.
	Username string
	// PasswordCmd is the shell command whose stdout is the password. See
	// runPasswordCmd.
	PasswordCmd string
	// PasswordTimeout bounds one PasswordCmd run. Zero means
	// DefaultPasswordTimeout.
	PasswordTimeout time.Duration
	// Mailboxes are the folders to fetch, in order. Empty means
	// [config.DefaultIMAPMailbox].
	Mailboxes []string
	// TLS chooses implicit TLS on connect. Unlike the config key this is a
	// plain bool with a false zero value, because a Provider is only ever built
	// through OptionsFromAccount or by a caller stating what it wants.
	TLS bool
	// TLSConfig overrides the TLS settings. Nil means a default configuration
	// with ServerName set to Host. Tests use it to trust a throwaway CA; there
	// is no config key for it, and there should not be one.
	TLSConfig *tls.Config
	// InitialLookback bounds how far back a first-ever sync — and any resync
	// forced by a UIDVALIDITY change — reaches. Zero or negative means the
	// whole mailbox.
	InitialLookback time.Duration
	// DialTimeout bounds the TCP connect and TLS handshake. Zero means
	// DefaultDialTimeout.
	DialTimeout time.Duration
	// Retry is the backoff applied to establishing the connection. The zero
	// value means provider.DefaultRetryConfig.
	Retry provider.RetryConfig
	// Logger receives the warnings this provider has to raise, chiefly the
	// UIDVALIDITY resync. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

// Provider fetches mail from one IMAP account. Build one with New or
// NewFromAccount.
//
// A Provider holds no connection between fetches: each Fetch runs the
// password command, dials, logs in, and logs out again. That costs a handshake
// per cycle and buys a daemon that survives a rotated app password, a server
// restart, and a laptop suspend without any reconnection logic of its own.
type Provider struct {
	account string
	opts    Options
}

// New builds a Provider for the named account.
func New(account string, opts Options) (*Provider, error) {
	if strings.TrimSpace(account) == "" {
		return nil, errors.New("imap: account name is required")
	}
	if strings.TrimSpace(opts.Host) == "" {
		return nil, errors.New("imap: host is required")
	}
	if strings.TrimSpace(opts.Username) == "" {
		return nil, errors.New("imap: username is required")
	}
	return &Provider{account: account, opts: opts.withDefaults()}, nil
}

// NewFromAccount is the pipeline's entry point: it derives everything from a
// validated config account.
//
// ctx is accepted for symmetry with the other providers' constructors; nothing
// here does I/O until Fetch.
func NewFromAccount(_ context.Context, account *config.Account) (*Provider, error) {
	if account == nil {
		return nil, errors.New("imap: nil account")
	}
	if account.IMAP == nil {
		return nil, fmt.Errorf("imap: account %q has no `imap:` block", account.Name)
	}
	return New(account.Name, OptionsFromAccount(account))
}

// OptionsFromAccount reads the `imap:` block off a config account, applying the
// same defaults Load would have. A nil account, or one with no `imap:` block,
// yields the zero Options.
func OptionsFromAccount(account *config.Account) Options {
	if account == nil || account.IMAP == nil {
		return Options{}
	}
	m := account.IMAP
	return Options{
		Host:            m.Host,
		Port:            m.PortOrDefault(),
		Username:        m.Username,
		PasswordCmd:     m.PasswordCmd,
		Mailboxes:       m.MailboxList(),
		TLS:             m.TLSEnabled(),
		InitialLookback: m.InitialLookbackDuration(),
	}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return config.ProviderIMAP }

// Account is the config account name this Provider fetches for.
func (p *Provider) Account() string { return p.account }

// Fetch implements provider.Provider.
//
// One Fetch runs `password_cmd`, opens one connection, and walks the configured
// mailboxes in order, each with its own independent cursor. Within a mailbox it
// takes one of two routes:
//
//   - Incremental. The stored UIDVALIDITY still matches what the server
//     reports, so `UID FETCH <last_uid+1>:*` asks only for what arrived since.
//   - Resync. There is no stored cursor (first-ever run) or the UIDVALIDITY
//     changed, so a UID SEARCH bounded by InitialLookback selects the window to
//     re-read. Every UID the server remembers is meaningless after a validity
//     change, so nothing about the old cursor is carried across.
//
// Mail is delivered to fn one message at a time, in UID order; fn is never
// called concurrently. fn returning an error stops the fetch and the error
// comes back unwrapped, so the pipeline's own sentinels survive the round trip.
//
// A fetch that ends early — cancelled, a dead connection, fn refusing a
// message — returns the state actually reached alongside the error: mailboxes
// already finished keep their advanced cursors, and the mailbox in flight
// keeps a cursor at the last message fn accepted. That is what lets a
// gracefully stopped daemon save its progress instead of redoing the cycle.
func (p *Provider) Fetch(ctx context.Context, state provider.SyncState, fn func(provider.RawMessage) error) (provider.SyncState, error) {
	out := state.Clone()
	if fn == nil {
		return out, errors.New("imap: Fetch requires a non-nil callback")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	password, err := runPasswordCmd(ctx, p.opts.PasswordCmd, p.opts.PasswordTimeout)
	if err != nil {
		return out, err
	}

	// Sampled before anything is listed, for the same reason the Gmail
	// provider reads its cursor first: a message that arrives while this fetch
	// is in flight must not end up behind the watermark a later run trusts.
	syncStart := p.now()

	client, err := p.connect(ctx, password)
	if err != nil {
		return out, err
	}
	defer p.disconnect(client)

	// go-imap's client takes no context, so cancellation is delivered by
	// closing the connection under it: an in-flight command then fails, and
	// every return path below re-reads ctx.Err() and reports that instead.
	stopWatch := watchContext(ctx, client)
	defer stopWatch()

	for _, mailbox := range p.opts.Mailboxes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := p.fetchMailbox(ctx, client, mailbox, &out, fn); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			return out, err
		}
	}

	out.LastSyncTime = syncStart
	return out, nil
}

// fetchMailbox syncs one folder, updating out's cursor keys for it.
func (p *Provider) fetchMailbox(ctx context.Context, c *imapclient.Client, mailbox string,
	out *provider.SyncState, fn func(provider.RawMessage) error) error {

	log := p.logger().With("account", p.account, "mailbox", mailbox)

	// EXAMINE, not SELECT. On a compliant server this alone makes the whole
	// session incapable of setting \Seen; BODY.PEEK below is the belt to that
	// brace, because "compliant" is not something a client gets to assume.
	data, err := c.Select(mailbox, &goimap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("imap: examine mailbox %q: %w", mailbox, err)
	}

	var (
		validityKey = ExtraKey(mailbox, "uidvalidity")
		lastUIDKey  = ExtraKey(mailbox, "last_uid")
		validity    = strconv.FormatUint(uint64(data.UIDValidity), 10)
		stored      = out.GetExtra(validityKey)
		resync      = stored != validity
		lastUID     goimap.UID
	)
	switch {
	case !resync:
		lastUID = parseUID(out.GetExtra(lastUIDKey))
	case stored != "":
		// The protocol's "everything you knew is invalid" signal. Keeping the
		// old last_uid would step over new mail (if UIDs restarted lower) or
		// re-deliver old mail under ids that collide with what is already
		// archived. Neither is recoverable, so the cursor is discarded.
		log.Warn("imap uidvalidity changed; discarding the stored uid cursor and resyncing from initial_lookback",
			"stored_uidvalidity", stored, "server_uidvalidity", validity,
			"stored_last_uid", out.GetExtra(lastUIDKey), "initial_lookback", p.opts.InitialLookback)
	}

	numSet, err := p.uidRange(ctx, c, mailbox, data, resync, lastUID)
	if err != nil {
		return err
	}

	highest := lastUID
	loopErr := p.deliver(ctx, c, mailbox, data.UIDValidity, lastUID, numSet, &highest, out, fn)

	// The two keys are written as a pair or not at all. A lone uidvalidity
	// would read as "incremental from UID 0" on the next run, which is a full
	// mailbox re-read that ignores initial_lookback entirely.
	if loopErr != nil {
		if highest > 0 {
			// Everything up to `highest` really was delivered, so a caller that
			// saves this state (a graceful stop does) resumes correctly.
			out.SetExtra(validityKey, validity)
			out.SetExtra(lastUIDKey, formatUID(highest))
		}
		return loopErr
	}

	// Nothing between `highest` and the UIDNEXT observed at EXAMINE time can
	// still be waiting for us: either it was delivered, or it was excluded by
	// initial_lookback, or it was expunged. Mail that arrives after that
	// sample gets a UID at or above it and is picked up next cycle.
	if next := data.UIDNext; next > 1 && next-1 > highest {
		highest = next - 1
	}
	if highest > 0 {
		out.SetExtra(validityKey, validity)
		out.SetExtra(lastUIDKey, formatUID(highest))
	}
	return nil
}

// uidRange decides what to ask the server for, or nil when there is nothing to
// ask.
func (p *Provider) uidRange(ctx context.Context, c *imapclient.Client, mailbox string,
	data *goimap.SelectData, resync bool, lastUID goimap.UID) (goimap.NumSet, error) {

	if data.NumMessages == 0 {
		return nil, nil
	}

	if !resync {
		// `n:*` is evaluated as a range, so on a mailbox whose highest UID is
		// below n the server answers with that highest message rather than
		// with nothing. UIDNEXT rules that out up front; deliver() drops any
		// stragglers for servers that do not report it.
		if data.UIDNext > 0 && lastUID+1 >= data.UIDNext {
			return nil, nil
		}
		return goimap.UIDSet{{Start: lastUID + 1, Stop: 0}}, nil
	}

	if p.opts.InitialLookback <= 0 {
		return goimap.UIDSet{{Start: 1, Stop: 0}}, nil
	}

	since := p.now().Add(-p.opts.InitialLookback)
	found, err := c.UIDSearch(&goimap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("imap: search mailbox %q since %s: %w", mailbox, since.Format(time.RFC3339), err)
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	return goimap.UIDSetNum(uids...), nil
}

// deliver runs the UID FETCH and hands each message to fn, recording the
// highest UID it got through in *highest.
func (p *Provider) deliver(ctx context.Context, c *imapclient.Client, mailbox string, validity uint32,
	lastUID goimap.UID, numSet goimap.NumSet, highest *goimap.UID,
	out *provider.SyncState, fn func(provider.RawMessage) error) error {

	if numSet == nil {
		return nil
	}

	// BODY.PEEK[], never BODY[]. A bare BODY[] marks every message it touches
	// \Seen, and there is nothing behind this line — no server-side setting, no
	// second check — that would stop it. This one field is the entire
	// read-only guarantee for the message bodies.
	section := &goimap.FetchItemBodySection{Peek: true}
	cmd := c.Fetch(numSet, &goimap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		BodySection:  []*goimap.FetchItemBodySection{section},
	})

	err := func() error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			msg := cmd.Next()
			if msg == nil {
				return nil
			}
			buf, err := msg.Collect()
			if err != nil {
				return fmt.Errorf("imap: read message from mailbox %q: %w", mailbox, err)
			}
			if buf.UID == 0 || buf.UID <= lastUID {
				// No UID (the server ignored the item) or a straggler from the
				// `n:*` range quirk. Either way it is not new.
				continue
			}
			raw := buf.FindBodySection(section)
			if raw == nil {
				return fmt.Errorf("imap: mailbox %q uid %d: server returned no body section", mailbox, buf.UID)
			}
			if buf.UID > *highest {
				*highest = buf.UID
			}

			id := MessageID(p.account, mailbox, validity, buf.UID)
			if out.Seen(id) {
				continue
			}
			// ThreadID stays empty: IMAP has no conversation id, and
			// model.Parse derives one from the References chain.
			if err := fn(provider.RawMessage{
				ID:           id,
				Raw:          raw,
				InternalDate: buf.InternalDate,
				Labels:       []string{mailbox},
			}); err != nil {
				return err
			}
		}
	}()

	// Close drains whatever is left on the wire, so the connection stays usable
	// for the next mailbox even when the loop above bailed out early.
	closeErr := cmd.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("imap: fetch from mailbox %q: %w", mailbox, closeErr)
	}
	return nil
}

// connect dials, negotiates TLS, and logs in, retrying the whole sequence with
// the shared backoff. Retrying here is free: nothing has been delivered yet, so
// a redialled connection repeats no work and cannot duplicate a message.
func (p *Provider) connect(ctx context.Context, password string) (*imapclient.Client, error) {
	return provider.RetryValue(ctx, p.opts.Retry, retryable, func(ctx context.Context) (*imapclient.Client, error) {
		return p.dial(ctx, password)
	})
}

func (p *Provider) dial(ctx context.Context, password string) (*imapclient.Client, error) {
	addr := net.JoinHostPort(p.opts.Host, strconv.Itoa(p.opts.Port))

	dialer := &net.Dialer{Timeout: p.opts.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap: dial %s: %w", addr, err)
	}

	if p.opts.TLS {
		cfg := &tls.Config{}
		if p.opts.TLSConfig != nil {
			cfg = p.opts.TLSConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = p.opts.Host
		}
		if cfg.NextProtos == nil {
			cfg.NextProtos = []string{"imap"}
		}
		tlsConn := tls.Client(conn, cfg)
		handshake, cancel := context.WithTimeout(ctx, p.opts.DialTimeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(handshake); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imap: TLS handshake with %s: %w", addr, err)
		}
		conn = tlsConn
	}

	client := imapclient.New(conn, &imapclient.Options{})
	if err := client.Login(p.opts.Username, password).Wait(); err != nil {
		_ = client.Close()
		// The password itself is never in err — go-imap reports the server's
		// tagged response, not the credentials it sent.
		return nil, fmt.Errorf("imap: login as %q to %s failed (check imap.username and what password_cmd printed): %w",
			p.opts.Username, addr, err)
	}
	return client, nil
}

// disconnect ends the session politely and then unconditionally closes the
// socket. Neither failing matters: the fetch is over, and a server that will
// not take LOGOUT will take a FIN.
func (p *Provider) disconnect(c *imapclient.Client) {
	if c == nil {
		return
	}
	if err := c.Logout().Wait(); err != nil {
		p.logger().Debug("imap logout failed", "account", p.account, "error", err)
	}
	if err := c.Close(); err != nil {
		p.logger().Debug("imap close failed", "account", p.account, "error", err)
	}
}

// watchContext closes the client when ctx is cancelled, and returns a function
// that stops watching. go-imap has no context-aware API, so this is the only
// way an in-flight FETCH of a large message can be interrupted.
func watchContext(ctx context.Context, c *imapclient.Client) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// retryable decides which connection failures are worth another attempt.
//
// A tagged NO or BAD is the server declining on purpose — a wrong password, a
// disabled account, LOGIN not permitted — and repeating it five times only
// makes the provider's rate limiter angrier. Everything else at this stage is
// transport, and transport is what backoff is for.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var imapErr *goimap.Error
	if errors.As(err, &imapErr) {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// A temporary resolver failure is worth retrying; NXDOMAIN is not.
		return dnsErr.IsTemporary || dnsErr.IsTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ExtraKey is the provider.SyncState.Extra key holding `field` for one mailbox.
// The layout is API: state files written by an older build must keep resolving.
func ExtraKey(mailbox, field string) string {
	return ExtraPrefix + mailbox + "." + field
}

// MessageID is the provider-scoped stable id for one message:
// `<account>:<mailbox>:<uidvalidity>:<uid>`.
//
// It feeds the sinks' filename digest, so its shape is load-bearing — see the
// package doc for why UIDVALIDITY belongs in it.
func MessageID(account, mailbox string, uidValidity uint32, uid goimap.UID) string {
	return account + ":" + mailbox + ":" +
		strconv.FormatUint(uint64(uidValidity), 10) + ":" + formatUID(uid)
}

func formatUID(uid goimap.UID) string {
	return strconv.FormatUint(uint64(uid), 10)
}

// parseUID reads a stored last_uid. Anything unreadable is treated as no
// cursor at all, which costs a re-read and never a skipped message.
func parseUID(s string) goimap.UID {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0
	}
	return goimap.UID(n)
}

// now is the Provider's clock, always UTC so persisted state compares cleanly
// across runs.
func (p *Provider) now() time.Time {
	if p.opts.Now != nil {
		return p.opts.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Provider) logger() *slog.Logger {
	if p.opts.Logger != nil {
		return p.opts.Logger
	}
	return slog.Default()
}

func (o Options) withDefaults() Options {
	if o.Port <= 0 {
		o.Port = config.DefaultIMAPPort
	}
	if len(o.Mailboxes) == 0 {
		o.Mailboxes = []string{config.DefaultIMAPMailbox}
	} else {
		o.Mailboxes = append([]string(nil), o.Mailboxes...)
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	if o.PasswordTimeout <= 0 {
		o.PasswordTimeout = DefaultPasswordTimeout
	}
	// Retry needs no defaulting: provider.RetryValue fills its own zero fields
	// from DefaultRetryConfig.
	return o
}
