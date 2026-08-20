package imap_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Everything in this file exists so the provider can be driven against a real
// IMAP server — go-imap's in-memory one — over a real socket, with no network
// beyond loopback. Nothing here mocks the protocol, which is the point: the
// behaviours under test (UIDVALIDITY, the `n:*` range, \Seen on a non-PEEK
// fetch) are server behaviours, and a fake would only assert what we already
// believe.

const (
	testUsername = "muncher@example.test"
	testPassword = "app-password-1234"
	// passwordCmd prints the password the way `pass` would: on stdout, with a
	// trailing newline the provider must strip.
	passwordCmd = "printf '%s\\n' 'app-password-1234'"
)

// testServer is an in-memory IMAP server listening on loopback.
type testServer struct {
	t         *testing.T
	Addr      string
	Host      string
	Port      int
	User      *imapmemserver.User
	TLSConfig *tls.Config // client-side config trusting the throwaway CA; nil when plaintext

	// selectMu guards selects, which records every SELECT/EXAMINE the server
	// has seen, across every connection — the provider dials, logs in and logs
	// out again on each Fetch (see the doc comment on Provider), so a single
	// test can easily span more than one connection.
	selectMu sync.Mutex
	selects  []selectCall
}

// selectCall is one SELECT or EXAMINE the server received, as the server
// itself parsed it — not as the client claims to have sent it.
type selectCall struct {
	Mailbox  string
	ReadOnly bool
}

// recordSelect is called by recordingSession.Select for every mailbox this
// server opens.
func (s *testServer) recordSelect(mailbox string, readOnly bool) {
	s.selectMu.Lock()
	defer s.selectMu.Unlock()
	s.selects = append(s.selects, selectCall{Mailbox: mailbox, ReadOnly: readOnly})
}

// Selects returns every SELECT/EXAMINE this server has received so far,
// across every connection, in order.
func (s *testServer) Selects() []selectCall {
	s.selectMu.Lock()
	defer s.selectMu.Unlock()
	out := make([]selectCall, len(s.selects))
	copy(out, s.selects)
	return out
}

// recordingSession wraps the real in-memory session and observes the
// imap.SelectOptions each SELECT/EXAMINE actually carried. This is the
// closest seam to the wire available through this harness: imapmemserver
// itself does not expose which command a client sent, but imapserver hands
// every Session.Select call the ReadOnly bit it parsed off the wire (SELECT
// decodes to false, EXAMINE to true — see go-imap's imapserver/select.go), so
// recording that argument is equivalent to recording the command name.
//
// The wrapped field is typed as the plain imapserver.Session interface, which
// only promotes that interface's methods — not the richer
// imapserver.SessionIMAP4rev2 (Namespace, Move) the wrapped *serverSession
// actually implements. imapserver type-asserts for those at runtime to decide
// what to advertise, so without forwarding them explicitly here the server
// would advertise IMAP4rev2 and then panic the first time a session in this
// wrapper was asked for it. Namespace and Move forward to the underlying
// session; nothing under test calls either.
type recordingSession struct {
	imapserver.Session
	srv *testServer
}

var (
	_ imapserver.SessionIMAP4rev2 = (*recordingSession)(nil)
)

func (s *recordingSession) Select(mailbox string, options *goimap.SelectOptions) (*goimap.SelectData, error) {
	s.srv.recordSelect(mailbox, options != nil && options.ReadOnly)
	return s.Session.Select(mailbox, options)
}

func (s *recordingSession) Namespace() (*goimap.NamespaceData, error) {
	ns, ok := s.Session.(imapserver.SessionNamespace)
	if !ok {
		return nil, errors.New("recordingSession: wrapped session does not support NAMESPACE")
	}
	return ns.Namespace()
}

func (s *recordingSession) Move(w *imapserver.MoveWriter, numSet goimap.NumSet, dest string) error {
	mv, ok := s.Session.(imapserver.SessionMove)
	if !ok {
		return errors.New("recordingSession: wrapped session does not support MOVE")
	}
	return mv.Move(w, numSet, dest)
}

// newTestServer starts a plaintext in-memory IMAP server with an empty INBOX.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return startTestServer(t, false)
}

