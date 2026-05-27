package hooks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inulute/cux/internal/signals"
	"github.com/inulute/cux/internal/store"
	"github.com/inulute/cux/internal/usage"
)

func TestRenderPromptSupportIncludesURL(t *testing.T) {
	out := renderPromptSupport()
	if !strings.Contains(out, "https://support.inulute.com") {
		t.Fatalf("support output missing URL: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("prompt support output contained ANSI escape bytes: %q", out)
	}
}

func TestRenderPromptUsageReportsAllExhaustedAtEffectiveCaps(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CUX_CONFIG_FILE", t.TempDir()+"/config.json")

	state := &store.State{
		ActiveSlot: 2,
		Sequence:   []int{1, 2},
		Accounts: map[int]store.Account{
			1: {Slot: 1, Email: "a@x.test"},
			2: {Slot: 2, Email: "b@x.test"},
		},
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := usage.SaveCache(usage.Cache{
		"a@x.test": hookAccountUsage(94, 67),
		"b@x.test": hookAccountUsage(0, 100),
	}); err != nil {
		t.Fatal(err)
	}

	out, err := renderPromptUsage(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "STATUS : ALL MANAGED ACCOUNTS EXHAUSTED") {
		t.Fatalf("status did not report exhaustion:\n%s", out)
	}
	if strings.Contains(out, "NEXT USABLE") {
		t.Fatalf("status should not advertise a next usable account:\n%s", out)
	}
	if !strings.Contains(out, "a@x.test") || !strings.Contains(out, "FULL") {
		t.Fatalf("status should mark the threshold-exhausted account full:\n%s", out)
	}
}

func TestUserPromptSubmitBareSwitchBlocksWhenAllAccountsExhausted(t *testing.T) {
	t.Setenv("CUX_WRAPPED", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CUX_CONFIG_FILE", t.TempDir()+"/config.json")

	state := &store.State{
		ActiveSlot: 2,
		Sequence:   []int{1, 2},
		Accounts: map[int]store.Account{
			1: {Slot: 1, Email: "a@x.test"},
			2: {Slot: 2, Email: "b@x.test"},
		},
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := usage.SaveCache(usage.Cache{
		"a@x.test": hookAccountUsage(94, 67),
		"b@x.test": hookAccountUsage(0, 100),
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := UserPromptSubmit(strings.NewReader(`{"prompt":"/switch"}`), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"decision":"block"`) {
		t.Fatalf("hook should block /switch, got: %s", got)
	}
	if strings.Contains(got, "switching accounts") {
		t.Fatalf("hook should not request a switch, got: %s", got)
	}
	if !strings.Contains(got, "STATUS : ALL MANAGED ACCOUNTS EXHAUSTED") {
		t.Fatalf("hook should return exhausted status, got: %s", got)
	}
	if strings.Contains(got, "CUX_WRAPPER_PID") {
		t.Fatalf("hook should not reach switch signaling path, got: %s", got)
	}
}

// TestHandleAutoSwitchPrompt_HardBlock_Threshold100 verifies that when the
// active account is at 100% 5h utilization AND thresholds are set to 100
// (default/"reactive-only"), the prompt-submit hook still triggers a switch
// to the available second account.
//
// This is the "session limit" regression: Claude Code's session-limit UI
// blocks before any tool use, so PostToolUseFailure never fires. The
// prompt-submit hook must catch the hard-blocked case itself.
func TestHandleAutoSwitchPrompt_HardBlock_Threshold100(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CUX_WRAPPED", "1")
	// Redirect HOME so claudecfg reads our fake .claude.json, not the real one.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("CUX_CONFIG_FILE", filepath.Join(tmp, "config.json"))

	// Write a minimal fake Claude config so CurrentLiveEmail() returns
	// the blocked account's email.
	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	claudeJSON := `{"oauthAccount":{"emailAddress":"blocked@x.test","accountUuid":"u1"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(claudeJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// Use current process PID as the fake wrapper PID so the signal
	// directory path is deterministic. Pre-create the dir so Write succeeds.
	// RuntimeDir() = $XDG_DATA_HOME/cux/runtime, so signals live under that.
	pid := os.Getpid()
	t.Setenv("CUX_WRAPPER_PID", fmt.Sprintf("%d", pid))
	sigDir := filepath.Join(tmp, "cux", "runtime", "signals")
	if err := os.MkdirAll(sigDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Two accounts: slot 1 active and hard-blocked at 100% 5h, slot 2 free.
	state := &store.State{
		ActiveSlot: 1,
		Sequence:   []int{1, 2},
		Accounts: map[int]store.Account{
			1: {Slot: 1, Email: "blocked@x.test"},
			2: {Slot: 2, Email: "free@x.test"},
		},
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := usage.SaveCache(usage.Cache{
		"blocked@x.test": hookAccountUsage(100, 31), // 5h at hard limit
		"free@x.test":    hookAccountUsage(0, 50),   // plenty of room
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := UserPromptSubmit(strings.NewReader(`{"prompt":"do something"}`), &out); err != nil {
		t.Fatalf("UserPromptSubmit returned error: %v", err)
	}
	got := out.String()

	// Hook must block the prompt.
	if !strings.Contains(got, `"decision":"block"`) {
		t.Fatalf("hook should block the prompt, got: %s", got)
	}
	// Hook must announce a switch, not an exhaustion warning.
	if !strings.Contains(got, "switching accounts") {
		t.Fatalf("hook should announce switching accounts, got: %s", got)
	}
	if strings.Contains(got, "all managed accounts are at or above") {
		t.Fatalf("hook should not report exhaustion when account 2 is free, got: %s", got)
	}

	// The SwitchRequested signal file must exist — wrapper picks it up to do the swap.
	sigFile := filepath.Join(sigDir, fmt.Sprintf("%d-%s", pid, signals.SwitchRequested))
	if _, err := os.Stat(sigFile); err != nil {
		t.Fatalf("SwitchRequested signal file not written: %v (hook output: %s)", err, got)
	}
}

func hookAccountUsage(five, seven float64) usage.AccountUsage {
	w5 := usage.Window{Utilization: five}
	w7 := usage.Window{Utilization: seven}
	return usage.AccountUsage{FiveHour: &w5, SevenDay: &w7}
}
