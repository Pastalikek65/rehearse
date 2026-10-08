package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdapterResourceIntentPersistsBeforeRuntime(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateForAdapter("synthetic-daemon", "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != 2 || run.AdapterID() != "forgejo" || run.AdapterContractVersion != 1 {
		t.Fatalf("new profile metadata absent: %#v", run)
	}
	if len(run.Resources) != 24 {
		t.Fatalf("expected 24 fixed resources, got %d", len(run.Resources))
	}
	loaded, err := s.Load(run.ID)
	if err != nil || !reflect.DeepEqual(loaded, run) {
		t.Fatalf("profile intent not durable: %v", err)
	}
	for _, phase := range []string{"baseline", "target", "recovery"} {
		dataVolumes := 0
		for _, resource := range loaded.Resources {
			if resource.Phase == phase && resource.Role == "data" && resource.Kind == "volume" {
				dataVolumes++
			}
		}
		if dataVolumes != 1 {
			t.Fatalf("phase %s lacks exactly one owned data volume", phase)
		}
	}
	if err := ValidateRunResources(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Resources[0].Name = "production-resource"
	if err := ValidateRunResources(loaded); err == nil {
		t.Fatal("profile accepted foreign resource intent")
	}
}

func TestLegacyStateAndUnknownAdapterContracts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("synthetic-daemon")
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != 1 || run.AdapterID() != "miniflux" || len(run.Resources) != 18 {
		t.Fatal("legacy intent changed")
	}
	if _, err := s.CreateForAdapter("synthetic-daemon", "custom"); err == nil {
		t.Fatal("unknown adapter accepted")
	}
	forgejo, err := s.CreateForAdapter("synthetic-daemon", "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Run){
		func(r *Run) { r.AdapterContractVersion = 999 },
		func(r *Run) { r.Adapter = "custom" },
		func(r *Run) { r.SchemaVersion = 999 },
		func(r *Run) { r.SchemaVersion = 1 },
	} {
		invalid := forgejo
		mutation(&invalid)
		data, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := s.RunDir(forgejo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(forgejo.ID); err == nil {
			t.Fatal("unknown/ambiguous file or adapter contract accepted")
		}
	}
}
