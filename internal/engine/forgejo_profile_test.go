package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func forgejoRun(t *testing.T) state.Run {
	t.Helper()
	owner, id := strings.Repeat("a", 32), strings.Repeat("b", 32)
	resources, err := state.IntendedResourcesForAdapter(owner, id, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	return state.Run{SchemaVersion: 2, Adapter: "forgejo", AdapterContractVersion: 1, ID: id, OwnerID: owner, DaemonID: "synthetic-daemon", Resources: resources}
}

func TestForgejoComposeCreatesOnlyFixedOwnedPhaseAndFreshVolumes(t *testing.T) {
	run := forgejoRun(t)
	for phase, wantApp := range map[string]string{"baseline": forgejo.SourceImage, "target": forgejo.TargetImage, "recovery": forgejo.SourceImage} {
		t.Run(phase, func(t *testing.T) {
			testForgejoComposePhase(t, run, phase, wantApp)
		})
	}
}

func testForgejoComposePhase(t *testing.T, run state.Run, phase, wantApp string) {
	t.Helper()
	raw, err := Compose(run, phase)
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
	if len(doc.Services) != 5 || len(doc.Networks) != 1 || len(doc.Volumes) != 2 {
		t.Fatalf("Forgejo target resources: services=%d networks=%d volumes=%d", len(doc.Services), len(doc.Networks), len(doc.Volumes))
	}
	if got := doc.Services["app"]["image"]; got != wantApp {
		t.Fatalf("app image=%v, want %s", got, wantApp)
	}
	if got := doc.Services["migration"]["command"]; got == nil {
		t.Fatal("fixed migration command absent")
	}
	if got := doc.Services["data-restore"]["network_mode"]; got != "none" {
		t.Fatalf("archive helper network mode=%v", got)
	}
	if _, hasNetworks := doc.Services["data-restore"]["networks"]; hasNetworks {
		t.Fatal("archive helper attached to a network")
	}
	wantImages := map[string]string{"db": forgejo.PostgresImage, "app": wantApp, "migration": wantApp, "probe": forgejo.ProbeImage, "data-restore": wantApp}
	for role, service := range doc.Services {
		for _, field := range []string{"ports", "privileged", "devices", "cap_add", "pid", "ipc", "volumes_from"} {
			if _, ok := service[field]; ok {
				t.Fatalf("%s has unsafe/unsupported field %s", role, field)
			}
		}
		if service["platform"] != "linux/amd64" || !strings.Contains(service["image"].(string), "@sha256:") || service["image"] != wantImages[role] {
			t.Fatalf("%s is not an immutable linux/amd64 image", role)
		}
		labels := service["labels"].(map[string]any)
		if labels["io.rehearse.owner"] != run.OwnerID || labels["io.rehearse.run"] != run.ID || labels["io.rehearse.kind"] != role {
			t.Fatalf("%s missing ownership labels: %#v", role, labels)
		}
		if role == "app" || role == "migration" {
			if service["user"] != "1000:1000" {
				t.Fatalf("%s must run explicitly as the Forgejo data owner, got %v", role, service["user"])
			}
			wantCommand := "web"
			if role == "migration" {
				wantCommand = "migrate"
			}
			if service["entrypoint"].([]any)[0] != "/usr/local/bin/forgejo" {
				t.Fatalf("%s has unexpected entrypoint", role)
			}
			command := service["command"].([]any)
			if !reflect.DeepEqual(command, []any{"--work-path", "/data/gitea", "--config", "/data/.rehearse-runtime/app.ini", wantCommand}) {
				t.Fatalf("%s command=%#v", role, command)
			}
		}
		if role == "data-restore" && service["user"] != "1000:1000" {
			t.Fatalf("archive helper has unexpected effective user: %v", service["user"])
		}
		if role == "db" || role == "app" || role == "migration" || role == "data-restore" {
			volumes := service["volumes"].([]any)
			wantSource, wantTarget := "data", "/data"
			if role == "db" {
				wantSource, wantTarget = "database", "/var/lib/postgresql/data"
			}
			if len(volumes) != 1 {
				t.Fatalf("%s mount count=%d", role, len(volumes))
			}
			mount := volumes[0].(map[string]any)
			if mount["type"] != "volume" || mount["source"] != wantSource || mount["target"] != wantTarget {
				t.Fatalf("%s has unexpected mount: %#v", role, mount)
			}
		} else if _, exists := service["volumes"]; exists {
			t.Fatalf("%s has unexpected mount", role)
		}
	}
	network := doc.Networks["isolated"]
	if network["internal"] != true || network["enable_ipv6"] != true {
		t.Fatal("isolated internal dual-stack network missing")
	}
	options := network["driver_opts"].(map[string]any)
	if options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || options["com.docker.network.bridge.gateway_mode_ipv6"] != "isolated" {
		t.Fatal("Forgejo phase omitted the isolated gateway mode")
	}
	phaseNames := map[string]string{}
	for _, resource := range run.Resources {
		if resource.Phase == phase {
			phaseNames[resource.Role] = resource.Name
		}
	}
	for role, name := range map[string]string{"network": phaseNames["network"], "database": phaseNames["volume"], "data": phaseNames["data"]} {
		var labels map[string]any
		var gotName string
		switch role {
		case "network":
			labels, gotName = doc.Networks["isolated"]["labels"].(map[string]any), doc.Networks["isolated"]["name"].(string)
		case "database", "data":
			labels, gotName = doc.Volumes[role]["labels"].(map[string]any), doc.Volumes[role]["name"].(string)
		}
		if gotName != name || labels["io.rehearse.owner"] != run.OwnerID || labels["io.rehearse.run"] != run.ID {
			t.Fatalf("%s resource name/ownership mismatch", role)
		}
	}
}

func TestForgejoComposeRejectsForgedResourceIntent(t *testing.T) {
	run := forgejoRun(t)
	run.Resources[0].Name = "production-network"
	if _, err := Compose(run, "baseline"); err == nil || err.Error() != "RUN_RESOURCES_INVALID" {
		t.Fatalf("forged resource accepted: %v", err)
	}
}

func TestForgejoArchiveHelperCannotStartOrExecuteACommand(t *testing.T) {
	e := &Engine{executable: "must-not-run"}
	run := forgejoRun(t)
	if err := e.Start(t.Context(), run, "baseline", "data-restore"); err == nil || err.Error() != "CONTAINER_START_FORBIDDEN" {
		t.Fatalf("archive helper start was not blocked before Docker: %v", err)
	}
	if err := e.Inside(t.Context(), run, "baseline", "data-restore", []string{"sh"}, nil, nil); err == nil || err.Error() != "CONTAINER_EXEC_FORBIDDEN" {
		t.Fatalf("archive helper exec was not blocked before Docker: %v", err)
	}
	if err := e.CopyForgejoData(t.Context(), run, "baseline", nil); err == nil || err.Error() != "FORGEJO_ARCHIVE_INVALID" {
		t.Fatalf("nil archive stream was not blocked before Docker: %v", err)
	}
}

func TestForgejoCopyOutArgumentsAreFixedAndUseOnlyAnInspectedContainerID(t *testing.T) {
	id := strings.Repeat("a", 64)
	args, err := forgejoDataCopyOutArgs(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"container", "cp", "--archive", id + ":/data/.", "-"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("copy-out arguments = %#v, want %#v", args, want)
	}
	for _, invalid := range []string{"", "123", "-" + strings.Repeat("a", 63), strings.Repeat("z", 64), strings.Repeat("a", 65), strings.Repeat("a", 63) + "/x"} {
		if _, err := forgejoDataCopyOutArgs(invalid); err == nil || err.Error() != "CONTAINER_INSPECT_INVALID" {
			t.Fatalf("invalid container identifier %q accepted: %v", invalid, err)
		}
	}
}

func TestForgejoDataRestoreInspectionRequiresStoppedIsolatedOwnedVolumeAndPinnedImage(t *testing.T) {
	good := []byte(`{"Id":"restore-id","Name":"/owned-restore","Config":{"Image":"codeberg.org/forgejo/forgejo@sha256:source","User":"1000:1000","Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"data-restore"}},"State":{"Status":"created","Running":false,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"none","ReadonlyRootfs":true,"PortBindings":{},"Binds":["owned-data:/data:rw"],"CapDrop":["ALL"],"CapAdd":[],"SecurityOpt":["no-new-privileges:true"],"Devices":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"none":{"NetworkID":""}},"Ports":{}},"Mounts":[{"Type":"volume","Name":"owned-data","Destination":"/data"}]}`)
	boundary := ContainerBoundary{Adapter: "forgejo", Role: "data-restore", NetworkName: "owned-network", NetworkID: "network-id", DataVolumeName: "owned-data", ExpectedImage: "codeberg.org/forgejo/forgejo@sha256:source", ExpectedUser: "1000:1000", Stage: ContainerStageCreated}
	parsed, err := ParseBoundedContainerForAdapter(good, boundary)
	if err != nil || parsed.Running || parsed.Status != "created" {
		t.Fatalf("created isolated restore helper rejected: %#v %v", parsed, err)
	}
	for name, change := range map[string]func(map[string]any){
		"wrong-network-mode": func(d map[string]any) { d["HostConfig"].(map[string]any)["NetworkMode"] = "owned-network" },
		"attached-network": func(d map[string]any) {
			d["NetworkSettings"].(map[string]any)["Networks"] = map[string]any{"owned-network": map[string]any{"NetworkID": "network-id"}}
		},
		"wrong-image":    func(d map[string]any) { d["Config"].(map[string]any)["Image"] = "unreviewed-image:latest" },
		"wrong-user":     func(d map[string]any) { d["Config"].(map[string]any)["User"] = "0:0" },
		"foreign-volume": func(d map[string]any) { d["Mounts"].([]any)[0].(map[string]any)["Name"] = "production-data" },
		"bind-mount":     func(d map[string]any) { d["Mounts"] = []any{map[string]any{"Type": "bind", "Destination": "/data"}} },
		"host-namespace": func(d map[string]any) { d["HostConfig"].(map[string]any)["PidMode"] = "host" },
		"capability-added": func(d map[string]any) {
			d["HostConfig"].(map[string]any)["CapAdd"] = []any{"SYS_ADMIN"}
		},
		"read-write-root": func(d map[string]any) { d["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"started-helper": func(d map[string]any) {
			d["State"].(map[string]any)["Status"] = "running"
			d["State"].(map[string]any)["Running"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(good, &doc); err != nil {
				t.Fatal(err)
			}
			change(doc)
			bad, _ := json.Marshal(doc)
			if _, err := ParseBoundedContainerForAdapter(bad, boundary); err == nil {
				t.Fatal("unsafe data restore helper accepted")
			}
		})
	}
}

func TestForgejoAppInspectionAllowsOnlyItsDataVolume(t *testing.T) {
	good := []byte(`{"Id":"app-id","Name":"/owned-app","Config":{"Image":"codeberg.org/forgejo/forgejo@sha256:target","User":"1000:1000","Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"app"}},"State":{"Status":"running","Running":true,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"owned-network","ReadonlyRootfs":true,"PortBindings":{},"Binds":["owned-data:/data:rw"],"CapDrop":["ALL"],"CapAdd":[],"SecurityOpt":["no-new-privileges:true"],"Devices":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"owned-network":{"NetworkID":"network-id"}},"Ports":{}},"Mounts":[{"Type":"volume","Name":"owned-data","Destination":"/data"}]}`)
	boundary := ContainerBoundary{Adapter: "forgejo", Role: "app", NetworkName: "owned-network", NetworkID: "network-id", DatabaseVolumeName: "owned-db", DataVolumeName: "owned-data", ExpectedImage: "codeberg.org/forgejo/forgejo@sha256:target", ExpectedUser: "1000:1000", Stage: ContainerStageRunning}
	if _, err := ParseBoundedContainerForAdapter(good, boundary); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(map[string]any){
		"database-mount": func(d map[string]any) {
			mounts := d["Mounts"].([]any)
			d["Mounts"] = append(mounts, map[string]any{"Type": "volume", "Name": "owned-db", "Destination": "/var/lib/postgresql/data"})
		},
		"wrong-data-destination":    func(d map[string]any) { d["Mounts"].([]any)[0].(map[string]any)["Destination"] = "/etc" },
		"bind-mount-in-host-config": func(d map[string]any) { d["HostConfig"].(map[string]any)["Binds"] = []string{"/production:/data:rw"} },
		"missing-cap-drop":          func(d map[string]any) { d["HostConfig"].(map[string]any)["CapDrop"] = []any{} },
		"missing-no-new-privileges": func(d map[string]any) { d["HostConfig"].(map[string]any)["SecurityOpt"] = []any{} },
		"wrong-user":                func(d map[string]any) { d["Config"].(map[string]any)["User"] = "" },
		"published-port": func(d map[string]any) {
			d["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"3000/tcp": []any{map[string]any{"HostIp": "0.0.0.0", "HostPort": "3000"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(good, &doc); err != nil {
				t.Fatal(err)
			}
			change(doc)
			bad, _ := json.Marshal(doc)
			if _, err := ParseBoundedContainerForAdapter(bad, boundary); err == nil {
				t.Fatal("unsafe Forgejo app inspection accepted")
			}
		})
	}
}

func TestForgejoStoppedAppInspectionRequiresExitedAppAndOwnedNetwork(t *testing.T) {
	good := []byte(`{"Id":"app-id","Name":"/owned-app","Config":{"Image":"codeberg.org/forgejo/forgejo@sha256:target","User":"1000:1000","Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"app"}},"State":{"Status":"exited","Running":false,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"owned-network","ReadonlyRootfs":true,"PortBindings":{},"Binds":["owned-data:/data:rw"],"CapDrop":["ALL"],"CapAdd":[],"SecurityOpt":["no-new-privileges:true"],"Devices":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"owned-network":{"NetworkID":"network-id"}},"Ports":{}},"Mounts":[{"Type":"volume","Name":"owned-data","Destination":"/data"}]}`)
	boundary := ContainerBoundary{Adapter: "forgejo", Role: "app", NetworkName: "owned-network", NetworkID: "network-id", DatabaseVolumeName: "owned-db", DataVolumeName: "owned-data", ExpectedImage: "codeberg.org/forgejo/forgejo@sha256:target", ExpectedUser: "1000:1000", Stage: ContainerStage("stopped")}
	parsed, err := ParseBoundedContainerForAdapter(good, boundary)
	if err != nil || parsed.Running || parsed.Status != "exited" || parsed.ExitCode != 0 {
		t.Fatalf("cleanly stopped owned Forgejo app rejected: %#v %v", parsed, err)
	}
	for name, change := range map[string]func(map[string]any){
		"still-running": func(d map[string]any) {
			d["State"].(map[string]any)["Status"] = "running"
			d["State"].(map[string]any)["Running"] = true
		},
		"unclean-exit": func(d map[string]any) {
			d["State"].(map[string]any)["ExitCode"] = float64(137)
		},
		"foreign-network": func(d map[string]any) {
			d["NetworkSettings"].(map[string]any)["Networks"] = map[string]any{"production": map[string]any{"NetworkID": "foreign-network-id"}}
		},
		"additional-network": func(d map[string]any) {
			d["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["production"] = map[string]any{"NetworkID": "foreign-network-id"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(good, &doc); err != nil {
				t.Fatal(err)
			}
			change(doc)
			bad, _ := json.Marshal(doc)
			if _, err := ParseBoundedContainerForAdapter(bad, boundary); err == nil {
				t.Fatal("unsafe or unclean stopped app inspection accepted")
			}
		})
	}

	// Docker may report a stopped container as detached or still attached to
	// the one private run network. Both representations are safe; no other
	// network attachment is.
	var detached map[string]any
	if err := json.Unmarshal(good, &detached); err != nil {
		t.Fatal(err)
	}
	detached["NetworkSettings"].(map[string]any)["Networks"] = map[string]any{}
	raw, _ := json.Marshal(detached)
	if _, err := ParseBoundedContainerForAdapter(raw, boundary); err != nil {
		t.Fatalf("detached stopped app rejected: %v", err)
	}
	var wrongRole map[string]any
	if err := json.Unmarshal(good, &wrongRole); err != nil {
		t.Fatal(err)
	}
	wrongRole["Config"].(map[string]any)["Labels"].(map[string]any)["io.rehearse.kind"] = "probe"
	wrongRoleRaw, _ := json.Marshal(wrongRole)
	wrongRoleParsed, err := ParseBoundedContainerForAdapter(wrongRoleRaw, boundary)
	if err != nil {
		t.Fatal(err)
	}
	expectedApp, err := resource(forgejoRun(t), "baseline", "app")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnership(expectedApp, wrongRoleParsed.LiveResource, "synthetic-daemon", "synthetic-daemon"); err == nil {
		t.Fatal("stopped container with a forged role label passed ownership verification")
	}
}

