package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &teamRepositoryResource{}
	_ resource.ResourceWithConfigure   = &teamRepositoryResource{}
	_ resource.ResourceWithImportState = &teamRepositoryResource{}
)

// teamRepositoryResource is the resource implementation.
type teamRepositoryResource struct {
	client *forgejo.Client
}

// teamRepositoryResourceModel maps the resource schema data.
type teamRepositoryResourceModel struct {
	TeamID       types.Int64  `tfsdk:"team_id"`
	Organization types.String `tfsdk:"organization"`
	Repository   types.String `tfsdk:"repository"`
}

// Metadata returns the resource type name.
func (r *teamRepositoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_repository"
}

// Schema defines the schema for the resource.
func (r *teamRepositoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Forgejo team repository resource. Grants a team access to a single repository of its organization.",

		Attributes: map[string]schema.Attribute{
			"team_id": schema.Int64Attribute{
				Description: "Numeric identifier of the team. Changing this forces a new resource to be created.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"organization": schema.StringAttribute{
				Description: "Name of the organization that owns the repository. Changing this forces a new resource to be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"repository": schema.StringAttribute{
				Description: "Name of the repository. Changing this forces a new resource to be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *teamRepositoryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*forgejo.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf(
				"Expected *forgejo.Client, got: %T. Please report this issue to the provider developers.",
				req.ProviderData,
			),
		)

		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *teamRepositoryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer un(trace(ctx, "Create team repository resource"))

	var data teamRepositoryResourceModel

	// Read Terraform plan data into model
	diags := req.Plan.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Use Forgejo client to add a repository to a team
	diags = setTeamRepository(
		ctx,
		r.client,
		data.TeamID.ValueInt64(),
		data.Organization.ValueString(),
		data.Repository.ValueString(),
	)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save data into Terraform state
	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *teamRepositoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer un(trace(ctx, "Read team repository resource"))

	var data teamRepositoryResourceModel

	// Read Terraform prior state into the model
	diags := req.State.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Use Forgejo client to check whether the repository is still assigned
	found, diags := checkTeamRepository(
		ctx,
		r.client,
		data.TeamID.ValueInt64(),
		data.Organization.ValueString(),
		data.Repository.ValueString(),
	)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The assignment is gone. Drop it from state so Terraform plans a new one
	// instead of reporting no changes.
	if !found {
		resp.State.RemoveResource(ctx)

		return
	}

	// Save data into Terraform state
	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *teamRepositoryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer un(trace(ctx, "Update team repository resource"))

	/*
	 * Team repositories can not be updated in-place. All writable attributes
	 * have 'RequiresReplace' plan modifier set.
	 */
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *teamRepositoryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	defer un(trace(ctx, "Delete team repository resource"))

	var data teamRepositoryResourceModel

	// Read Terraform prior state into the model.
	diags := req.State.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Use Forgejo client to remove a repository from a team
	diags = deleteTeamRepository(
		ctx,
		r.client,
		data.TeamID.ValueInt64(),
		data.Organization.ValueString(),
		data.Repository.ValueString(),
	)
	resp.Diagnostics.Append(diags...)
}

