package app

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

const maxForgejoAPIRepositories = 10000

// Business fields are held only in memory for comparison, never reports/logs.
type forgejoRepositoryObservation struct {
	Repository forgejo.APIRepository
	Branch     forgejo.APIBranch
	Contents   []forgejoContentObservation
}
type forgejoAPIObservation struct {
	User         forgejo.APIUser
	Repositories []forgejoRepositoryObservation
}

func requestForgejoAPI(ctx context.Context, client runtimeClient, run state.Run, phase, endpoint string, auth *forgejo.Auth) (int, []byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, nil, code("CANCELED")
	}
	config, err := forgejo.CurlConfig(endpoint, auth)
	if err != nil {
		return 0, nil, code("AUTH_FAILED")
	}
	output, err := client.InsideBytes(ctx, run, phase, "probe", []string{"curl", "--disable", "--config", "-"}, bytes.NewReader(config), forgejo.MaxAPIResponseBytes+8)
	if ctx.Err() != nil {
		return 0, nil, code("CANCELED")
	}
	if err != nil {
		return 0, nil, code("AUTH_FAILED")
	}
	status, body, err := forgejo.ParseResponse(output)
	if err != nil {
		return 0, nil, code("AUTH_FAILED")
	}
	return status, body, nil
}

func observeForgejoAPI(ctx context.Context, client runtimeClient, run state.Run, phase string, auth forgejo.Auth, expectedVersion string) (forgejoAPIObservation, error) {
	if ctx == nil || ctx.Err() != nil {
		return forgejoAPIObservation{}, code("CANCELED")
	}
	get := func(endpoint string) ([]byte, error) {
		status, body, err := requestForgejoAPI(ctx, client, run, phase, endpoint, &auth)
		if err != nil || status != 200 {
			return nil, code("AUTH_FAILED")
		}
		return body, nil
	}
	versionBody, err := get(forgejo.VersionEndpoint())
	if err != nil {
		return forgejoAPIObservation{}, err
	}
	version, err := forgejo.ParseVersion(versionBody)
	if err != nil || !matchesForgejoAPIVersion(version, expectedVersion) {
		return forgejoAPIObservation{}, code("AUTH_FAILED")
	}
	userBody, err := get(forgejo.UserEndpoint())
	if err != nil {
		return forgejoAPIObservation{}, err
	}
	user, err := forgejo.ParseUser(userBody)
	if err != nil {
		return forgejoAPIObservation{}, code("AUTH_FAILED")
	}
	result := forgejoAPIObservation{User: user}
	ids := map[int64]bool{}
	names := map[string]bool{}
	for page := 1; page <= maxForgejoAPIRepositories/forgejo.MaxAPIListItems+1; page++ {
		endpoint, err := forgejo.UserRepositoriesEndpoint(page)
		if err != nil {
			return forgejoAPIObservation{}, code("AUTH_FAILED")
		}
		body, err := get(endpoint)
		if err != nil {
			return forgejoAPIObservation{}, err
		}
		list, err := forgejo.ParseRepoList(body)
		if err != nil || len(result.Repositories)+len(list) > maxForgejoAPIRepositories {
			return forgejoAPIObservation{}, code("AUTH_FAILED")
		}
		for _, listed := range list {
			name := strings.ToLower(listed.FullName)
			if ids[listed.ID] || names[name] {
				return forgejoAPIObservation{}, code("AUTH_FAILED")
			}
			ids[listed.ID] = true
			names[name] = true
			detailEndpoint, err := forgejo.RepositoryEndpoint(listed.OwnerLogin, listed.Name)
			if err != nil {
				return forgejoAPIObservation{}, code("AUTH_FAILED")
			}
			detailBody, err := get(detailEndpoint)
			if err != nil {
				return forgejoAPIObservation{}, err
			}
			detail, err := forgejo.ParseRepository(detailBody)
			if err != nil || detail != listed {
				return forgejoAPIObservation{}, code("AUTH_FAILED")
			}
			observation := forgejoRepositoryObservation{Repository: detail}
			if !detail.Empty {
				branchEndpoint, err := forgejo.BranchEndpoint(detail.OwnerLogin, detail.Name, detail.DefaultBranch)
				if err != nil {
					return forgejoAPIObservation{}, code("AUTH_FAILED")
				}
				branchBody, err := get(branchEndpoint)
				if err != nil {
					return forgejoAPIObservation{}, err
				}
				branch, err := forgejo.ParseBranch(branchBody)
				if err != nil || branch.Name != detail.DefaultBranch || !validGitObjectID(branch.CommitSHA) {
					return forgejoAPIObservation{}, code("AUTH_FAILED")
				}
				observation.Branch = branch
				contents, err := observeForgejoContents(ctx, client, run, phase, detail, branch, auth)
				if err != nil {
					return forgejoAPIObservation{}, err
				}
				observation.Contents = contents
			}
			result.Repositories = append(result.Repositories, observation)
		}
		if len(list) < forgejo.MaxAPIListItems {
			verifiedContent := false
			for _, observed := range result.Repositories {
				if len(observed.Contents) > 0 {
					verifiedContent = true
					break
				}
			}
			// Global SQL/Git fingerprints do not exercise repository API
			// operations. Require an actual commit-pinned content observation
			// before reporting this phase's behavior check as passed.
			if !verifiedContent {
				return forgejoAPIObservation{}, code("AUTH_FAILED")
			}
			slices.SortFunc(result.Repositories, func(a, b forgejoRepositoryObservation) int {
				if a.Repository.ID < b.Repository.ID {
					return -1
				}
				if a.Repository.ID > b.Repository.ID {
					return 1
				}
				return 0
			})
			return result, nil
		}
	}
	return forgejoAPIObservation{}, code("AUTH_FAILED")
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func matchesForgejoAPIVersion(actual, release string) bool {
	if release != forgejo.SourceVersion && release != forgejo.TargetVersion {
		return false
	}
	// Both pinned images report this exact compatibility suffix at runtime.
	// Bare release values remain accepted for canonical API fixtures.
	return actual == release || actual == release+"+gitea-1.22.0"
}

func sameForgejoAPI(a, b forgejoAPIObservation) bool { return reflect.DeepEqual(a, b) }

func startForgejoApplication(ctx context.Context, client runtimeClient, run state.Run, phase string, auth forgejo.Auth) error {
	if err := client.Start(ctx, run, phase, "app"); err != nil {
		return code("AUTH_FAILED")
	}
	if err := client.Start(ctx, run, phase, "probe"); err != nil {
		return code("AUTH_FAILED")
	}
	readyCtx, cancel := context.WithTimeout(ctx, apiReadyTimeout)
	defer cancel()
	for {
		status, body, err := requestForgejoAPI(readyCtx, client, run, phase, forgejo.UserEndpoint(), &auth)
		if err == nil && status == 200 {
			if _, err := forgejo.ParseUser(body); err == nil {
				return nil
			}
		}
		if err == nil && status >= 400 && status < 500 {
			return code("AUTH_FAILED")
		}
		select {
		case <-readyCtx.Done():
			return code("AUTH_FAILED")
		case <-time.After(time.Second):
		}
	}
}

func forgejoUnauthenticatedProbe(ctx context.Context, client runtimeClient, run state.Run, phase string) error {
	status, _, err := requestForgejoAPI(ctx, client, run, phase, forgejo.UserEndpoint(), nil)
	if err != nil {
		return code("AUTH_FAILED")
	}
	if status != 401 {
		if status >= 200 && status < 300 {
			return code("AUTH_BYPASS")
		}
		return code("AUTH_FAILED")
	}
	return nil
}
