package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type projectAPIModel struct {
	ID          string `json:"projectID"`
	Name        string `json:"projectName"`
	Description string `json:"description"`
}

type projectModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

type projectsDataSourceModel struct {
	Projects []projectModel `tfsdk:"projects"`
}

type deploymentAPIModel struct {
	CreateAt     string `json:"createAt"`
	ID           string `json:"deploymentID"`
	Name         string `json:"deploymentName"`
	Type         string `json:"deploymentType"`
	ProjectName  string `json:"projectName"`
	Platform     string `json:"platform"`
	Region       string `json:"region"`
	Status       string `json:"status"`
	Version      string `json:"version"`
	Connections  int64  `json:"connections"`
	Transactions int64  `json:"tps"`
}

type deploymentModel struct {
	CreateAt     types.String `tfsdk:"created_at"`
	ID           types.String `tfsdk:"deployment_id"`
	Name         types.String `tfsdk:"name"`
	Type         types.String `tfsdk:"deployment_type"`
	ProjectName  types.String `tfsdk:"project_name"`
	Platform     types.String `tfsdk:"platform"`
	Region       types.String `tfsdk:"region"`
	Status       types.String `tfsdk:"status"`
	Version      types.String `tfsdk:"version"`
	Connections  types.Int64  `tfsdk:"connections"`
	Transactions types.Int64  `tfsdk:"tps"`
}

type deploymentDetailModel struct {
	CreateAt     types.String `tfsdk:"created_at"`
	ID           types.String `tfsdk:"deployment_id"`
	Name         types.String `tfsdk:"name"`
	Type         types.String `tfsdk:"deployment_type"`
	Platform     types.String `tfsdk:"platform"`
	Region       types.String `tfsdk:"region"`
	Status       types.String `tfsdk:"status"`
	Connections  types.Int64  `tfsdk:"connections"`
	Transactions types.Int64  `tfsdk:"tps"`
}

type deploymentsDataSourceModel struct {
	Deployments []deploymentModel `tfsdk:"deployments"`
}

type deploymentDataSource struct {
	platform *client.Client
}

type deploymentsDataSource struct {
	platform *client.Client
}

type projectsDataSource struct {
	platform *client.Client
}

func newProjectsDataSource() datasource.DataSource {
	return &projectsDataSource{}
}

func newDeploymentsDataSource() datasource.DataSource {
	return &deploymentsDataSource{}
}

func newDeploymentDataSource() datasource.DataSource {
	return &deploymentDataSource{}
}

func (d *projectsDataSource) Metadata(
	_ context.Context,
	request datasource.MetadataRequest,
	response *datasource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_projects"
}

func (d *projectsDataSource) Schema(
	_ context.Context,
	_ datasource.SchemaRequest,
	response *datasource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "List projects available to the configured Platform API key.",
		Attributes: map[string]schema.Attribute{
			"projects": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Projects visible to the configured Platform API key.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: projectAttributes(),
				},
			},
		},
	}
}

func (d *projectsDataSource) Configure(
	_ context.Context,
	request datasource.ConfigureRequest,
	response *datasource.ConfigureResponse,
) {
	d.platform = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (d *projectsDataSource) Read(
	ctx context.Context,
	_ datasource.ReadRequest,
	response *datasource.ReadResponse,
) {
	if !requirePlatform(d.platform, &response.Diagnostics) {
		return
	}

	var projects []projectAPIModel
	_, err := d.platform.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   "/projects",
		Result: &projects,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to list Platform projects", err.Error())
		return
	}
	sort.Slice(projects, func(left, right int) bool {
		return projects[left].ID < projects[right].ID
	})

	state := projectsDataSourceModel{
		Projects: make([]projectModel, 0, len(projects)),
	}
	for _, project := range projects {
		state.Projects = append(state.Projects, projectModel{
			ID:          types.StringValue(project.ID),
			Name:        types.StringValue(project.Name),
			Description: types.StringValue(project.Description),
		})
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (d *deploymentsDataSource) Metadata(
	_ context.Context,
	request datasource.MetadataRequest,
	response *datasource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_deployments"
}

func (d *deploymentsDataSource) Schema(
	_ context.Context,
	_ datasource.SchemaRequest,
	response *datasource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "List deployments available to the configured Platform API key.",
		Attributes: map[string]schema.Attribute{
			"deployments": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Deployments visible to the configured Platform API key.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: deploymentAttributes(false, true),
				},
			},
		},
	}
}

