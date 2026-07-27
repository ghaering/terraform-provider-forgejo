package provider_test

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

/*
 * Out-of-band drift tests. Delete an object behind Terraform's back and the
 * next plan has to create it again. Every test here has that shape.
 */

// testClient returns a Forgejo client authenticated as the acceptance test
// admin, for changes made behind Terraform's back.
func testClient(t *testing.T) *forgejo.Client {
	t.Helper()

	client, err := forgejo.NewClient(forgejoTestHost, forgejo.SetToken(os.Getenv("FORGEJO_API_TOKEN")))
	if err != nil {
		t.Fatalf("creating Forgejo client: %s", err)
	}

	return client
}

// deleted fails the test if the out-of-band deletion did not work. Without it
// a drift test passes for the wrong reason: nothing was deleted.
func deleted(t *testing.T, what string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("deleting %s out of band: %s", what, err)
	}
}

// driftTest applies config, deletes the object behind Terraform's back and
// applies the same config again, expecting a create in the plan.
func driftTest(t *testing.T, resourceName, config string, externals map[string]resource.ExternalProvider, deleteOutOfBand func(*testing.T)) {
	t.Helper()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		ExternalProviders:        externals,
		Steps: []resource.TestStep{
			// Create the object.
			{
				Config: config,
			},
			// Delete it behind Terraform's back. The refresh has to drop it
			// from state, so the plan creates it again.
			{
				PreConfig: func() { deleteOutOfBand(t) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
					},
				},
			},
		},
	})
}

// tlsKey generates a key pair for the tests that need key material. It stays
// in state, so the second apply reuses it.
const tlsKey = `
resource "tls_private_key" "test" {
	algorithm = "ED25519"
}`

// tlsProvider is the external provider tlsKey needs.
var tlsProvider = map[string]resource.ExternalProvider{
	"tls": {Source: "hashicorp/tls"},
}

// driftTeamID looks up a team by name. Deleting a team out of band needs its
// numeric id.
func driftTeamID(t *testing.T, client *forgejo.Client, org, name string) int64 {
	t.Helper()

	teams, _, err := client.ListOrgTeams(org, forgejo.ListTeamsOptions{})
	if err != nil {
		t.Fatalf("listing teams of %s: %s", org, err)
	}

	for _, tm := range teams {
		if tm.Name == name {
			return tm.ID
		}
	}
	t.Fatalf("team %s not found in organization %s", name, org)

	return 0
}

func TestAccOrganizationResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_organization" "test" {
	name = "drift_org"
}`

	driftTest(t, "forgejo_organization.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteOrg("drift_org")
		deleted(t, "organization drift_org", err)
	})
}

func TestAccRepositoryResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_repo"
}`

	driftTest(t, "forgejo_repository.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteRepo(forgejoTestUser, "drift_repo")
		deleted(t, "repository drift_repo", err)
	})
}

func TestAccUserResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_user" "test" {
	login    = "drift_user"
	email    = "drift_user@localhost.localdomain"
	password = "P@s$w0rd!"
}`

	driftTest(t, "forgejo_user.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).AdminDeleteUser("drift_user")
		deleted(t, "user drift_user", err)
	})
}

func TestAccTeamResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_organization" "test" {
	name = "drift_team_org"
}
resource "forgejo_team" "test" {
	name            = "drift_team"
	organization_id = forgejo_organization.test.id
	permission      = "read"

	units_map = {
		"repo.code" = "read"
	}
}`

	driftTest(t, "forgejo_team.test", config, nil, func(t *testing.T) {
		client := testClient(t)
		_, err := client.DeleteTeam(driftTeamID(t, client, "drift_team_org", "drift_team"))
		deleted(t, "team drift_team", err)
	})
}

func TestAccTeamMemberResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_organization" "test" {
	name = "drift_member_org"
}
resource "forgejo_team" "test" {
	name                      = "drift_member_team"
	organization_id           = forgejo_organization.test.id
	includes_all_repositories = true
	permission                = "read"

	units_map = {
		"repo.code" = "read"
	}
}
resource "forgejo_user" "test" {
	login    = "drift_member"
	email    = "drift_member@localhost.localdomain"
	password = "P@s$w0rd!"
}
resource "forgejo_team_member" "test" {
	team_id = forgejo_team.test.id
	user    = forgejo_user.test.login
}`

	driftTest(t, "forgejo_team_member.test", config, nil, func(t *testing.T) {
		client := testClient(t)
		teamID := driftTeamID(t, client, "drift_member_org", "drift_member_team")
		_, err := client.RemoveTeamMember(teamID, "drift_member")
		deleted(t, "membership of drift_member", err)
	})
}

func TestAccCollaboratorResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_collab_repo"
}
resource "forgejo_user" "test" {
	login    = "drift_collab"
	email    = "drift_collab@localhost.localdomain"
	password = "P@s$w0rd!"
}
resource "forgejo_collaborator" "test" {
	repository_id = forgejo_repository.test.id
	user          = forgejo_user.test.login
	permission    = "write"
}`

	// The repository is public, so the removed collaborator still has an
	// effective 'read' permission. Reading that instead of the membership is
	// exactly what used to hide this drift.
	driftTest(t, "forgejo_collaborator.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteCollaborator(forgejoTestUser, "drift_collab_repo", "drift_collab")
		deleted(t, "collaborator drift_collab", err)
	})
}

func TestAccRepositoryWebhookResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_hook_repo"
}
resource "forgejo_repository_webhook" "test" {
	repository_id = forgejo_repository.test.id
	type          = "forgejo"
	events        = ["push"]
	config        = {
		"content_type" = "json"
		"url"          = "http://example.com/drift"
	}
}`

	driftTest(t, "forgejo_repository_webhook.test", config, nil, func(t *testing.T) {
		client := testClient(t)

		// The repository has exactly one webhook, the one under test.
		hooks, _, err := client.ListRepoHooks(forgejoTestUser, "drift_hook_repo", forgejo.ListHooksOptions{})
		if err != nil {
			t.Fatalf("listing webhooks of drift_hook_repo: %s", err)
		}
		if len(hooks) != 1 {
			t.Fatalf("expected 1 webhook on drift_hook_repo, got %d", len(hooks))
		}

		_, err = client.DeleteRepoHook(forgejoTestUser, "drift_hook_repo", hooks[0].ID)
		deleted(t, "webhook of drift_hook_repo", err)
	})
}

func TestAccBranchProtectionResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_protection_repo"
}
resource "forgejo_branch_protection" "test" {
	branch_name   = "main"
	repository_id = forgejo_repository.test.id
}`

	driftTest(t, "forgejo_branch_protection.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteBranchProtection(forgejoTestUser, "drift_protection_repo", "main")
		deleted(t, "branch protection of drift_protection_repo", err)
	})
}

func TestAccDeployKeyResourceDrift(t *testing.T) {
	const config = providerConfig + tlsKey + `
resource "forgejo_repository" "test" {
	name = "drift_key_repo"
}
resource "forgejo_deploy_key" "test" {
	repository_id = forgejo_repository.test.id
	key           = trimspace(tls_private_key.test.public_key_openssh)
	title         = "drift_deploy_key"
	read_only     = false
}`

	driftTest(t, "forgejo_deploy_key.test", config, tlsProvider, func(t *testing.T) {
		client := testClient(t)

		keys, _, err := client.ListDeployKeys(forgejoTestUser, "drift_key_repo", forgejo.ListDeployKeysOptions{})
		if err != nil {
			t.Fatalf("listing deploy keys of drift_key_repo: %s", err)
		}

		for _, key := range keys {
			if key.Title != "drift_deploy_key" {
				continue
			}

			_, err = client.DeleteDeployKey(forgejoTestUser, "drift_key_repo", key.ID)
			deleted(t, "deploy key drift_deploy_key", err)

			return
		}
		t.Fatal("deploy key drift_deploy_key not found")
	})
}

