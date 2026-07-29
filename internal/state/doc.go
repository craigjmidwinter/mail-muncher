// Package state persists sync state between runs: which messages have already
// been processed, provider cursors, and the locking that keeps concurrent
// runs from stepping on each other.
package state
