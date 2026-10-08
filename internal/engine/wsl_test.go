package engine

import (
	"reflect"
	"testing"
)

func TestWSLTransportNamesDistributionAndPinsLocalSocket(t *testing.T) {
	args, err := WSLArguments("RehearseTest2404-20261008", []string{"exec", "-i", "owned-probe", "curl", "--disable", "--config", "-"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--distribution", "RehearseTest2404-20261008", "--exec", "env", "-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "docker", "--host", "unix:///var/run/docker.sock", "exec", "-i", "owned-probe", "curl", "--disable", "--config", "-"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("transport changed daemon or distro: %#v", args)
	}
	for _, name := range []string{"", "--shutdown", "foo\nbar", "foo bar", "../distro"} {
		if _, err := WSLArguments(name, nil); err == nil {
			t.Fatalf("unsafe distribution %q", name)
		}
	}
}