func TestAccSSHKeyResourceDrift(t *testing.T) {
	const config = providerConfig + tlsKey + `
resource "forgejo_ssh_key" "test" {
	user  = "` + forgejoTestUser + `"
	key   = trimspace(tls_private_key.test.public_key_openssh)
	title = "drift_ssh_key"
}`

	driftTest(t, "forgejo_ssh_key.test", config, tlsProvider, func(t *testing.T) {
		client := testClient(t)

		keys, _, err := client.ListPublicKeys(forgejoTestUser, forgejo.ListPublicKeysOptions{})
		if err != nil {
			t.Fatalf("listing SSH keys of %s: %s", forgejoTestUser, err)
		}

		for _, key := range keys {
			if key.Title != "drift_ssh_key" {
				continue
			}

			_, err = client.DeletePublicKey(key.ID)
			deleted(t, "SSH key drift_ssh_key", err)

			return
		}
		t.Fatal("SSH key drift_ssh_key not found")
	})
}

func TestAccOrganizationActionSecretResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_organization" "test" {
	name = "drift_secret_org"
}
resource "forgejo_organization_action_secret" "test" {
	organization_id = forgejo_organization.test.id
	name            = "drift_secret"
	data            = "drift_secret_value"
}`

	driftTest(t, "forgejo_organization_action_secret.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteOrgActionSecret("drift_secret_org", "drift_secret")
		deleted(t, "organization action secret drift_secret", err)
	})
}

func TestAccRepositoryActionSecretResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_secret_repo"
}
resource "forgejo_repository_action_secret" "test" {
	repository_id = forgejo_repository.test.id
	name          = "drift_secret"
	data          = "drift_secret_value"
}`

	driftTest(t, "forgejo_repository_action_secret.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteRepoActionSecret(forgejoTestUser, "drift_secret_repo", "drift_secret")
		deleted(t, "repository action secret drift_secret", err)
	})
}

func TestAccOrganizationActionVariableResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_organization" "test" {
	name = "drift_variable_org"
}
resource "forgejo_organization_action_variable" "test" {
	organization_id = forgejo_organization.test.id
	name            = "drift_variable"
	data            = "drift_variable_value"
}`

	driftTest(t, "forgejo_organization_action_variable.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteOrgActionVariable("drift_variable_org", "drift_variable")
		deleted(t, "organization action variable drift_variable", err)
	})
}

func TestAccRepositoryActionVariableResourceDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_variable_repo"
}
resource "forgejo_repository_action_variable" "test" {
	repository_id = forgejo_repository.test.id
	name          = "drift_variable"
	data          = "drift_variable_value"
}`

	driftTest(t, "forgejo_repository_action_variable.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteRepoActionVariable(forgejoTestUser, "drift_variable_repo", "drift_variable")
		deleted(t, "repository action variable drift_variable", err)
	})
}

/*
 * Parent drift: deleting the repository takes its dependent objects with it.
 * The dependent resource has to drop out of state too, rather than failing the
 * refresh on a repository that is no longer there.
 */
func TestAccDependentResourceParentDrift(t *testing.T) {
	const config = providerConfig + `
resource "forgejo_repository" "test" {
	name = "drift_parent_repo"
}
resource "forgejo_repository_action_variable" "test" {
	repository_id = forgejo_repository.test.id
	name          = "drift_variable"
	data          = "drift_variable_value"
}`

	driftTest(t, "forgejo_repository_action_variable.test", config, nil, func(t *testing.T) {
		_, err := testClient(t).DeleteRepo(forgejoTestUser, "drift_parent_repo")
		deleted(t, "repository drift_parent_repo", err)
	})
}
