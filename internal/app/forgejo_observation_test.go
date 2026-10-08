package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoAPIStub struct {
	runtimeClient
	response map[string]string
}

func (f forgejoAPIStub) InsideBytes(_ context.Context, _ state.Run, _, role string, args []string, input io.Reader, _ int) ([]byte, error) {
	if role != "probe" || strings.Join(args, " ") != "curl --disable --config -" {
		return nil, code("OPERATION_FAILED")
	}
	raw, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	for endpoint, response := range f.response {
		if strings.Contains(string(raw), `url = "http://app:3000`+endpoint+`"`) {
			return []byte(response), nil
		}
	}
	return nil, code("OPERATION_FAILED")
}

func TestForgejoObservationChecksVersionAndDetectsBranchChange(t *testing.T) {
	const repository = `{"id":1,"owner":{"login":"synthetic"},"name":"example","full_name":"synthetic/example","default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}`
	stub := forgejoAPIStub{response: map[string]string{
		"/api/v1/version":                               `{"version":"15.0.9"}` + "\n200\n",
		"/api/v1/user":                                  `{"id":1,"login":"synthetic","full_name":"Example","is_admin":false}` + "\n200\n",
		"/api/v1/user/repos?limit=50&page=1":            "[" + repository + "]\n200\n",
		"/api/v1/repos/synthetic/example":               repository + "\n200\n",
		"/api/v1/repos/synthetic/example/branches/main": `{"name":"main","commit":{"id":"` + strings.Repeat("a", 40) + `"}}` + "\n200\n",
	}}
	addContentResponseFixtures(stub.response, "synthetic", "example", strings.Repeat("a", 40))
	addContentResponseFixtures(stub.response, "synthetic", "example", strings.Repeat("c", 40))
	first, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.SourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Repositories[0].Contents) != 1 {
		t.Fatal("full API observation omitted content probes")
	}
	stub.response["/api/v1/repos/synthetic/example/branches/main"] = `{"name":"main","commit":{"id":"` + strings.Repeat("c", 40) + `"}}` + "\n200\n"
	changed, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "target", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.SourceVersion)
	if err != nil || sameForgejoAPI(first, changed) {
		t.Fatalf("branch change hidden: %v", err)
	}
	if _, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "target", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.TargetVersion); err == nil {
		t.Fatal("wrong binary version accepted")
	}
	stub.response["/api/v1/version"] = `{"version":"15.0.9+gitea-1.22.0"}` + "\n200\n"
	if _, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.SourceVersion); err != nil {
		t.Fatalf("qualified pinned-image version rejected: %v", err)
	}
	stub.response["/api/v1/version"] = `{"version":"15.0.9+gitea-9.9.9"}` + "\n200\n"
	if _, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.SourceVersion); err == nil {
		t.Fatal("arbitrary version suffix accepted")
	}
}

func TestForgejoAnonymousFailureDistinguishesBypassFromServerFailure(t *testing.T) {
	for _, tc := range []struct{ response, expected string }{
		{"{}\n401\n", ""}, {"{}\n200\n", "AUTH_BYPASS"}, {"{}\n403\n", "AUTH_FAILED"}, {"{}\n500\n", "AUTH_FAILED"},
	} {
		err := forgejoUnauthenticatedProbe(context.Background(), forgejoAPIStub{response: map[string]string{"/api/v1/user": tc.response}}, state.Run{}, "baseline")
		if tc.expected == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || err.Error() != tc.expected {
			t.Fatalf("expected %s: %v", tc.expected, err)
		}
	}
}

func TestForgejoObservationRejectsDuplicateRepositoriesAcrossPages(t *testing.T) {
	const repository = `{"id":1,"owner":{"login":"synthetic"},"name":"example","full_name":"synthetic/example","default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}`
	list := strings.Repeat(repository+",", 49) + repository
	stub := forgejoAPIStub{response: map[string]string{
		"/api/v1/version":                    `{"version":"15.0.9"}` + "\n200\n",
		"/api/v1/user":                       `{"id":1,"login":"synthetic","full_name":"Example","is_admin":false}` + "\n200\n",
		"/api/v1/user/repos?limit=50&page=1": "[" + list + "]\n200\n",
	}}
	if _, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("b", 40)}, forgejo.SourceVersion); err == nil {
		t.Fatal("duplicate repository identities accepted")
	}
}
