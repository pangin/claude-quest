package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJSONL creates a JSONL file with the given lines and forces its mtime
// to the supplied value via os.Chtimes (newer than the previous mtime by the
// caller's choice).
func writeJSONL(t *testing.T, dir, name string, lines []string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

func newWatcherFor(dir string) *Watcher {
	w := NewWatcher()
	w.ProjectDir = dir
	return w
}

// uuidName returns a 36-character UUID-shaped filename stem with .jsonl suffix.
func uuidName(n int) string {
	return fmt.Sprintf("%08d-1111-2222-3333-%012d.jsonl", n, n)
}

func TestCheckForNewerFile_ActiveFileNotSwitchedAway(t *testing.T) {
	dir := t.TempDir()

	now := time.Now()
	active := writeJSONL(t, dir, uuidName(1), []string{`{"uuid":"u-active-1","type":"user"}`}, now)
	// A second file that's slightly OLDER than the active one — must not
	// "win" the comparison even though both exist.
	_ = writeJSONL(t, dir, uuidName(2), []string{`{"uuid":"u-other-1","type":"user"}`}, now.Add(-2*time.Second))

	w := newWatcherFor(dir)
	w.FilePath = active
	w.lastModTime = now
	if w.checkForNewerFile() {
		t.Fatalf("checkForNewerFile switched even though active file is newest")
	}
	if filepath.Base(w.FilePath) != filepath.Base(active) {
		t.Fatalf("FilePath changed unexpectedly: %s", w.FilePath)
	}
}

func TestCheckForNewerFile_StaleBaselineDoesNotTriggerSwitch(t *testing.T) {
	// Regression: previously, lastModTime froze at startup and any other
	// file whose mtime exceeded that frozen value would cause a switch —
	// even if the active file was *currently* even newer.
	dir := t.TempDir()

	now := time.Now()
	active := writeJSONL(t, dir, uuidName(1), []string{`{"uuid":"u-active-1"}`}, now)
	_ = writeJSONL(t, dir, uuidName(2), []string{`{"uuid":"u-other-1"}`}, now.Add(-1*time.Second))

	w := newWatcherFor(dir)
	w.FilePath = active
	// Pretend baseline is way in the past (simulating tailFile having not
	// refreshed it yet). The fix uses a fresh stat of FilePath instead.
	w.lastModTime = now.Add(-1 * time.Hour)
	if w.checkForNewerFile() {
		t.Fatalf("switched on stale baseline; fix should compare against fresh stat")
	}
}

func TestCheckForNewerFile_TouchedHistoricalFileIgnored(t *testing.T) {
	// Claude Code occasionally touches old session files. Even if such a
	// file becomes "newest" by mtime, we should not switch to it because
	// it isn't a live session — its mtime is older than activeSessionWindow.
	dir := t.TempDir()

	stale := time.Now().Add(-(activeSessionWindow + time.Minute))
	active := writeJSONL(t, dir, uuidName(1), []string{`{"uuid":"u-1"}`}, stale.Add(-time.Hour))
	// "Touched" historical file: UUID-shaped name, newer than active, but
	// mtime itself is well outside the active-session window.
	_ = writeJSONL(t, dir, uuidName(2), []string{`{"uuid":"u-2"}`}, stale)

	w := newWatcherFor(dir)
	w.FilePath = active
	w.lastModTime = stale.Add(-time.Hour)
	if w.checkForNewerFile() {
		t.Fatalf("switched to a historical file whose mtime was outside the active-session window")
	}
}

func TestCheckForNewerFile_NonUUIDFilenameIgnored(t *testing.T) {
	dir := t.TempDir()

	now := time.Now()
	active := writeJSONL(t, dir, uuidName(1), []string{`{"uuid":"u-1"}`}, now.Add(-1*time.Second))
	// A non-UUID-shaped filename — must not be considered a live session.
	_ = writeJSONL(t, dir, "scratch.jsonl", []string{`{"uuid":"u-2"}`}, now)

	w := newWatcherFor(dir)
	w.FilePath = active
	w.lastModTime = now.Add(-1 * time.Second)
	if w.checkForNewerFile() {
		t.Fatalf("switched to a non-UUID-shaped file")
	}
}

func TestCheckForNewerFile_RealSessionSwitchSeeksToEOF(t *testing.T) {
	dir := t.TempDir()

	now := time.Now()
	active := writeJSONL(t, dir, uuidName(1), []string{`{"uuid":"u-old-1"}`}, now.Add(-5*time.Second))

	// New live session: UUID-shaped, mtime is now, contains ten lines of
	// historical content that MUST NOT be replayed after the switch.
	historicalLines := make([]string, 10)
	for i := range historicalLines {
		historicalLines[i] = fmt.Sprintf(`{"uuid":"u-new-%d","type":"user"}`, i)
	}
	newSession := writeJSONL(t, dir, uuidName(2), historicalLines, now)

	w := newWatcherFor(dir)
	w.FilePath = active
	w.lastModTime = now.Add(-5 * time.Second)
	w.lastPos = 999 // a meaningless previous position

	if !w.checkForNewerFile() {
		t.Fatalf("expected switch to genuinely newer session file")
	}
	if w.FilePath != newSession {
		t.Fatalf("FilePath = %s; want %s", w.FilePath, newSession)
	}
	info, err := os.Stat(newSession)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if w.lastPos != info.Size() {
		t.Fatalf("lastPos = %d; want EOF (%d) so historical lines are skipped",
			w.lastPos, info.Size())
	}
}