func (d *deploymentsDataSource) Configure(
	_ context.Context,
	request datasource.ConfigureRequest,
	response *datasource.ConfigureResponse,
) {
	d.platform = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (d *deploymentsDataSource) Read(
	ctx context.Context,
	_ datasource.ReadRequest,
	response *datasource.ReadResponse,
) {
	if !requirePlatform(d.platform, &response.Diagnostics) {
		return
	}

	var deployments []deploymentAPIModel
	_, err := d.platform.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   "/deployments",
		Result: &deployments,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to list Platform deployments", err.Error())
		return
	}
	sort.Slice(deployments, func(left, right int) bool {
		return deployments[left].ID < deployments[right].ID
	})

	state := deploymentsDataSourceModel{
		Deployments: make([]deploymentModel, 0, len(deployments)),
	}
	for _, deployment := range deployments {
		state.Deployments = append(state.Deployments, terraformDeployment(deployment))
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (d *deploymentDataSource) Metadata(
	_ context.Context,
	request datasource.MetadataRequest,
	response *datasource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_deployment"
}

func (d *deploymentDataSource) Schema(
	_ context.Context,
	_ datasource.SchemaRequest,
	response *datasource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "Read one Platform deployment.",
		Attributes:  deploymentAttributes(true, false),
	}
}

func (d *deploymentDataSource) Configure(
	_ context.Context,
	request datasource.ConfigureRequest,
	response *datasource.ConfigureResponse,
) {
	d.platform = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (d *deploymentDataSource) Read(
	ctx context.Context,
	request datasource.ReadRequest,
	response *datasource.ReadResponse,
) {
	if !requirePlatform(d.platform, &response.Diagnostics) {
		return
	}

	var config deploymentDetailModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	var deployment deploymentAPIModel
	_, err := d.platform.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   "/deployments/" + client.EscapePathSegment(config.ID.ValueString()),
		Result: &deployment,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to read Platform deployment", err.Error())
		return
	}
	state := terraformDeploymentDetail(deployment)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func projectAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "Project identifier.",
		},
		"name": schema.StringAttribute{
			Computed:    true,
			Description: "Project name.",
		},
		"description": schema.StringAttribute{
			Computed:    true,
			Description: "Project description.",
		},
	}
}

func deploymentAttributes(idRequired bool, includeListFields bool) map[string]schema.Attribute {
	attributes := map[string]schema.Attribute{
		"deployment_id": schema.StringAttribute{
			Computed:    !idRequired,
			Required:    idRequired,
			Description: "Deployment identifier.",
		},
		"created_at": schema.StringAttribute{
			Computed:    true,
			Description: "Deployment creation time returned by the Platform API.",
		},
		"name": schema.StringAttribute{
			Computed:    true,
			Description: "Deployment name.",
		},
		"deployment_type": schema.StringAttribute{
			Computed:    true,
			Description: "Deployment type.",
		},
		"platform": schema.StringAttribute{
			Computed:    true,
			Description: "Cloud platform hosting the deployment.",
		},
		"region": schema.StringAttribute{
			Computed:    true,
			Description: "Cloud region hosting the deployment.",
		},
		"status": schema.StringAttribute{
			Computed:    true,
			Description: "Current deployment status.",
		},
		"connections": schema.Int64Attribute{
			Computed:    true,
			Description: "Purchased concurrent connection capacity.",
		},
		"tps": schema.Int64Attribute{
			Computed:    true,
			Description: "Purchased transactions-per-second capacity.",
		},
	}
	if includeListFields {
		attributes["project_name"] = schema.StringAttribute{
			Computed:    true,
			Description: "Name of the project containing the deployment.",
		}
		attributes["version"] = schema.StringAttribute{
			Computed:    true,
			Description: "EMQX major version reported by the Platform API.",
		}
	}
	return attributes
}

func terraformDeployment(deployment deploymentAPIModel) deploymentModel {
	return deploymentModel{
		CreateAt:     types.StringValue(deployment.CreateAt),
		ID:           types.StringValue(deployment.ID),
		Name:         types.StringValue(deployment.Name),
		Type:         types.StringValue(deployment.Type),
		ProjectName:  types.StringValue(deployment.ProjectName),
		Platform:     types.StringValue(deployment.Platform),
		Region:       types.StringValue(deployment.Region),
		Status:       types.StringValue(deployment.Status),
		Version:      types.StringValue(deployment.Version),
		Connections:  types.Int64Value(deployment.Connections),
		Transactions: types.Int64Value(deployment.Transactions),
	}
}

func terraformDeploymentDetail(deployment deploymentAPIModel) deploymentDetailModel {
	return deploymentDetailModel{
		CreateAt:     types.StringValue(deployment.CreateAt),
		ID:           types.StringValue(deployment.ID),
		Name:         types.StringValue(deployment.Name),
		Type:         types.StringValue(deployment.Type),
		Platform:     types.StringValue(deployment.Platform),
		Region:       types.StringValue(deployment.Region),
		Status:       types.StringValue(deployment.Status),
		Connections:  types.Int64Value(deployment.Connections),
		Transactions: types.Int64Value(deployment.Transactions),
	}
}

func platformClientFromProviderData(data any, diagnostics interface {
	AddError(summary string, detail string)
}) *client.Client {
	if data == nil {
		return nil
	}
	providerData, ok := data.(*ProviderData)
	if !ok {
		diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("Expected *ProviderData, got %T.", data),
		)
		return nil
	}
	return providerData.Platform
}

func requirePlatform(platform *client.Client, diagnostics interface {
	AddError(summary string, detail string)
}) bool {
	if platform != nil {
		return true
	}
	diagnostics.AddError(
		"Platform API is not configured",
		"Configure platform_endpoint, platform_api_key, and platform_api_secret.",
	)
	return false
}
