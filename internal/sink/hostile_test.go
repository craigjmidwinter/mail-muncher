package sink

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/model"
)

// The tests in this file are about the local filesystem, not about email. The
// destination tree is read — and written near — by autonomous agents, so a
// neighbouring process planting links in it is the threat being modelled, not a
// sender crafting a subject. Every case here therefore builds a destination
// alongside an "outside" directory that a leaked write would land in, and
// checks that it came through untouched.

// hostileTree is a destination directory plus somewhere for a planted symlink
// to point at.
type hostileTree struct {
	dest    string
	outside string
}

func newHostileTree(t *testing.T) hostileTree {
	t.Helper()
	root := t.TempDir()
	h := hostileTree{
		dest:    filepath.Join(root, "archive"),
		outside: filepath.Join(root, "outside"),
	}
	require.NoError(t, os.MkdirAll(h.dest, 0o700))
	require.NoError(t, os.MkdirAll(h.outside, 0o700))
	return h
}

// snapshot records everything under the outside directory: names, whether each
// is a directory, and file contents. Comparing one taken before a Store against
// one taken after catches a write that escaped the destination tree, whether it
// created a file, created a directory, or rewrote something already there.
func (h hostileTree) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(h.outside, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(h.outside, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return out
}

// bothSinks is the set every filesystem-safety property has to hold for.
func bothSinks() map[string]Sink {
	return map[string]Sink{
		"eml":      NewEML(),
		"markdown": NewMarkdown(),
	}
}

// hostileMessage carries an attachment so the markdown sink exercises its
// attachments directory too.
func hostileMessage() *model.Message {
	msg := testMessage()
	msg.Attachments = []model.Attachment{{Filename: "offer.pdf", Content: []byte("%PDF-1.4 offer")}}
	return msg
}

// TestStoreRefusesSymlinkAtFinalPath: a link sitting where a message file
// belongs is an anomaly, not a prior archive.
//
// Stat follows it. That is the whole bug: a link pointing at any existing file
// makes the old check answer "already stored", and the message is counted as
// skipped and never written — dropped silently, with the archive now pointing
// at a file the sink did not produce. Both shapes are covered: a link to a real
// file, which is the silent-drop case, and a dangling one, which the old code
// would have quietly replaced.
func TestStoreRefusesSymlinkAtFinalPath(t *testing.T) {
	for name, s := range bothSinks() {
		for _, target := range []string{"live", "dangling"} {
			t.Run(name+"/"+target, func(t *testing.T) {
				h := newHostileTree(t)
				msg := hostileMessage()
				rule := testRule(h.dest, config.FormatEML, config.FormatMarkdown)

				path := s.Path(msg, rule)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

				loot := filepath.Join(h.outside, "loot")
				if target == "live" {
					require.NoError(t, os.WriteFile(loot, []byte("someone else's file"), 0o600))
				}
				require.NoError(t, os.Symlink(loot, path))
				before := h.snapshot(t)

				got, skipped, err := s.Store(msg, rule)
				require.ErrorIs(t, err, ErrSymlink)
				assert.False(t, skipped, "a symlink must be surfaced as an error, not counted as already stored")
				assert.Equal(t, path, got, "the error still names the path it concerns")
				assert.Equal(t, before, h.snapshot(t))

				// The link itself is left exactly as found: refusing means
				// refusing, not quietly repairing the tree.
				info, err := os.Lstat(path)
				require.NoError(t, err)
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			})
		}
	}
}

// TestPlanRefusesSymlinkAtFinalPath: a dry run reports the same refusal a real
// run would, so `--dry-run` cannot say "would skip" about a message a real run
// would error on.
func TestPlanRefusesSymlinkAtFinalPath(t *testing.T) {
	for name, s := range bothSinks() {
		t.Run(name, func(t *testing.T) {
			h := newHostileTree(t)
			msg := hostileMessage()
			rule := testRule(h.dest, config.FormatEML, config.FormatMarkdown)

			path := s.Path(msg, rule)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			loot := filepath.Join(h.outside, "loot")
			require.NoError(t, os.WriteFile(loot, []byte("someone else's file"), 0o600))
			require.NoError(t, os.Symlink(loot, path))
			before := h.snapshot(t)

			_, skipped, err := s.Plan(msg, rule)
			require.ErrorIs(t, err, ErrSymlink)
			assert.False(t, skipped)
			assert.Equal(t, before, h.snapshot(t))
		})
	}
}

// TestStoreRefusesSymlinkedDirectoryComponent: the year and month directories
// are this package's own layout, so a link standing in for one is refused
// rather than descended through. MkdirAll would have followed it and filed the
// entire month somewhere else.
func TestStoreRefusesSymlinkedDirectoryComponent(t *testing.T) {
	for name, s := range bothSinks() {
		for _, component := range []string{"2026", filepath.Join("2026", "07")} {
			t.Run(name+"/"+component, func(t *testing.T) {
				h := newHostileTree(t)
				msg := hostileMessage()
				rule := testRule(h.dest, config.FormatEML, config.FormatMarkdown)

				link := filepath.Join(h.dest, component)
				require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
				require.NoError(t, os.Symlink(h.outside, link))
				before := h.snapshot(t)

				_, skipped, err := s.Store(msg, rule)
				require.ErrorIs(t, err, ErrSymlink)
				assert.False(t, skipped)
				assert.Equal(t, before, h.snapshot(t), "the month was filed outside the destination tree")

				_, skipped, err = s.Plan(msg, rule)
				require.ErrorIs(t, err, ErrSymlink, "a dry run must refuse it too")
				assert.False(t, skipped)
				assert.Equal(t, before, h.snapshot(t))
			})
		}
	}
}

// TestStoreRefusesSymlinkedDirectoryComponentEvenWhenTheTargetLooksStored:
// following a link for the existence check is its own leak, separate from
// following one for the write. With a file of the right name already sitting in
// the link's target, a Stat down that path answers "already stored" and the
// message is dropped without anything ever being written.
func TestStoreRefusesSymlinkedDirectoryComponentEvenWhenTheTargetLooksStored(t *testing.T) {
	h := newHostileTree(t)
	msg := hostileMessage()
	rule := testRule(h.dest, config.FormatEML)
	s := NewEML()

	// The name the sink is about to look for, pre-placed in the outside
	// directory the planted link points at.
	require.NoError(t, os.WriteFile(
		filepath.Join(h.outside, filepath.Base(s.Path(msg, rule))),
		[]byte("not this message"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(h.dest, "2026"), 0o700))
	require.NoError(t, os.Symlink(h.outside, filepath.Join(h.dest, "2026", "07")))
	before := h.snapshot(t)

	_, skipped, err := s.Store(msg, rule)
	require.ErrorIs(t, err, ErrSymlink)
	assert.False(t, skipped, "the message must be counted and logged, not silently dropped")
	assert.Equal(t, before, h.snapshot(t))
}

// TestMarkdownRefusesSymlinkedAttachmentsDirectory: decoded attachments are the
// documents that were private in the mailbox, and are the largest thing a sink
// writes. A link at the attachments directory is refused before any of them are
// written.
func TestMarkdownRefusesSymlinkedAttachmentsDirectory(t *testing.T) {
	h := newHostileTree(t)
	msg := hostileMessage()
	rule := testRule(h.dest, config.FormatMarkdown)
	s := NewMarkdown()

	attDir := s.AttachmentsDir(msg, rule)
	require.NoError(t, os.MkdirAll(filepath.Dir(attDir), 0o700))
	require.NoError(t, os.Symlink(h.outside, attDir))
	before := h.snapshot(t)

	path, skipped, err := s.Store(msg, rule)
	require.ErrorIs(t, err, ErrSymlink)
	assert.False(t, skipped)
	assert.Equal(t, before, h.snapshot(t), "attachments were written outside the destination tree")
	assert.NoFileExists(t, path, "the document must not land without its attachments")
}

// TestStoreAllowsSymlinkedDest: the exemption, pinned deliberately. The rule's
// own dest: is the operator's choice, and pointing it at another volume or at a
// dotfile-managed directory is ordinary — only the layout this package builds
// beneath it is policed.
func TestStoreAllowsSymlinkedDest(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-archive")
	require.NoError(t, os.MkdirAll(real, 0o700))
	link := filepath.Join(root, "archive")
	require.NoError(t, os.Symlink(real, link))

	msg := testMessage()
	path, skipped, err := NewEML().Store(msg, testRule(link))
	require.NoError(t, err)
	assert.False(t, skipped)

	data, err := os.ReadFile(filepath.Join(real, "2026", "07", Basename(msg)+EMLExt))
	require.NoError(t, err)
	assert.Equal(t, msg.Raw, data)
	assert.FileExists(t, path)
}

// TestStoreDoesNotClobberAFileThatAppearsAfterTheCheck closes the check/rename
// window. The old code decided "not there" with a Stat and then acted on that
// decision with a rename, which replaces whatever it finds — so a file created
// in between was silently overwritten. Here a competing writer lands in exactly
// that window, and its file has to survive.
func TestStoreDoesNotClobberAFileThatAppearsAfterTheCheck(t *testing.T) {
	for name, s := range bothSinks() {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			msg := testMessage()
			rule := testRule(dest, config.FormatEML, config.FormatMarkdown)
			path := s.Path(msg, rule)

			sentinel := []byte("written by someone else")
			restore := writeAndSync
			var once sync.Once
			writeAndSync = func(f *os.File, data []byte) error {
				// Fires while the temp file is being written: after this
				// call decided the name was free, before it claims it.
				once.Do(func() {
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					require.NoError(t, os.WriteFile(path, sentinel, 0o600))
				})
				return restore(f, data)
			}
			t.Cleanup(func() { writeAndSync = restore })

			got, skipped, err := s.Store(msg, rule)
			require.NoError(t, err)
			assert.True(t, skipped, "losing the race is a skip, the same as finding the file up front")
			assert.Equal(t, path, got)

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, sentinel, data, "the loser of the race overwrote the winner")

			entries, err := os.ReadDir(filepath.Dir(path))
			require.NoError(t, err)
			assert.Len(t, entries, 1, "no temp file may survive a lost race")
		})
	}
}

