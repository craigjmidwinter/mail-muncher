package provider

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE READ-ONLY GUARANTEE, enforced at the source level.
//
// mail-muncher never modifies a mailbox: no Gmail label/trash/send/insert
// call, no IMAP STORE/EXPUNGE/APPEND/MOVE/COPY (see CONTRIBUTING.md and
// README.md). Everywhere else that guarantee is enforced by review and
// prose. This test enforces it mechanically: it parses every non-test Go
// file under internal/provider and fails if a write-capable Gmail or IMAP
// method is ever called from provider code, so widening the surface area
// requires deleting or editing this test on purpose, in the same commit,
// where a reviewer will see it.
//
// THE FALSE-POSITIVE PROBLEM, and how it is avoided.
//
// The forbidden method names are not rare: Delete, Copy, Send, Insert and
// Store are all common Go method names on types that have nothing to do
// with mail (io.Copy, a map delete, an atomic.Int64.Store, ...) — and this
// very package has one such collision (gmail.Provider.vanished is an
// atomic.Int64, and it calls Store; see gmail.go). Matching on method name
// alone would make this test cry wolf on the first unrelated refactor, and
// a test that cries wolf gets deleted, not fixed. So instead of a bare name
// match, each language gets a check grounded in how *this codebase* actually
// reaches the API, not just what the call is named:
//
//   - Gmail: the generated client is always reached as
//     `<something>.Users.<Resource>.<Verb>(...)`, e.g.
//     `p.svc.Users.Messages.Get(...)`. A call only counts if its receiver
//     chain has that exact shape — "Users" two segments before the call, with
//     the immediate parent being a mail resource (Messages, Threads, Labels,
//     History) — or, for Drafts specifically, any call at all against
//     Users.Drafts.*, since a draft is unread mail with a Send button already
//     attached to it. `atomic.Int64.Store`, `io.Copy`, and every other
//     unrelated Verb-named call in the tree lack the `.Users.<Resource>.`
//     shape and so are ignored.
//
//   - IMAP: a call only counts if its receiver is a bare identifier that the
//     *same function* declares (as a parameter, or via `:= imapclient.New(...)`)
//     to have type *imapclient.Client. This mirrors exactly how imap.go is
//     written today — every write-capable call the provider could make goes
//     through a variable named `c` or `client` of that exact type — without
//     hard-coding those two names as an allowlist: rename the variable, and
//     the check still finds it from the declared type.
//
// Neither check does full type-checking (no go/types, no new dependency):
// both are structural pattern matches over the AST that happen to coincide,
// in this codebase, with "is this actually the Gmail or IMAP client". If a
// future refactor changes those shapes (a helper that hides `.Users.`, or a
// client stored in a struct field instead of a local/parameter), this test
// stops seeing real write calls and needs to be revisited alongside that
// refactor — which is a fair trade against false alarms on every unrelated
// Delete/Copy/Store in the tree.
func TestProviderSourceNeverCallsAWriteCapableMailboxMethod(t *testing.T) {
	fset := token.NewFileSet()

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err, "parse %s", path)

		ast.Inspect(file, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && fn.Body != nil {
				checkIMAPWrites(t, fset, fn)
			}
			if call, ok := n.(*ast.CallExpr); ok {
				checkGmailWrite(t, fset, call)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err, "walk internal/provider")
}

// gmailWriteVerbs are Gmail API methods reached via Users.<Resource>.<Verb>
// that change a mailbox: Modify relabels, Trash/Untrash/Delete/BatchDelete/
// BatchModify remove or restore mail, Send/Insert/Import inject new mail.
var gmailWriteVerbs = map[string]bool{
	"Modify":      true,
	"Trash":       true,
	"Untrash":     true,
	"Delete":      true,
	"BatchDelete": true,
	"BatchModify": true,
	"Send":        true,
	"Insert":      true,
	"Import":      true,
}

// gmailReadOnlyResources are the Users.<Resource> segments this codebase may
// walk into, provided the verb on the end is not in gmailWriteVerbs.
var gmailReadOnlyResources = map[string]bool{
	"Messages": true,
	"Threads":  true,
	"Labels":   true,
	"History":  true,
}

// imapWriteVerbs are go-imap client methods that mutate a mailbox.
var imapWriteVerbs = map[string]bool{
	"Store":      true,
	"Expunge":    true,
	"Append":     true,
	"Move":       true,
	"Copy":       true,
	"UIDStore":   true,
	"UIDExpunge": true,
}

