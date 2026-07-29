package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
)

func main() {
	if err := Execute(context.Background()); err != nil {
		var ne *notImplementedError
		if errors.As(err, &ne) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "error:", err)

		// Commands that distinguish failure modes carry the status cron and
		// calling agents branch on: 2 for a provider/auth failure, 3 for a
		// cycle lock held elsewhere. Anything untyped — a cobra usage error,
		// say — stays a plain 1.
		var coded *pipeline.ExitCodeError
		if errors.As(err, &coded) {
			os.Exit(coded.Code)
		}
		os.Exit(1)
	}
}