// TestConcurrentStoreHasExactlyOneWinner: overlapping cron invocations are the
// motivating case for the whole idempotency scheme, and they really do run at
// the same time. Exactly one writer may report having created the file; every
// other one reports a skip, none report an error, and the file that ends up on
// disk is complete.
func TestConcurrentStoreHasExactlyOneWinner(t *testing.T) {
	const writers = 8

	t.Run("eml", func(t *testing.T) {
		dest := t.TempDir()
		rule := testRule(dest, config.FormatEML)
		created, err := raceStore(t, NewEML(), rule, writers, testMessage)
		require.NoError(t, err)
		assert.Equal(t, 1, created, "exactly one writer may claim the name")

		monthDir := filepath.Join(dest, "2026", "07")
		entries, readErr := os.ReadDir(monthDir)
		require.NoError(t, readErr)
		assert.Len(t, entries, 1, "one message, one file, no temp droppings")

		data, readErr := os.ReadFile(filepath.Join(monthDir, Basename(testMessage())+EMLExt))
		require.NoError(t, readErr)
		assert.Equal(t, testMessage().Raw, data, "the surviving file must be whole")
	})

	t.Run("markdown", func(t *testing.T) {
		dest := t.TempDir()
		rule := testRule(dest, config.FormatMarkdown)
		s := NewMarkdown()
		created, err := raceStore(t, s, rule, writers, hostileMessage)
		require.NoError(t, err)
		assert.Equal(t, 1, created)

		msg := hostileMessage()
		data, readErr := os.ReadFile(s.Path(msg, rule))
		require.NoError(t, readErr)
		assert.Contains(t, string(data), "- [offer.pdf](")

		// The losers must not roll back the attachments the winner's
		// document links to.
		att, readErr := os.ReadFile(filepath.Join(s.AttachmentsDir(msg, rule), "offer.pdf"))
		require.NoError(t, readErr)
		assert.Equal(t, []byte("%PDF-1.4 offer"), att)

		entries, readErr := os.ReadDir(filepath.Join(dest, "2026", "07"))
		require.NoError(t, readErr)
		assert.Len(t, entries, 2, "the .md and its attachments directory, and nothing else")
	})
}

// raceStore runs n concurrent Stores of the same message and reports how many
// of them claimed to have created the file, plus the first error any of them
// returned.
func raceStore(t *testing.T, s Sink, rule *config.Rule, n int, newMsg func() *model.Message) (int, error) {
	t.Helper()

	skips := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, skipped, err := s.Store(newMsg(), rule)
			skips[i], errs[i] = skipped, err
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			return created, errs[i]
		}
		if !skips[i] {
			created++
		}
	}
	return created, nil
}