// newTLSTestServer starts the same server behind implicit TLS, so the default
// `tls: true` path is exercised end to end rather than assumed.
func newTLSTestServer(t *testing.T) *testServer {
	t.Helper()
	return startTestServer(t, true)
}

func startTestServer(t *testing.T, useTLS bool) *testServer {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUsername, testPassword)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	mem.AddUser(user)

	ts := &testServer{t: t, User: user}

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &recordingSession{Session: mem.NewSession(), srv: ts}, nil, nil
		},
		InsecureAuth: !useTLS,
		Caps: goimap.CapSet{
			goimap.CapIMAP4rev1: {},
			goimap.CapIMAP4rev2: {},
		},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	if useTLS {
		serverCfg, clientCfg := testTLSConfigs(t)
		ln = tls.NewListener(ln, serverCfg)
		ts.TLSConfig = clientCfg
	}

	go func() {
		// Serve returns as soon as the listener closes, which t.Cleanup does.
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() { _ = srv.Close() })

	ts.Addr = ln.Addr().String()
	host, portStr, err := net.SplitHostPort(ts.Addr)
	if err != nil {
		t.Fatalf("split %q: %v", ts.Addr, err)
	}
	ts.Host = host
	if _, err := fmt.Sscanf(portStr, "%d", &ts.Port); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return ts
}

// CreateMailbox adds a folder alongside INBOX.
func (s *testServer) CreateMailbox(name string) {
	s.t.Helper()
	if err := s.User.Create(name, nil); err != nil {
		s.t.Fatalf("create mailbox %q: %v", name, err)
	}
}

// Append stores one message and returns its UID.
func (s *testServer) Append(mailbox, subject string, received time.Time) goimap.UID {
	s.t.Helper()
	raw := rawMessage(subject)
	data, err := s.User.Append(mailbox, &literal{Reader: strings.NewReader(raw), size: int64(len(raw))},
		&goimap.AppendOptions{Time: received})
	if err != nil {
		s.t.Fatalf("append to %q: %v", mailbox, err)
	}
	return data.UID
}

// Unseen is the server's own count of messages without \Seen. It is the
// authority the PEEK test asserts against: it is computed from the flags the
// server actually holds, not from anything the client reports.
func (s *testServer) Unseen(mailbox string) uint32 {
	s.t.Helper()
	data, err := s.User.Status(mailbox, &goimap.StatusOptions{NumUnseen: true})
	if err != nil {
		s.t.Fatalf("status %q: %v", mailbox, err)
	}
	if data.NumUnseen == nil {
		s.t.Fatalf("status %q: server reported no unseen count", mailbox)
	}
	return *data.NumUnseen
}

// UIDValidity is the server's UIDVALIDITY for a mailbox.
func (s *testServer) UIDValidity(mailbox string) uint32 {
	s.t.Helper()
	data, err := s.User.Status(mailbox, &goimap.StatusOptions{UIDValidity: true})
	if err != nil {
		s.t.Fatalf("status %q: %v", mailbox, err)
	}
	return data.UIDValidity
}

// rawMessage builds a small but complete RFC822 message. The Message-Id is
// derived from the subject so a test can tell two messages apart in the bytes
// the provider hands over.
func rawMessage(subject string) string {
	return strings.Join([]string{
		"MIME-Version: 1.0",
		"From: Sender <sender@example.test>",
		"To: " + testUsername,
		"Subject: " + subject,
		fmt.Sprintf("Message-Id: <%s@example.test>", strings.ReplaceAll(subject, " ", "-")),
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body of " + subject + ".",
		"",
	}, "\r\n")
}

// literal adapts a reader to goimap.LiteralReader.
type literal struct {
	io.Reader
	size int64
}

func (l *literal) Size() int64 { return l.size }

// testTLSConfigs mints a throwaway CA-less self-signed certificate for
// 127.0.0.1 and returns the server config plus a client config that trusts it.
// Nothing here weakens verification: the client checks the chain and the name,
// against a root that exists only for the duration of this test.
func testTLSConfigs(t *testing.T) (server, client *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"mail-muncher test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return &tls.Config{
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
		}, &tls.Config{
			RootCAs:    pool,
			ServerName: "127.0.0.1",
		}
}

// errIsAny reports whether err matches any of the targets, for assertions that
// accept either of two equally correct errors.
func errIsAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
