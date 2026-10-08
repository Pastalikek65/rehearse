package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkInspectionRejectsInternalBridgeWithReachableHostGateway(t *testing.T) {
	good := []byte(`{"Id":"network-id","Name":"owned-network","Driver":"bridge","Internal":true,"EnableIPv6":true,"Options":{"com.docker.network.bridge.gateway_mode_ipv4":"isolated","com.docker.network.bridge.gateway_mode_ipv6":"isolated"},"IPAM":{"Config":[{"Subnet":"172.30.0.0/16"},{"Subnet":"fd00:1234::/64"}]},"Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"network"}}`)
	if _, err := ParseIsolatedNetwork(good); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(good, &doc)
	ipam := doc["IPAM"].(map[string]any)
	ipam["Config"].([]any)[0].(map[string]any)["Gateway"] = "172.30.0.1"
	bad, _ := json.Marshal(doc)
	if _, err := ParseIsolatedNetwork(bad); err == nil {
		t.Fatal("host bridge gateway accepted")
	}
	delete(ipam["Config"].([]any)[0].(map[string]any), "Gateway")
	doc["Internal"] = false
	bad, _ = json.Marshal(doc)
	if _, err := ParseIsolatedNetwork(bad); err == nil {
		t.Fatal("external network accepted")
	}
	doc["Internal"] = true
	doc["Options"].(map[string]any)["com.docker.network.bridge.gateway_mode_ipv6"] = "nat"
	bad, _ = json.Marshal(doc)
	if _, err := ParseIsolatedNetwork(bad); err == nil {
		t.Fatal("IPv6 host gateway exposed")
	}
}

