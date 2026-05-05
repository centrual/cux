package wrapper

import (
	"strings"
	"testing"

	"github.com/inulute/cux/internal/config"
	"github.com/inulute/cux/internal/history"
	"github.com/inulute/cux/internal/store"
	"github.com/inulute/cux/internal/usage"
)

func TestResolveTargetDoesNotRotateToWeeklyFullFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	state := &store.State{
		ActiveSlot: 1,
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
		"a@x.test": accountUsage(94, 67),
		"b@x.test": accountUsage(0, 100),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	_, err := resolveTarget("", history.TriggerManual, &cfg)
	if err == nil {
		t.Fatal("resolveTarget should refuse to rotate when every alternate account is exhausted")
	}
	if !strings.Contains(err.Error(), "no usable accounts") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRotateFallbackAllowsMissingUsage(t *testing.T) {
	state := &store.State{
		ActiveSlot: 1,
		Sequence:   []int{1, 2},
		Accounts: map[int]store.Account{
			1: {Slot: 1, Email: "a@x.test"},
			2: {Slot: 2, Email: "b@x.test"},
		},
	}
	cfg := config.Defaults()
	got, err := rotateFallback(state, usage.Cache{}, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2" {
		t.Fatalf("rotateFallback = %q, want slot 2", got)
	}
}

func accountUsage(five, seven float64) usage.AccountUsage {
	w5 := usage.Window{Utilization: five}
	w7 := usage.Window{Utilization: seven}
	return usage.AccountUsage{FiveHour: &w5, SevenDay: &w7}
}
