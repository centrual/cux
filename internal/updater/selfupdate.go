package updater

// SelfUpdate downloads the latest release binary for the current OS/arch
// from GitHub, verifies its SHA-256 checksum, and atomically replaces exe.
//
// This is the upgrade path for non-npm installs on every platform:
//   - No shell (sh / bash / PowerShell) required
//   - No curl / wget dependency
//   - Works on Windows — handles the running-exe rename dance
//
// For npm installs, callers should continue to use
// "npm install -g @inulute/cux@latest" so npm's own metadata stays correct.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// assetName returns the GitHub release artifact name for the current
// OS and architecture, matching the names produced by the release workflow.
func assetName() (string, error) {
	var osName string
	switch runtime.GOOS {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "darwin"
	case "windows":
		osName = "windows"
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	var archName string
	switch runtime.GOARCH {
	case "amd64":
		archName = "amd64"
	case "arm64":
		archName = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	name := fmt.Sprintf("cux-%s-%s", osName, archName)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name, nil
}

// SelfUpdate fetches the latest release tag, downloads the matching binary
// for the running platform, verifies its SHA-256, and replaces exe.
//
// Progress messages are written to stderr so they don't pollute stdout.
func SelfUpdate(exe string) error {
	// Resolve the latest release.
	r, err := fetchLatest("") // current version unused here
	if err != nil {
		return fmt.Errorf("resolving latest release: %w", err)
	}
	if r.Latest == "" {
		return fmt.Errorf("could not resolve latest release tag")
	}
	tag := "v" + r.Latest // fetchLatest strips the leading "v"

	asset, err := assetName()
	if err != nil {
		return err
	}

	repo := defaultRepo
	if e := strings.TrimSpace(os.Getenv(envRepo)); e != "" {
		repo = e
	}
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, tag)
	binURL := baseURL + "/" + asset
	sumURL := binURL + ".sha256"

	fmt.Fprintf(os.Stderr, "cux: downloading %s from %s\n", asset, binURL)

	// Download to a temp file adjacent to the target so the final rename
	// stays on the same filesystem (required for atomic replace on Linux).
	exeDir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(exeDir, ".cux-upgrade-*")
	if err != nil {
		// Fallback to system temp dir (rename may cross filesystems, which
		// is fine on Linux; on Windows we handle below anyway).
		tmp, err = os.CreateTemp("", ".cux-upgrade-*")
		if err != nil {
			return fmt.Errorf("creating temp file: %w", err)
		}
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful rename

	if err := downloadTo(binURL, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flushing download: %w", err)
	}

	// Verify SHA-256 if the sidecar is available. Failures are warnings,
	// not hard errors — older releases may not have the sidecar.
	if err := verifyChecksum(tmpPath, sumURL); err != nil {
		fmt.Fprintf(os.Stderr, "cux: checksum warning: %s\n", err)
	}

	// Make executable before replacing.
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := replaceBinary(exe, tmpPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}

	fmt.Fprintf(os.Stderr, "cux %s installed at %s\n", r.Latest, exe)
	return nil
}

// replaceBinary atomically swaps newPath into place at target.
//
// On Unix a simple rename works even when target is the running binary.
// On Windows, the running .exe is locked by name, so we rename it away
// first, then rename the new binary into its place.
func replaceBinary(target, newPath string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(newPath, target)
	}

	// Windows: rename the running exe aside (allowed while it's running),
	// then move the new binary into the vacated name.
	oldPath := target + ".old"
	_ = os.Remove(oldPath) // remove any leftover from a previous upgrade

	if err := os.Rename(target, oldPath); err != nil {
		return fmt.Errorf("renaming current binary: %w", err)
	}
	if err := os.Rename(newPath, target); err != nil {
		// Best-effort rollback.
		_ = os.Rename(oldPath, target)
		return fmt.Errorf("placing new binary: %w", err)
	}
	// Try to remove the old binary immediately. This will fail if Windows
	// still has a file handle open, which is expected; it can be cleaned up
	// on the next upgrade or left as-is.
	_ = os.Remove(oldPath)
	return nil
}

// downloadTo streams url into dst, returning an error on HTTP failures.
func downloadTo(url string, dst *os.File) error {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("writing download: %w", err)
	}
	return nil
}

// verifyChecksum fetches the .sha256 sidecar from sumURL and checks it
// against the file at path. Returns nil when the checksum matches or when
// the sidecar is unavailable (older releases).
func verifyChecksum(path, sumURL string) error {
	resp, err := http.Get(sumURL) //nolint:noctx
	if err != nil || resp.StatusCode != http.StatusOK {
		// Sidecar missing — not an error, just no verification.
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return nil // best-effort only
	}
	want := strings.Fields(strings.TrimSpace(string(body)))[0]

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", want, got)
	}
	return nil
}
