package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var distroPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// WSLArguments uses an explicitly selected distribution and Unix socket. It
// neither changes the WSL default nor falls back to a different Docker engine.
func WSLArguments(distro string, args []string) ([]string, error) {
	if !distroPattern.MatchString(distro) {
		return nil, code("WSL_DISTRIBUTION_INVALID")
	}
	prefix := []string{"--distribution", distro, "--exec", "env", "-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "docker", "--host", "unix:///var/run/docker.sock"}
	return append(prefix, args...), nil
}
func NewWSL(distro string) (*Engine, error) {
	if _, err := WSLArguments(distro, nil); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		return nil, code("WSL_PLATFORM_UNSUPPORTED")
	}
	path, err := exec.LookPath("wsl.exe")
	if err != nil {
		return nil, code("WSL_NOT_FOUND")
	}
	return &Engine{executable: path, wslDistro: distro}, nil
}

// HostPath translates only locally generated paths, with wslpath as a direct
// process argument. User text is never inserted into a Linux shell command.
func (e *Engine) HostPath(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", code("STATE_PATH_INVALID")
	}
	if e.wslDistro == "" {
		return abs, nil
	}
	b := &boundedBuffer{limit: 4096}
	if err := execute(ctx, e.executable, []string{"--distribution", e.wslDistro, "--exec", "wslpath", "-a", "-u", abs}, nil, b); err != nil {
		return "", code("WSL_PATH_TRANSLATION_FAILED")
	}
	translated := strings.TrimSpace(b.buffer.String())
	if !strings.HasPrefix(translated, "/") || strings.ContainsAny(translated, "\r\n\x00") {
		return "", code("WSL_PATH_TRANSLATION_FAILED")
	}
	return translated, nil
}
