package engine

import (
	"bytes"
	"context"
	"github.com/Pastalikek65/rehearse/internal/state"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestInsideBytesRejectsInvalidLimitBeforeDockerOperation(t *testing.T) {
	e := &Engine{executable: "nonexistent"}
	for _, limit := range []int{0, -1} {
		if _, err := e.InsideBytes(context.Background(), state.Run{}, "target", "probe", []string{"curl"}, nil, limit); err == nil || err.Error() != "DOCKER_OUTPUT_LIMIT_INVALID" {
			t.Fatalf("invalid limit not rejected: %v", err)
		}
	}
}

func TestWindowsMustSelectExplicitWSLDistribution(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows transport requirement")
	}
	if _, err := New(); err == nil || err.Error() != "WSL_DISTRIBUTION_REQUIRED" {
		t.Fatalf("silently accepted configured daemon: %v", err)
	}
}

func TestCommandOutputIsPreservedAndRawFailureIsWithheld(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{executable: goPath}
	output, err := e.Bytes(context.Background(), []string{"version"}, nil, 4096)
	if err != nil || !strings.HasPrefix(string(output), "go version ") {
		t.Fatalf("stdout missing: %q %v", output, err)
	}
	_, err = e.Bytes(context.Background(), []string{"private-SYNTHETIC-SECRET"}, nil, 4096)
	if err == nil || err.Error() != "DOCKER_COMMAND_FAILED" || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe failure: %v", err)
	}
}

func TestCommandCannotOverflowOutputLimit(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{executable: goPath}
	raw, err := e.Bytes(context.Background(), []string{"version"}, nil, 4)
	if err == nil || err.Error() != "DOCKER_OUTPUT_TOO_LARGE" || len(raw) != 0 {
		t.Fatalf("unbounded output: %q %v", raw, err)
	}
}

func TestCanceledCommandNeverStarts(t *testing.T) {
	e := &Engine{executable: "nonexistent"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b bytes.Buffer
	if err := e.Execute(ctx, []string{"anything"}, nil, &b); err == nil || err.Error() != "CANCELED" {
		t.Fatalf("cancel lost: %v", err)
	}
}

func TestDaemonCapabilityRejectsWrongPlatformOrOldEngine(t *testing.T) {
	good := []byte(`{"ID":"daemon-one","OSType":"linux","Architecture":"x86_64","ServerVersion":"29.8.2"}`)
	d, err := ParseDaemonInfo(good)
	if err != nil || d.ID != "daemon-one" {
		t.Fatalf("valid engine rejected: %#v %v", d, err)
	}
	for _, raw := range []string{`{"ID":"x","OSType":"windows","Architecture":"x86_64","ServerVersion":"29.8.2"}`, `{"ID":"x","OSType":"linux","Architecture":"aarch64","ServerVersion":"29.8.2"}`, `{"ID":"x","OSType":"linux","Architecture":"x86_64","ServerVersion":"27.5.1"}`, `{"ID":"","OSType":"linux","Architecture":"x86_64","ServerVersion":"29.8.2"}`, `{"ID":"x","OSType":"linux","Architecture":"x86_64","ServerVersion":"unknown"}`} {
		if _, err := ParseDaemonInfo([]byte(raw)); err == nil {
			t.Fatalf("unsupported daemon accepted: %s", raw)
		}
	}
}
