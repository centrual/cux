// Package session captures the running Claude Code session ID so the
// wrapper can call `claude --resume <id>` after an account swap. The
// transcript file at ~/.claude/projects/<encoded-cwd>/<id>.jsonl carries
// the full conversation, so a swap+resume preserves context fully.
package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoSession is returned when no active session can be identified
// (likely a brand-new conversation that has not produced a transcript
// yet). Callers should treat this as "relaunch fresh" rather than fatal.
var ErrNoSession = errors.New("session: no current session id")

// CurrentID returns the best guess at the running session's ID.
//
// Resolution order:
//
//  1. $CLAUDE_SESSION_ID, when Claude Code exposes it to the slash-command
//     subprocess. Cheap, exact, and stable across invocations.
//  2. Newest *.jsonl file in the project's transcript directory, by
//     modification time. Mirrors how Claude Code itself surfaces the
//     "last session" in `claude --resume` interactive mode.
//
// cwd is the working directory the running claude was launched in.
func CurrentID(cwd string) (string, error) {
	if id := strings.TrimSpace(os.Getenv("CLAUDE_SESSION_ID")); id != "" {
		return id, nil
	}

	dir := transcriptDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ErrNoSession
	}
	var (
		newest    string
		newestMod int64
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if mod := fi.ModTime().UnixNano(); mod > newestMod {
			newestMod = mod
			newest = name
		}
	}
	if newest == "" {
		return "", ErrNoSession
	}
	// Strip ".jsonl" — the session id is just the basename.
	return strings.TrimSuffix(newest, ".jsonl"), nil
}

// transcriptDir resolves the per-project transcript directory using the
// same encoding Claude Code uses (path with separators replaced by '-').
// Kept private here rather than in paths because it depends on cwd, which
// the rest of paths.go does not.
func transcriptDir(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	encoded := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	if !strings.HasPrefix(encoded, "-") {
		encoded = "-" + encoded
	}
	return filepath.Join(home, ".claude", "projects", encoded)
}
