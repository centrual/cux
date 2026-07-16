package registry

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/inulute/cux/internal/paths"
)

// TestReapStaleAttachSockets: only sockets belonging to dead wrappers go;
// the live wrapper's socket and anything that isn't a PID-named socket
// stay untouched.
func TestReapStaleAttachSockets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(paths.AttachDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	touch := func(name string) string {
		p := filepath.Join(paths.AttachDir(), name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	deadSock := touch(strconv.Itoa(spawnDead(t)) + ".sock")
	liveSock := touch(strconv.Itoa(os.Getpid()) + ".sock")
	junkSock := touch("not-a-pid.sock")
	other := touch("readme.txt")

	ReapStaleAttachSockets()

	if _, err := os.Stat(deadSock); !os.IsNotExist(err) {
		t.Error("dead wrapper's socket should have been reaped")
	}
	for _, p := range []string{liveSock, junkSock, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(p), err)
		}
	}
}
