package imap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultPasswordTimeout bounds one `password_cmd` run. It is generous because
// the command may legitimately block on a pinentry prompt or a hardware token,
// and short because a daemon that waits forever on a prompt nobody will answer
// looks exactly like a daemon that has hung.
const DefaultPasswordTimeout = 2 * time.Minute

// maxPasswordBytes is the most stdout a password_cmd may produce. Anything
// larger is a command that printed a file rather than a secret.
const maxPasswordBytes = 4096

// maxStderrBytes bounds how much of a failed command's stderr is quoted back.
const maxStderrBytes = 512

// ErrNoPassword reports that an account has no usable `imap.password_cmd`. It
// is returned before anything is dialled, so the message the user sees names
// the config key rather than a connection failure.
var ErrNoPassword = errors.New("imap: no password_cmd configured")

// runPasswordCmd runs the account's `password_cmd` and returns its stdout as
// the password.
//
// The command runs under `/bin/sh -c`, so pipelines and shell quoting work the
// way they do in the config file the user wrote it in. Trailing newlines are
// stripped, because every password manager prints one and none of them mean it;
// nothing else is trimmed, since leading or interior whitespace could be part of
// a real secret.
//
// The secret never appears in an error, a log line, or the process table of
// this process. It does appear in the arguments of the command the user chose,
// which is theirs to decide.
func runPasswordCmd(ctx context.Context, command string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", ErrNoPassword
	}
	if timeout <= 0 {
		timeout = DefaultPasswordTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// No stdin: a command that tries to read from the terminal must fail
	// rather than block a daemon on a prompt nobody is watching.
	cmd.Stdin = nil

	err := cmd.Run()
	switch {
	case err != nil && ctx.Err() != nil:
		return "", fmt.Errorf("imap: password_cmd %q did not finish within %s: %w%s",
			command, timeout, ctx.Err(), quoteStderr(stderr.Bytes()))
	case err != nil:
		return "", fmt.Errorf("imap: password_cmd %q failed: %w%s", command, err, quoteStderr(stderr.Bytes()))
	}

	if stdout.Len() > maxPasswordBytes {
		return "", fmt.Errorf("imap: password_cmd %q wrote %d bytes to stdout; a password should be one line (limit %d bytes)",
			command, stdout.Len(), maxPasswordBytes)
	}

	password := strings.TrimRight(stdout.String(), "\r\n")
	if password == "" {
		return "", fmt.Errorf("imap: password_cmd %q produced no output on stdout; it must print the password there%s",
			command, quoteStderr(stderr.Bytes()))
	}
	return password, nil
}

// quoteStderr renders a failed command's stderr for an error message, bounded
// and collapsed onto one line. It returns "" when there was nothing on stderr,
// so the caller can concatenate it unconditionally.
func quoteStderr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if len(s) > maxStderrBytes {
		s = s[:maxStderrBytes] + "…"
	}
	return ": " + strings.Join(strings.Fields(s), " ")
}
