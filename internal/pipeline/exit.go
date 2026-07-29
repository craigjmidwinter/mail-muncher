package pipeline

import (
	"errors"
	"fmt"

	"github.com/craigmidwinter/mail-muncher/internal/config"
	"github.com/craigmidwinter/mail-muncher/internal/filter"
	"github.com/craigmidwinter/mail-muncher/internal/state"
)

// init installs the filter package's match-tree compiler as config's
// validation hook.
//
// Without it, config.Validate cannot tell a malformed match tree (unknown
// predicate, two keys in one node, bad regex or duration) from a valid one, and
// the problem would only surface later as a NewRunner failure. Installing it
// here — rather than only from cmd/ — makes match-tree validation a property of
// using the pipeline, so every consumer gets it: the CLI, the daemon, the MCP
// server, and any test that calls config.LoadAndValidate.
//
// cmd/mail-muncher/validate.go installs the same hook for the `validate`
// command, which does not import this package. Setting it twice is harmless:
// both assign the identical function.
func init() {
	config.MatchValidator = func(r *config.Rule) error {
		_, err := filter.Compile(&r.Match)
		return err
	}
}

// Process exit statuses. Cron jobs and calling agents branch on these, so they
// are part of mail-muncher's published contract.
const (
	// ExitOK: the cycle completed. Message-level failures (a message that
	// would not parse, a sink that failed on one message) are reported in the
	// manifest and counted in the summary, but they do not make the run itself
	// a failure — the next run retries them.
	ExitOK = 0
	// ExitConfig: the config could not be loaded, did not validate, or the
	// cycle failed for a reason that is neither the provider nor the lock.
	ExitConfig = 1
	// ExitProvider: a provider or auth failure, e.g. a token that expired. The
	// error text names the account and the command that fixes it.
	ExitProvider = 2
	// ExitLocked: another mail-muncher instance holds the cycle lock. This is
	// "not now" rather than "broken" — a cron run that collides with a daemon
	// tick exits this way, and simply running again later is the right
	// response.
	ExitLocked = 3
)

// ExitCodeError carries a process exit status alongside the error that caused
// it, so a command can choose a status without calling os.Exit from deep
// inside its RunE (which would be untestable and would bypass cobra).
//
// main() unwraps it with errors.As and exits with Code. Anything untyped —
// a cobra usage error, say — stays a plain exit 1, so cobra's own error path
// is unaffected.
//
// Build one with Exit, which picks the status from the error itself.
type ExitCodeError struct {
	// Code is the process exit status; one of the Exit* constants.
	Code int
	// Err is the underlying cause, returned by Unwrap.
	Err error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }

// ExitCode grades an error into the process status it deserves:
//
//	nil                       -> ExitOK
//	wraps state.ErrLocked     -> ExitLocked
//	wraps *ProviderError      -> ExitProvider
//	anything else             -> ExitConfig
//
// It is the single place that mapping lives; `run`, `daemon`, and the MCP
// server all defer to it so their statuses cannot drift apart.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, state.ErrLocked):
		return ExitLocked
	case IsProviderError(err):
		return ExitProvider
	default:
		return ExitConfig
	}
}

// Exit wraps a cycle error in an ExitCodeError carrying the status ExitCode
// picks for it. A nil error stays nil, so a command can `return pipeline.Exit(
// cycleErr)` unconditionally.
func Exit(err error) error {
	if err == nil {
		return nil
	}
	return &ExitCodeError{Code: ExitCode(err), Err: err}
}
