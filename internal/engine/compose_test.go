package engine

import (
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/state"
	"strings"
	"testing"
)

func TestComposeHasOnlyFreshPhaseResourcesAndNoHostExposure(t *testing.T) {
	owner := strings.Repeat("a", 32)
	id := strings.Repeat("b", 32)
	run := state.Run{SchemaVersion: 1, ID: id, OwnerID: owner, DaemonID: "daemon", Resources: state.IntendedResources(owner, id)}
	raw, err := Compose(run, "target")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]map[string]any `json:"services"`
		Networks map[string]map[string]any `json:"networks"`
		Volumes  map[string]map[string]any `json:"volumes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Services) != 4 || len(doc.Networks) != 1 || len(doc.Volumes) != 1 {
		t.Fatal("unexpected resources")
	}
	n := doc.Networks["isolated"]
	if n["internal"] != true || n["enable_ipv6"] != true {
		t.Fatal("network isolation missing")
	}
	options := n["driver_opts"].(map[string]any)
	if options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || options["com.docker.network.bridge.gateway_mode_ipv6"] != "isolated" {
		t.Fatal("host gateway still exposed")
	}
	for role, svc := range doc.Services {
		for _, field := range []string{"ports", "privileged", "network_mode", "devices", "pid", "ipc", "cap_add"} {
			if _, ok := svc[field]; ok {
				t.Fatalf("unsafe %s on %s", field, role)
			}
		}
		if !strings.HasPrefix(svc["container_name"].(string), "rehearse-"+id+"-target-") {
			t.Fatal("foreign container")
		}
		if !strings.Contains(svc["image"].(string), "@sha256:") {
			t.Fatal("mutable image")
		}
		if svc["platform"] != "linux/amd64" {
			t.Fatal("unqualified platform")
		}
		nets := svc["networks"].([]any)
		if len(nets) != 1 || nets[0] != "isolated" {
			t.Fatal("extra attachment")
		}
		labels := svc["labels"].(map[string]any)
		if labels["io.rehearse.owner"] != owner || labels["io.rehearse.run"] != id {
			t.Fatal("ownership missing")
		}
		if vols, ok := svc["volumes"]; ok {
			for _, v := range vols.([]any) {
				m := v.(map[string]any)
				if m["type"] != "volume" || m["source"] != "database" {
					t.Fatal("arbitrary mount")
				}
			}
		}
	}
}

func TestComposeRejectsForgedIntentAndUnknownPhase(t *testing.T) {
	owner := strings.Repeat("a", 32)
	id := strings.Repeat("b", 32)
	run := state.Run{SchemaVersion: 1, ID: id, OwnerID: owner, DaemonID: "daemon", Resources: state.IntendedResources(owner, id)}
	if _, err := Compose(run, "../../production"); err == nil {
		t.Fatal("accepted arbitrary phase")
	}
	run.Resources[0].Name = "production"
	if _, err := Compose(run, "baseline"); err == nil {
		t.Fatal("accepted forged resource")
	}
}

func TestLiveOwnershipRequiresExactLabelsNameTypeAndDaemon(t *testing.T) {
	r := state.Resource{Kind: "container", Role: "db", Name: "rehearse-abc-target-db", Labels: map[string]string{"io.rehearse.owner": "owner", "io.rehearse.run": "run", "io.rehearse.kind": "db"}}
	live := LiveResource{Kind: "container", Name: r.Name, ID: "immutable-id", Labels: map[string]string{"io.rehearse.owner": "owner", "io.rehearse.run": "run", "io.rehearse.kind": "db", "com.docker.compose.project": "extra-label-allowed"}}
	if err := VerifyOwnership(r, live, "daemon", "daemon"); err != nil {
		t.Fatal(err)
	}
	cases := []LiveResource{live, live, live, live}
	cases[0].Name = "production-db"
	cases[1].Kind = "volume"
	cases[2].ID = ""
	cases[3].Labels = map[string]string{"io.rehearse.owner": "another", "io.rehearse.run": "run", "io.rehearse.kind": "db"}
	for _, bad := range cases {
		if err := VerifyOwnership(r, bad, "daemon", "daemon"); err == nil {
			t.Fatal("foreign resource accepted")
		}
	}
	if err := VerifyOwnership(r, live, "old-daemon", "new-daemon"); err == nil {
		t.Fatal("cross-daemon cleanup accepted")
	}
}
