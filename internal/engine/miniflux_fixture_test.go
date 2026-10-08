package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMinifluxFixtureOnExplicitDisposableWSLRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	if distro == "" {
		t.Skip("real application fixture requires explicit disposable runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, err := NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(daemon.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := e.Cleanup(c, run); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	}()
	if err := e.CreatePhase(ctx, run, dir, "baseline"); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx, run, "baseline", "db"); err != nil {
		t.Fatal(err)
	}
	if err := e.WaitDatabase(ctx, run, "baseline"); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx, run, "baseline", "migration"); err != nil {
		t.Fatal(err)
	}
	if err := e.WaitMigration(ctx, run, "baseline"); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx, run, "baseline", "app"); err != nil {
		t.Fatal(err)
	}
	// Public synthetic fixture credentials appear only in this test helper,
	// never in a user configuration or the application's authenticated probes.
	appResource, _ := resource(run, "baseline", "app")
	appLive, err := e.owned(ctx, run, appResource)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := e.Bytes(ctx, []string{"exec", "-i", "--env", "CREATE_ADMIN=1", "--env", "ADMIN_USERNAME=" + fixture.AdminUsername, "--env", "ADMIN_PASSWORD=" + fixture.AdminPassword, appLive.ID, "/usr/bin/miniflux", "-refresh-feeds"}, nil, 8192)
	if err != nil {
		t.Fatalf("fixture admin creation failed: %v %q", err, raw)
	}
	sql := func(query string) []byte {
		t.Helper()
		raw, err := e.InsideBytes(ctx, run, "baseline", "db", []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}, strings.NewReader(query), 1<<20)
		if err != nil {
			t.Fatalf("fixture SQL failed: %v", err)
		}
		return raw
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(sql(miniflux.SchemaSQL))))
	if err != nil || version != fixture.Expected.Baseline.SchemaVersion {
		t.Fatalf("schema mismatch: %d %v", version, err)
	}
	t.Logf("initial category count: %s", strings.TrimSpace(string(sql("SELECT count(*) FROM categories"))))
	sql(fixture.SeedSQL)
	var counts fixture.Counts
	if err := json.Unmarshal(sql(miniflux.CountsSQL), &counts); err != nil {
		t.Fatal(err)
	}
	if counts != fixture.Expected.Baseline.Counts {
		t.Fatalf("fixture counts mismatch: %#v", counts)
	}
	if _, err := miniflux.Fingerprint(strings.NewReader(string(sql(miniflux.ProjectionSQL)))); err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if err := e.Start(ctx, run, "baseline", "probe"); err != nil {
		t.Fatal(err)
	}
	auth := miniflux.Auth{Username: fixture.AdminUsername, Password: fixture.AdminPassword}
	request, err := miniflux.CurlConfig("/v1/entries?limit=100&status=unread", &auth)
	if err != nil {
		t.Fatal(err)
	}
	response, err := e.InsideBytes(ctx, run, "baseline", "probe", []string{"curl", "--disable", "--config", "-"}, strings.NewReader(string(request)), miniflux.MaxAPIResponseBytes+8)
	if err != nil {
		t.Fatal(err)
	}
	status, body, err := miniflux.ParseResponse(response)
	if err != nil || status != 200 {
		t.Fatalf("source API status %d: %v; synthetic body %s", status, err, body)
	}
	var list struct {
		Total   int `json:"total"`
		Entries []struct {
			ID int64 `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 1 || len(list.Entries) != 1 || list.Entries[0].ID != fixture.UnreadEntryID {
		t.Fatalf("unread API entry missing: %s %v", body, err)
	}
	if err := e.VerifyPhase(ctx, run, "baseline"); err != nil {
		for _, r := range run.Resources {
			if r.Phase != "baseline" || r.Kind != "container" || r.Role == "migration" {
				continue
			}
			raw, inspectErr := e.inspect(ctx, r)
			if inspectErr != nil {
				continue
			}
			var d map[string]any
			if json.Unmarshal(raw, &d) != nil {
				continue
			}
			h, _ := d["HostConfig"].(map[string]any)
			n, _ := d["NetworkSettings"].(map[string]any)
			t.Logf("synthetic %s boundary diagnostic: networkMode=%v binds=%v portBindings=%v mounts=%v networks=%v", r.Role, h["NetworkMode"], h["Binds"], h["PortBindings"], d["Mounts"], n["Networks"])
		}
		t.Fatal(err)
	}
	if output := os.Getenv("REHEARSE_FIXTURE_OUTPUT"); output != "" {
		dump, err := e.InsideBytes(ctx, run, "baseline", "db", []string{"pg_dump", "--username", "rehearse", "--dbname", "rehearse", "--format=custom", "--no-owner", "--no-acl"}, nil, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(dump) < 5 || string(dump[:5]) != "PGDMP" {
			t.Fatal("fixture dump output invalid")
		}
		absolute, err := filepath.Abs(output)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("fixture output already exists or cannot be created")
		}
		_, err = file.Write(dump)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal("fixture dump write failed")
		}
		t.Logf("synthetic source archive exported: bytes=%d sha256=%x", len(dump), sha256.Sum256(dump))
	}
	t.Log("real source schema, admin creation, deterministic fixture and projection passed")
}