func TestStopForgejoAppRejectsOtherAdapterBeforeDocker(t *testing.T) {
	owner, id := strings.Repeat("c", 32), strings.Repeat("d", 32)
	run := state.Run{SchemaVersion: 1, ID: id, OwnerID: owner, DaemonID: "synthetic-daemon", Resources: state.IntendedResources(owner, id)}
	e := &Engine{executable: "must-not-run"}
	if err := e.StopForgejoApp(t.Context(), run, "baseline"); err == nil || err.Error() != "FORGEJO_STOP_FORBIDDEN" {
		t.Fatalf("non-Forgejo stop was not rejected before Docker: %v", err)
	}
}

func TestContainerIdentityMustRemainStableAcrossArchiveCopy(t *testing.T) {
	before := ContainerInspection{LiveResource: LiveResource{ID: "inspected-before"}}
	after := ContainerInspection{LiveResource: LiveResource{ID: "replacement-after"}}
	if err := verifySameContainerIdentity(before, after); err == nil || err.Error() != "CONTAINER_IDENTITY_CHANGED" {
		t.Fatalf("replacement container identity accepted after archive copy: %v", err)
	}
	if err := verifySameContainerIdentity(before, before); err != nil {
		t.Fatalf("unchanged inspected container identity rejected: %v", err)
	}
}
