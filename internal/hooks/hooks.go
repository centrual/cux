// Package hooks implements the bodies of `cux hook stop`,
// `cux hook session-start`, and `cux hook rate-limit`.
//
// These subcommands are invoked by Claude Code via entries in
// ~/.claude/settings.json. Their job is small: read the JSON Claude
// Code pipes on stdin, decide whether the event is interesting, and
// emit a signal file the cux wrapper polls for.
//
// All three are gated by the CUX_WRAPPED env var. When unset (the user
// is running `claude` directly, not under `cux`), the hook silently
// no-ops with exit 0 — so installed hooks are harmless when cux is not
// the parent process.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inulute/cux/internal/signals"
)

const (
	envWrapped    = "CUX_WRAPPED"
	envWrapperPID = "CUX_WRAPPER_PID"

	// Hook timeouts in settings.json are in seconds. Reading stdin
	// shouldn't ever take more than a few hundred ms in practice; we
	// fail fast rather than block claude on a stuck hook.
	stdinReadDeadline = 4 * time.Second
)

// stdinJSON shapes mirror what claude-revolver reads. We tolerate
// missing fields with `omitempty` defaults so a future Claude Code
// version that adds keys does not break us.

type stopHookInput struct {
	SessionID string `json:"session_id"`
}

type sessionStartHookInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd,omitempty"`
	Source    string `json:"source,omitempty"`
}

type rateLimitHookInput struct {
	Error *struct {
		Type    string `json:"type,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

// Stop is `cux hook stop`. Always emits a Stopped signal in wrapped
// mode — the wrapper interprets "received Stop" as "the turn just
// finished and the transcript is flushed; safe to act now."
func Stop(stdin io.Reader) error {
	if !isWrapped() {
		return nil
	}
	pid, err := wrapperPID()
	if err != nil {
		return err
	}
	var in stopHookInput
	_ = decode(stdin, &in) // tolerate missing/empty stdin
	return signals.Write(pid, signals.Stopped, signals.StoppedPayload{
		SessionID: in.SessionID,
		Timestamp: time.Now().UTC(),
	})
}

// SessionStart is `cux hook session-start`. We capture the session ID
// the moment a session begins so the wrapper does not have to fall
// back to mtime-scanning the transcript directory.
func SessionStart(stdin io.Reader) error {
	if !isWrapped() {
		return nil
	}
	pid, err := wrapperPID()
	if err != nil {
		return err
	}
	var in sessionStartHookInput
	_ = decode(stdin, &in)
	if in.SessionID == "" {
		// A session-start with no ID is not useful; do not write the
		// signal. The wrapper will still find the latest .jsonl as a
		// fallback, but this case is rare.
		return nil
	}
	return signals.Write(pid, signals.SessionStarted, signals.SessionStartedPayload{
		SessionID: in.SessionID,
		CWD:       in.CWD,
		Source:    in.Source,
		Timestamp: time.Now().UTC(),
	})
}

// RateLimit is `cux hook rate-limit`. Claude Code routes generic
// PostToolUseFailure events through this; we filter the body for
// rate-limit indicators before signalling. False positives here would
// trigger spurious account swaps, so the filter is deliberately
// conservative.
func RateLimit(stdin io.Reader) error {
	if !isWrapped() {
		return nil
	}
	pid, err := wrapperPID()
	if err != nil {
		return err
	}
	var in rateLimitHookInput
	if err := decode(stdin, &in); err != nil {
		return nil // malformed input is not our problem
	}
	if in.Error == nil {
		return nil
	}
	t := strings.ToLower(in.Error.Type)
	m := strings.ToLower(in.Error.Message)
	isRateLimit := strings.Contains(t, "rate_limit") ||
		strings.Contains(m, "rate limit") ||
		strings.Contains(m, "usage limit")
	if !isRateLimit {
		return nil
	}
	return signals.Write(pid, signals.RateLimited, signals.RateLimitedPayload{
		Timestamp: time.Now().UTC(),
		Message:   in.Error.Message,
	})
}

// decode reads stdin (with a deadline) and parses it as JSON. Empty
// stdin is treated as an empty object — Claude Code occasionally
// invokes hooks with no body and we should tolerate that quietly.
func decode(r io.Reader, dst interface{}) error {
	type result struct {
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(r)
		if err != nil {
			ch <- result{err: err}
			return
		}
		if len(b) == 0 {
			ch <- result{err: nil}
			return
		}
		ch <- result{err: json.Unmarshal(b, dst)}
	}()
	select {
	case res := <-ch:
		return res.err
	case <-time.After(stdinReadDeadline):
		return errors.New("hook: stdin read timeout")
	}
}

func isWrapped() bool {
	return os.Getenv(envWrapped) == "1"
}

func wrapperPID() (int, error) {
	v := os.Getenv(envWrapperPID)
	if v == "" {
		return 0, errors.New("hook: CUX_WRAPPER_PID not set")
	}
	pid, err := strconv.Atoi(v)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("hook: bad CUX_WRAPPER_PID %q", v)
	}
	return pid, nil
}
