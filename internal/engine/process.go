package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Engine struct {
	executable string
	wslDistro  string
	localHost  string
}
type Daemon struct {
	ID      string
	Version string
}

func New() (*Engine, error) {
	if runtime.GOOS == "windows" {
		return nil, code("WSL_DISTRIBUTION_REQUIRED")
	}
	if runtime.GOOS != "linux" {
		return nil, code("DOCKER_PLATFORM_UNSUPPORTED")
	}
	p, err := exec.LookPath("docker")
	if err != nil {
		return nil, code("DOCKER_NOT_FOUND")
	}
	return &Engine{executable: p, localHost: "unix:///var/run/docker.sock"}, nil
}

// Execute never uses a shell and never forwards raw Docker diagnostics. Its
// arguments are constructed by reviewed internal operations, never user JSON.
func (e *Engine) Execute(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if e.wslDistro != "" {
		var err error
		args, err = WSLArguments(e.wslDistro, args)
		if err != nil {
			return err
		}
	}
	if e.localHost != "" {
		args = append([]string{"--host", e.localHost}, args...)
	}
	return execute(ctx, e.executable, args, input, output)
}

func execute(ctx context.Context, executable string, args []string, input io.Reader, output io.Writer) error {
	if ctx.Err() != nil {
		return code("CANCELED")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 5 * time.Second
	// Compose interpolation inputs are suppressed. Authentication variables are
	// not inherited; the adapter sends resolved values only through request stdin.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		u := strings.ToUpper(name)
		if u == "PATH" || u == "PATHEXT" || u == "SYSTEMROOT" || u == "WINDIR" || u == "HOME" || u == "USERPROFILE" || u == "TMP" || u == "TEMP" || u == "SSL_CERT_FILE" || u == "SSL_CERT_DIR" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return code("DOCKER_TIMEOUT")
		}
		return code("CANCELED")
	}
	if err != nil {
		return code("DOCKER_COMMAND_FAILED")
	}
	return nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.overflow = true
		return 0, code("DOCKER_OUTPUT_TOO_LARGE")
	}
	return b.buffer.Write(p)
}
func (e *Engine) Bytes(ctx context.Context, args []string, input io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, code("DOCKER_OUTPUT_LIMIT_INVALID")
	}
	b := &boundedBuffer{limit: limit}
	err := e.Execute(ctx, args, input, b)
	if b.overflow {
		return nil, code("DOCKER_OUTPUT_TOO_LARGE")
	}
	if err != nil {
		return nil, err
	}
	return b.buffer.Bytes(), nil
}
func ParseDaemonInfo(raw []byte) (Daemon, error) {
	var d struct {
		ID            string
		OSType        string
		Architecture  string
		ServerVersion string
	}
	if json.Unmarshal(raw, &d) != nil || strings.TrimSpace(d.ID) == "" || len(d.ID) > 128 {
		return Daemon{}, code("DOCKER_INFO_INVALID")
	}
	if d.OSType != "linux" || (d.Architecture != "x86_64" && d.Architecture != "amd64") {
		return Daemon{}, code("DOCKER_PLATFORM_UNSUPPORTED")
	}
	majorPart, _, ok := strings.Cut(d.ServerVersion, ".")
	major, err := strconv.Atoi(majorPart)
	if !ok || err != nil || major < 28 {
		return Daemon{}, code("DOCKER_VERSION_UNSUPPORTED")
	}
	return Daemon{ID: d.ID, Version: d.ServerVersion}, nil
}
func (e *Engine) Info(ctx context.Context) (Daemon, error) {
	raw, err := e.Bytes(ctx, []string{"info", "--format", "{{json .}}"}, nil, 1<<20)
	if err != nil {
		return Daemon{}, err
	}
	return ParseDaemonInfo(raw)
}