func TestManagedVolumeRejectsHostDeviceOptionsAndForeignDriver(t *testing.T) {
	good := []byte(`{"Name":"owned-volume","Driver":"local","Scope":"local","Options":{},"Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"volume"}}`)
	if _, err := ParseManagedVolume(good); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"Name":"owned-volume","Driver":"local","Scope":"local","Options":{"type":"none","o":"bind","device":"/production"}}`, `{"Name":"owned-volume","Driver":"third-party","Scope":"local","Options":{}}`} {
		if _, err := ParseManagedVolume([]byte(raw)); err == nil {
			t.Fatal("arbitrary volume driver/mount accepted")
		}
	}
}

func TestContainerInspectionRejectsExtraNetworkSocketMountAndHostPort(t *testing.T) {
	good := []byte(`{"Id":"container-id","Name":"/owned-db","Config":{"Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"db"}},"State":{"Status":"running","Running":true,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"owned-network","PortBindings":{},"Binds":["owned-volume:/var/lib/postgresql/data:rw"],"CapAdd":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"owned-network":{"NetworkID":"network-id"}},"Ports":{"5432/tcp":null}},"Mounts":[{"Type":"volume","Name":"owned-volume","Destination":"/var/lib/postgresql/data"}]}`)
	if _, err := ParseBoundedContainer(good, "db", "owned-network", "network-id", "owned-volume", ContainerStageRunning); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(d map[string]any) {
			d["HostConfig"].(map[string]any)["Binds"] = []string{"/production:/var/lib/postgresql/data:rw"}
		},
		func(d map[string]any) { d["HostConfig"].(map[string]any)["Privileged"] = true },
		func(d map[string]any) {
			d["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["production"] = map[string]any{"NetworkID": "foreign"}
		},
		func(d map[string]any) {
			d["Mounts"] = []any{map[string]any{"Type": "bind", "Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock"}}
		},
		func(d map[string]any) {
			d["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "15432"}}}
		},
		func(d map[string]any) { d["Mounts"].([]any)[0].(map[string]any)["Name"] = "production-volume" },
	} {
		var d map[string]any
		json.Unmarshal(good, &d)
		change(d)
		bad, _ := json.Marshal(d)
		if _, err := ParseBoundedContainer(bad, "db", "owned-network", "network-id", "owned-volume", ContainerStageRunning); err == nil {
			t.Fatal("unsafe live container accepted")
		}
	}
}

func TestContainerInspectionRejectsNamespaceSharingAndRequiresRunning(t *testing.T) {
	good := []byte(`{"Id":"container-id","Name":"/owned-db","Config":{"Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"db"}},"State":{"Status":"running","Running":true,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"owned-network","PortBindings":{},"Binds":["owned-volume:/var/lib/postgresql/data:rw"],"CapAdd":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"owned-network":{"NetworkID":"network-id"}},"Ports":{"5432/tcp":null}},"Mounts":[{"Type":"volume","Name":"owned-volume","Destination":"/var/lib/postgresql/data"}]}`)
	if _, err := ParseBoundedContainer(good, "db", "owned-network", "network-id", "owned-volume", ContainerStageRunning); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(d map[string]any) { d["HostConfig"].(map[string]any)["PidMode"] = "host" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["PidMode"] = "container:foreign" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["IpcMode"] = "host" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["IpcMode"] = "container:foreign" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["IpcMode"] = "shareable" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["UTSMode"] = "host" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["UTSMode"] = "container:foreign" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["CgroupnsMode"] = "host" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["CgroupnsMode"] = "container:foreign" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["UsernsMode"] = "host" },
		func(d map[string]any) {
			d["State"].(map[string]any)["Status"] = "created"
			d["State"].(map[string]any)["Running"] = false
		},
		func(d map[string]any) { d["State"].(map[string]any)["Running"] = false },
		func(d map[string]any) { d["State"].(map[string]any)["Dead"] = true },
	} {
		var d map[string]any
		if err := json.Unmarshal(good, &d); err != nil {
			t.Fatal(err)
		}
		change(d)
		bad, _ := json.Marshal(d)
		if _, err := ParseBoundedContainer(bad, "db", "owned-network", "network-id", "owned-volume", ContainerStageRunning); err == nil {
			t.Fatal("shared namespace or non-running container accepted")
		}
	}
}

func TestMigrationInspectionDistinguishesCreatedRunningAndExited(t *testing.T) {
	created := []byte(`{"Id":"migration-id","Name":"/owned-migration","Config":{"Labels":{"io.rehearse.owner":"o","io.rehearse.run":"r","io.rehearse.kind":"migration"}},"State":{"Status":"created","Running":false,"ExitCode":0,"Dead":false},"HostConfig":{"Privileged":false,"NetworkMode":"owned-network","PortBindings":{},"Binds":[],"CapAdd":[],"PidMode":"","IpcMode":"private","UTSMode":"","CgroupnsMode":"private","UsernsMode":""},"NetworkSettings":{"Networks":{"owned-network":{"NetworkID":""}},"Ports":{}},"Mounts":[{"Type":"tmpfs","Name":"","Destination":"/tmp"}]}`)
	parsed, err := ParseBoundedContainer(created, "migration", "owned-network", "network-id", "owned-volume", ContainerStageCreated)
	if err != nil || parsed.Status != "created" || parsed.Running {
		t.Fatalf("created migration inspection rejected: %#v %v", parsed, err)
	}
	if _, err := ParseBoundedContainer(created, "migration", "owned-network", "network-id", "owned-volume", ContainerStageRunning); err == nil {
		t.Fatal("created migration accepted as running")
	}
	for _, change := range []func(map[string]any){
		func(d map[string]any) { d["HostConfig"].(map[string]any)["NetworkMode"] = "container:foreign" },
		func(d map[string]any) { d["HostConfig"].(map[string]any)["Binds"] = []any{"/production:/data:rw"} },
		func(d map[string]any) {
			d["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["production"] = map[string]any{"NetworkID": "foreign"}
		},
		func(d map[string]any) {
			d["Mounts"] = []any{
				map[string]any{"Type": "tmpfs", "Name": "", "Destination": "/tmp"},
				map[string]any{"Type": "bind", "Name": "", "Destination": "/var/run/docker.sock"},
			}
		},
	} {
		var doc map[string]any
		if err := json.Unmarshal(created, &doc); err != nil {
			t.Fatal(err)
		}
		change(doc)
		bad, _ := json.Marshal(doc)
		if _, err := ParseBoundedContainer(bad, "migration", "owned-network", "network-id", "owned-volume", ContainerStageCreated); err == nil {
			t.Fatal("unsafe created migration accepted")
		}
	}
	running := strings.ReplaceAll(string(created), `"Status":"created","Running":false`, `"Status":"running","Running":true`)
	running = strings.ReplaceAll(running, `"NetworkID":""`, `"NetworkID":"network-id"`)
	if _, err := ParseBoundedContainer([]byte(running), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigration); err != nil {
		t.Fatalf("running migration rejected: %v", err)
	}
	exited := strings.ReplaceAll(string(created), `"Status":"created","Running":false`, `"Status":"exited","Running":false`)
	if _, err := ParseBoundedContainer([]byte(exited), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigration); err != nil {
		t.Fatalf("successful fast-exit migration rejected: %v", err)
	}
	missingExitCode := strings.ReplaceAll(exited, `,"ExitCode":0`, "")
	if _, err := ParseBoundedContainer([]byte(missingExitCode), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigrationExited); err == nil {
		t.Fatal("migration inspect without an explicit exit code passed as exit code zero")
	}
	if _, err := ParseBoundedContainer([]byte(exited), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigrationExited); err != nil {
		t.Fatalf("exited migration rejected: %v", err)
	}
	exitedOnForeignNetwork := strings.ReplaceAll(exited, `"Networks":{"owned-network":{"NetworkID":""}}`, `"Networks":{"production":{"NetworkID":"foreign"}}`)
	if _, err := ParseBoundedContainer([]byte(exitedOnForeignNetwork), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigration); err == nil {
		t.Fatal("exited migration on foreign network accepted")
	}
	nonzero := strings.ReplaceAll(exited, `"ExitCode":0`, `"ExitCode":9`)
	if _, err := ParseBoundedContainer([]byte(nonzero), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigration); err == nil {
		t.Fatal("non-zero fast exit accepted as successful migration")
	}
	nonzeroForeignNetwork := strings.ReplaceAll(nonzero, `"Networks":{"owned-network":{"NetworkID":""}}`, `"Networks":{"production":{"NetworkID":"foreign"}}`)
	if _, err := ParseBoundedContainer([]byte(nonzeroForeignNetwork), "migration", "owned-network", "network-id", "owned-volume", ContainerStageMigration); err == nil || err.Error() == "MIGRATION_FAILED" {
		t.Fatal("non-zero exit skipped network-boundary validation")
	}
}

func TestMigrationWaitRequiresOwnedInspectedExitToMatchWaitResult(t *testing.T) {
	inspected := ContainerInspection{Status: "exited", ExitCode: 0}
	if err := verifyMigrationExitCode([]byte("0\n"), inspected); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		output string
		code   int
	}{
		{name: "wait mismatch", output: "9\n", code: 0},
		{name: "inspect mismatch", output: "0\n", code: 9},
		{name: "failed migration", output: "9\n", code: 9},
		{name: "malformed wait result", output: "unknown\n", code: 0},
		{name: "not exited", output: "0\n", code: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := inspected
			observed.ExitCode = tc.code
			if tc.name == "not exited" {
				observed.Status = "running"
			}
			if err := verifyMigrationExitCode([]byte(tc.output), observed); err == nil {
				t.Fatal("inconsistent or failed migration result accepted")
			}
		})
	}
}
