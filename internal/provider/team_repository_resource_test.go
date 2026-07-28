package provider_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// teamRepositoryTestOrg is the organization all steps below share.
const teamRepositoryTestOrg = "team_repo_test_org"

// teamRepositoryTestConfig is the organization, teams and repositories every
// step attaches to. Kept in one place - a step that drops it would destroy the
// objects under test.
//
// includes_all_repositories is false on purpose. Such a team has every
// repository of the organization already, so the checks below would pass
// without the resource doing anything.
const teamRepositoryTestConfig = `
resource "forgejo_organization" "test" {
	name = "` + teamRepositoryTestOrg + `"
}
resource "forgejo_team" "test" {
	name                      = "test_team"
	organization_id           = forgejo_organization.test.id
	can_create_org_repo       = false
	includes_all_repositories = false
	permission                = "write"
	units_map                 = {
		"repo.code" = "write"
	}
}
resource "forgejo_team" "test2" {
	name                      = "second_test_team"
	organization_id           = forgejo_organization.test.id
	can_create_org_repo       = false
	includes_all_repositories = false
	permission                = "write"
	units_map                 = {
		"repo.code" = "write"
	}
}
resource "forgejo_repository" "test" {
	owner = forgejo_organization.test.name
	name  = "test_repo"
}
resource "forgejo_repository" "test2" {
	owner = forgejo_organization.test.name
	name  = "second_test_repo"
}
`

// wrapped makes an expected diagnostic survive the line wrapping Terraform
// applies to error output. Spaces become '\s+', the rest stays a regexp.
func wrapped(pattern string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(pattern, " ", `\s+`))
}

// TestAccTeamRepositoryResource walks one assignment through create, import,
// recreate and delete, plus the ways create and import fail. One step is worth
// naming: an assignment removed outside of Terraform has to come back in the
// next plan, not read as no changes.
func TestAccTeamRepositoryResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing (non-existent team)
			{
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = 1010
	organization = forgejo_organization.test.name
	repository   = forgejo_repository.test.name
}`,
				ExpectError: wrapped(
					"Either repository '" + teamRepositoryTestOrg + "/test_repo' or team with ID 1010 not found",
				),
			},
			// Create and Read testing (non-existent repository)
			{
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = forgejo_team.test.id
	organization = forgejo_organization.test.name
	repository   = "non-existing-repo"
}`,
				ExpectError: wrapped(
					"Either repository '" + teamRepositoryTestOrg + "/non-existing-repo' or team with ID [0-9]+ not found",
				),
			},
			// Create and Read testing
			{
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = forgejo_team.test.id
	organization = forgejo_organization.test.name
	repository   = forgejo_repository.test.name
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("forgejo_team_repository.test", plancheck.ResourceActionCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs("forgejo_team_repository.test", tfjsonpath.New("team_id"), "forgejo_team.test", tfjsonpath.New("id"), compare.ValuesSame()),
					statecheck.ExpectKnownValue("forgejo_team_repository.test", tfjsonpath.New("organization"), knownvalue.StringExact(teamRepositoryTestOrg)),
					statecheck.ExpectKnownValue("forgejo_team_repository.test", tfjsonpath.New("repository"), knownvalue.StringExact("test_repo")),
				},
			},
			// Import testing (malformed identifier)
			{
				ResourceName:  "forgejo_team_repository.test",
				ImportState:   true,
				ImportStateId: teamRepositoryTestOrg + "/test_team",
				ExpectError:   wrapped("Expected import identifier with format: 'org/team/repo'"),
			},
			// Import testing (non-existent team)
			{
				ResourceName:  "forgejo_team_repository.test",
				ImportState:   true,
				ImportStateId: teamRepositoryTestOrg + "/non-existent/test_repo",
				ExpectError:   wrapped("Team with name 'non-existent' not found"),
			},
			// Import testing (repository not assigned to the team)
			{
				ResourceName:  "forgejo_team_repository.test",
				ImportState:   true,
				ImportStateId: teamRepositoryTestOrg + "/second_test_team/test_repo",
				ExpectError:   wrapped("Cannot import non-existent remote object"),
			},
			// Import testing
			{
				ResourceName:      "forgejo_team_repository.test",
				ImportState:       true,
				ImportStateId:     teamRepositoryTestOrg + "/test_team/test_repo",
				ImportStateVerify: true,
				// The resource has no 'id' attribute, its key is the
				// team_id/organization/repository triple.
				ImportStateVerifyIdentifierAttribute: "repository",
			},
			// Read testing (assignment removed outside of Terraform)
			{
				PreConfig: func() {
					deleteTeamRepositoryOutOfBand(t, teamRepositoryTestOrg, "test_team", "test_repo")
				},
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = forgejo_team.test.id
	organization = forgejo_organization.test.name
	repository   = forgejo_repository.test.name
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("forgejo_team_repository.test", plancheck.ResourceActionCreate),
					},
				},
			},
			// Recreate and Read testing (updating any value recreates the resource -- repository)
			{
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = forgejo_team.test.id
	organization = forgejo_organization.test.name
	repository   = forgejo_repository.test2.name
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("forgejo_team_repository.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs("forgejo_team_repository.test", tfjsonpath.New("team_id"), "forgejo_team.test", tfjsonpath.New("id"), compare.ValuesSame()),
					statecheck.ExpectKnownValue("forgejo_team_repository.test", tfjsonpath.New("repository"), knownvalue.StringExact("second_test_repo")),
				},
			},
			// Recreate and Read testing (updating any value recreates the resource -- team)
			{
				Config: providerConfig + teamRepositoryTestConfig + `
resource "forgejo_team_repository" "test" {
	team_id      = forgejo_team.test2.id
	organization = forgejo_organization.test.name
	repository   = forgejo_repository.test2.name
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("forgejo_team_repository.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs("forgejo_team_repository.test", tfjsonpath.New("team_id"), "forgejo_team.test2", tfjsonpath.New("id"), compare.ValuesSame()),
					statecheck.ExpectKnownValue("forgejo_team_repository.test", tfjsonpath.New("repository"), knownvalue.StringExact("second_test_repo")),
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// deleteTeamRepositoryOutOfBand removes a repository from a team behind
// Terraform's back, so the next refresh has to notice it is gone.
func deleteTeamRepositoryOutOfBand(t *testing.T, org, team, repo string) {
	t.Helper()

	client, err := forgejo.NewClient(forgejoTestHost, forgejo.SetToken(os.Getenv("FORGEJO_API_TOKEN")))
	if err != nil {
		t.Fatalf("creating Forgejo client: %s", err)
	}

	// Look up the team by name, only its numeric id can be deleted against.
	teams, _, err := client.ListOrgTeams(org, forgejo.ListTeamsOptions{})
	if err != nil {
		t.Fatalf("listing teams of %s: %s", org, err)
	}

	var teamID int64
	for _, tm := range teams {
		if tm.Name == team {
			teamID = tm.ID

			break
		}
	}
	if teamID == 0 {
		t.Fatalf("team %s not found in organization %s", team, org)
	}

	// Remove the repository from the team.
	if _, err := client.RemoveTeamRepository(teamID, org, repo); err != nil {
		t.Fatalf("removing %s/%s from team %d: %s", org, repo, teamID, err)
	}
}
