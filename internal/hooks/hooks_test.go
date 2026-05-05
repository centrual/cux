package hooks

import (
	"bytes"
	"strings"
	"testing"

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

func hookAccountUsage(five, seven float64) usage.AccountUsage {
	w5 := usage.Window{Utilization: five}
	w7 := usage.Window{Utilization: seven}
	return usage.AccountUsage{FiveHour: &w5, SevenDay: &w7}
}
