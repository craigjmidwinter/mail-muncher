package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

func main() {
	err := Execute(context.Background())
	if err == nil {
		return
	}
	if msg := fatalMessage(err); msg != "" {
		// Best-effort: os.Exit happens right after regardless of whether this
		// write lands.
		_, _ = fmt.Fprint(os.Stderr, msg)
	}
	os.Exit(exitStatus(err))
}

// fatalMessage renders a failed command the way the user actually reads it.
//
// Setup guidance is printed verbatim, with no "error:" prefix and no
// reformatting. That output is a user interface — it is the first thing an
// unconfigured machine says, and the thing an installing agent parses off
// stderr — so wrapping it in an error decoration would bury the one part that
// matters. Everything else keeps the terse form it always had.
func fatalMessage(err error) string {
	var reported *reportedError
	if errors.As(err, &reported) {
		// Already written to stderr by the command, at the moment it happened.
		return ""
	}

	var se *setupError
	if errors.As(err, &se) {
		return se.Text + "\n"
	}

	var ne *notImplementedError
	if errors.As(err, &ne) {
		return err.Error() + "\n"
	}

	return "error: " + err.Error() + "\n"
}

// exitStatus is the process status an error deserves.
//
// Commands that distinguish failure modes carry the status cron and calling
// agents branch on: 2 for a provider/auth failure, 3 for a cycle lock held
// elsewhere. Anything untyped — a cobra usage error, say — stays a plain 1.
func exitStatus(err error) int {
	var coded *pipeline.ExitCodeError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return 1
}