// ImportState reads an existing resource and adds it to Terraform state on success.
func (r *teamRepositoryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	defer un(trace(ctx, "Import team repository resource"))

	var state teamRepositoryResourceModel

	// Parse import identifier
	cmp := strings.Split(req.ID, "/")
	if len(cmp) != 3 {
		resp.Diagnostics.AddError(
			"Unable to parse import identifier",
			fmt.Sprintf(
				"Expected import identifier with format: 'org/team/repo', got: '%s'",
				req.ID,
			),
		)

		return
	}
	orgName, teamName, repoName := cmp[0], cmp[1], cmp[2]

	// The import identifier names the team, the resource stores its numeric id.
	team, diags := getOrgTeamByName(
		ctx,
		r.client,
		orgName,
		teamName,
	)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.TeamID = types.Int64Value(team.ID)
	state.Organization = types.StringValue(orgName)
	state.Repository = types.StringValue(repoName)

	// Save data into Terraform state. Read runs next and errors out if the
	// repository is not actually assigned to the team.
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// NewTeamRepositoryResource is a helper function to simplify the provider implementation.
func NewTeamRepositoryResource() resource.Resource {
	return &teamRepositoryResource{}
}

// setTeamRepository is a helper function to add a repository to a team.
func setTeamRepository(ctx context.Context, client *forgejo.Client, teamID int64, orgName, repoName string) diag.Diagnostics {
	var diags diag.Diagnostics

	tflog.Info(ctx, "Create team repository", map[string]any{
		"team_id":      teamID,
		"organization": orgName,
		"repository":   repoName,
	})

	// Use Forgejo client to add a repository to a team
	res, err := client.AddTeamRepository(teamID, orgName, repoName)
	if err == nil {
		return diags
	}

	// Handle errors
	diags.AddError(
		"Unable to create team repository",
		teamRepositoryErrorMessage(ctx, res, err, teamID, orgName, repoName),
	)

	return diags
}

// deleteTeamRepository is a helper function to remove a repository from a team.
func deleteTeamRepository(ctx context.Context, client *forgejo.Client, teamID int64, orgName, repoName string) diag.Diagnostics {
	var diags diag.Diagnostics

	tflog.Info(ctx, "Delete team repository", map[string]any{
		"team_id":      teamID,
		"organization": orgName,
		"repository":   repoName,
	})

	// Use Forgejo client to remove a repository from a team
	res, err := client.RemoveTeamRepository(teamID, orgName, repoName)
	if err == nil {
		return diags
	}

	// Already gone, nothing to delete. The 404 covers a missing team, a
	// missing repository and an assignment removed elsewhere alike.
	if res != nil && res.StatusCode == http.StatusNotFound {
		return diags
	}

	// Handle errors
	diags.AddError(
		"Unable to delete team repository",
		teamRepositoryErrorMessage(ctx, res, err, teamID, orgName, repoName),
	)

	return diags
}

// checkTeamRepository is a helper function to report whether a repository is
// assigned to a team. There is no API to read a single assignment, so the
// team's repositories are listed and searched.
func checkTeamRepository(ctx context.Context, client *forgejo.Client, teamID int64, orgName, repoName string) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	tflog.Info(ctx, "Read team repository", map[string]any{
		"team_id":      teamID,
		"organization": orgName,
		"repository":   repoName,
	})

	// Page through all repositories of the team. ListOptions{Page: -1} does NOT
	// return every repository - it sends the server's default first page (30
	// repositories), so a team with more than that would never find one past
	// the first page. Walk the pages explicitly instead.
	const pageSize = 50
	for page := 1; ; page++ {
		repos, res, err := client.ListTeamRepositories(
			teamID,
			forgejo.ListTeamRepositoriesOptions{
				ListOptions: forgejo.ListOptions{
					Page:     page,
					PageSize: pageSize,
				},
			},
		)
		if err != nil {
			diags.AddError(
				"Unable to list team repositories",
				teamRepositoryErrorMessage(ctx, res, err, teamID, orgName, repoName),
			)

			return false, diags
		}

		// Search this page for the repository. Names are case-insensitive in
		// Forgejo, and the owner is checked too so that a team_id pointing at
		// another organization's team does not silently match.
		for _, repo := range repos {
			if !strings.EqualFold(repo.Name, repoName) {
				continue
			}
			if repo.Owner != nil && !strings.EqualFold(repo.Owner.UserName, orgName) {
				continue
			}

			return true, diags
		}

		// Only an empty page proves this was the last page. The server caps
		// the page size at MAX_RESPONSE_ITEMS, so a short page is no proof.
		if len(repos) == 0 {
			return false, diags
		}
	}
}

// teamRepositoryErrorMessage is a helper function to turn an API error into a
// message. All three calls fail the same way, so they share this.
func teamRepositoryErrorMessage(ctx context.Context, res *forgejo.Response, err error, teamID int64, orgName, repoName string) string {
	if res == nil {
		return fmt.Sprintf("Unknown error with nil response: %s", err)
	}

	tflog.Error(ctx, "Error", map[string]any{
		"status": res.Status,
	})

	switch res.StatusCode {
	case 403:
		return fmt.Sprintf(
			"Repository '%s/%s' in team with ID %d forbidden: %s",
			orgName,
			repoName,
			teamID,
			err,
		)
	case 404:
		return fmt.Sprintf(
			"Either repository '%s/%s' or team with ID %d not found: %s",
			orgName,
			repoName,
			teamID,
			err,
		)
	default:
		return fmt.Sprintf(
			"Unknown error (status %d): %s",
			res.StatusCode,
			err,
		)
	}
}