// checkGmailWrite flags call if it reaches a write-capable Gmail method via
// the Users.<Resource>.<Verb> shape described in the test's doc comment.
func checkGmailWrite(t *testing.T, fset *token.FileSet, call *ast.CallExpr) {
	t.Helper()

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	verb := sel.Sel.Name
	chain := selectorChain(sel.X)
	if len(chain) < 2 {
		return
	}
	resource := chain[len(chain)-1]
	if chain[len(chain)-2] != "Users" {
		return
	}

	switch {
	case resource == "Drafts":
		// Any Drafts call, not just write verbs: a draft is a message this
		// account did not receive, sitting one Send call away from existing.
	case gmailReadOnlyResources[resource] && gmailWriteVerbs[verb]:
		// A write verb against a resource this codebase is otherwise allowed
		// to read.
	default:
		return
	}

	pos := fset.Position(call.Pos())
	t.Errorf(
		"%s:%d: call to Users.%s.%s reaches a write-capable Gmail method.\n"+
			"mail-muncher never modifies a mailbox (see CONTRIBUTING.md and README.md). "+
			"If this call is genuinely needed, that invariant has to change on purpose — the OAuth scope, "+
			"the docs, and this test's allowlist — not by accident.",
		pos.Filename, pos.Line, resource, verb,
	)
}

// checkIMAPWrites flags every call inside fn that invokes a write-capable
// IMAP method on a receiver fn itself establishes to be an *imapclient.Client
// — a parameter of that type, or a local `:= imapclient.New(...)`.
func checkIMAPWrites(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) {
	t.Helper()

	clientIdents := imapClientIdentsInFunc(fn)
	if len(clientIdents) == 0 {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || !clientIdents[recv.Name] {
			return true
		}
		verb := sel.Sel.Name
		if !imapWriteVerbs[verb] {
			return true
		}

		pos := fset.Position(call.Pos())
		t.Errorf(
			"%s:%d: %s calls %s.%s, a write-capable IMAP command.\n"+
				"mail-muncher never modifies a mailbox (see CONTRIBUTING.md and README.md). "+
				"If this call is genuinely needed, that invariant has to change on purpose, not by accident.",
			pos.Filename, pos.Line, fnLabel(fn), recv.Name, verb,
		)
		return true
	})
}

// imapClientIdentsInFunc returns the names, local to fn, that are known to
// hold an *imapclient.Client: fn's own parameters of that type, plus any
// `name := imapclient.New(...)` short variable declaration in its body.
func imapClientIdentsInFunc(fn *ast.FuncDecl) map[string]bool {
	idents := map[string]bool{}

	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			if !isImapClientPointerType(field.Type) {
				continue
			}
			for _, name := range field.Names {
				idents[name.Name] = true
			}
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			return true
		}
		for i, rhs := range assign.Rhs {
			if i >= len(assign.Lhs) {
				break
			}
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "imapclient" || sel.Sel.Name != "New" {
				continue
			}
			if lhs, ok := assign.Lhs[i].(*ast.Ident); ok {
				idents[lhs.Name] = true
			}
		}
		return true
	})

	return idents
}

// isImapClientPointerType reports whether t is exactly *imapclient.Client.
func isImapClientPointerType(t ast.Expr) bool {
	star, ok := t.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "imapclient" && sel.Sel.Name == "Client"
}

// selectorChain flattens a chain of selector expressions rooted at an
// identifier into its dotted path, outermost-root first — e.g.
// `p.svc.Users.Messages` becomes []string{"p", "svc", "Users", "Messages"}.
// A chain rooted at anything else (a call, an index expression, ...) returns
// nil: this test only needs to recognise plain field/selector chains, and
// treating anything unrecognised as "no match" is the safe direction for a
// guard that must not cry wolf.
func selectorChain(e ast.Expr) []string {
	switch v := e.(type) {
	case *ast.Ident:
		return []string{v.Name}
	case *ast.SelectorExpr:
		base := selectorChain(v.X)
		if base == nil {
			return nil
		}
		return append(base, v.Sel.Name)
	default:
		return nil
	}
}

// fnLabel names fn for an error message: "Type.Method" for a method,
// "function" for a plain function.
func fnLabel(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fmt.Sprintf("func %s", fn.Name.Name)
	}
	recvType := selectorChain(fn.Recv.List[0].Type)
	if recvType == nil {
		// Pointer receiver: unwrap the StarExpr the chain walk doesn't handle.
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if ident, ok := star.X.(*ast.Ident); ok {
				return fmt.Sprintf("(*%s).%s", ident.Name, fn.Name.Name)
			}
		}
		return fn.Name.Name
	}
	return fmt.Sprintf("%s.%s", strings.Join(recvType, "."), fn.Name.Name)
}
