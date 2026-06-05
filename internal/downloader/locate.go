package downloader

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// LocateAria2 returns the path to aria2c. When binary is non-empty it must
// exist on disk; otherwise aria2c is resolved on PATH.
func LocateAria2(binary string) (string, error) {
	if binary != "" {
		if _, err := os.Stat(binary); err != nil {
			return "", fmt.Errorf("aria2c not found at %q: %w", binary, err)
		}
		return binary, nil
	}
	found, err := exec.LookPath("aria2c")
	if err != nil {
		return "", fmt.Errorf("aria2c not found on PATH (set downloader.aria2_binary): %w", err)
	}
	return found, nil
}

// InstallHint returns a one-line install command for the current OS (best-effort).
func InstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install aria2"
	case "linux":
		return "sudo apt install aria2"
	case "windows":
		return "winget install aria2.aria2"
	default:
		return "install aria2 from your package manager (binary must be named aria2c)"
	}
}
