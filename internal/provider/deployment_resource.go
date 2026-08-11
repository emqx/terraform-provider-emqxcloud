package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	defaultDeploymentPollInterval  = time.Minute
	defaultDeploymentTimeout       = time.Hour
	defaultDeploymentCreateTimeout = 30 * time.Minute
)

type deploymentResource struct {
	platform     *client.Client
	pollInterval time.Duration
	timeout      time.Duration
}

type managedDeploymentModel struct {
	ProjectID    types.String `tfsdk:"project_id"`
	Platform     types.String `tfsdk:"platform"`
	Region       types.String `tfsdk:"region"`
	Connections  types.Int64  `tfsdk:"connections"`
	Transactions types.Int64  `tfsdk:"tps"`
	Name         types.String `tfsdk:"deployment_name"`
	Version      types.String `tfsdk:"deployment_version"`
	Status       types.String `tfsdk:"status"`
	ID           types.String `tfsdk:"deployment_id"`
	Type         types.String `tfsdk:"deployment_type"`
}

type createDeploymentRequest struct {
	ProjectID    string `json:"projectID"`
	Platform     string `json:"platform"`
	Region       string `json:"region"`
	Connections  int64  `json:"connections"`
	Transactions int64  `json:"tps"`
	Name         string `json:"deploymentName,omitempty"`
	Type         string `json:"deploymentType"`
	Version      string `json:"deploymentVersion"`
	FreeTrial    bool   `json:"freeTrial"`
}

func newDeploymentResource() resource.Resource {
	return &deploymentResource{
		pollInterval: defaultDeploymentPollInterval,
		timeout:      defaultDeploymentTimeout,
	}
}

func (r *deploymentResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_deployment"
}

func (r *deploymentResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	response.Schema = schema.Schema{
		Description: "Create and start or stop one Dedicated Flex deployment. Deletion is not supported.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				Required:    true,
				Description: "Identifier of the Platform project that owns the deployment.",
			},
			"platform": schema.StringAttribute{
				Required:    true,
				Description: "Cloud platform: aws, gcp, azure, aliyun, tencent, or huawei.",
			},
			"region": schema.StringAttribute{
				Required:    true,
				Description: "Cloud region for the Dedicated Flex deployment.",
			},
			"connections": schema.Int64Attribute{
				Required:    true,
				Description: "Concurrent connection capacity.",
			},
			"tps": schema.Int64Attribute{
				Required:    true,
				Description: "Transactions-per-second capacity.",
			},
			"deployment_name": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: useState,
				Description:   "Requested name, between 1 and 64 characters. Older Platform APIs may generate a different remote name.",
			},
			"deployment_version": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("v5"),
				Description: "EMQX major version: v5 or v6.",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("running"),
				Description: "Desired deployment status: running or stopped.",
			},
			"deployment_id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: useState,
				Description:   "Platform deployment identifier.",
			},
			"deployment_type": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: useState,
				Description:   "Deployment type returned by the Platform API; this resource creates dedicatedFlex only.",
			},
		},
	}
}

func (r *deploymentResource) Configure(
	_ context.Context,
	request resource.ConfigureRequest,
	response *resource.ConfigureResponse,
) {
	r.platform = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (r *deploymentResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	var config managedDeploymentModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	validateNonEmptyString(config.ProjectID, "project_id", &response.Diagnostics)
	validateStringChoice(
		config.Platform,
		"platform",
		[]string{"aws", "gcp", "azure", "aliyun", "tencent", "huawei"},
		&response.Diagnostics,
	)
	validateNonEmptyString(config.Region, "region", &response.Diagnostics)
	validateStringLength(config.Name, "deployment_name", 1, 64, &response.Diagnostics)
	validatePositiveInt64(config.Connections, "connections", &response.Diagnostics)
	validatePositiveInt64(config.Transactions, "tps", &response.Diagnostics)
	validateStringChoice(
		config.Version,
		"deployment_version",
		[]string{"v5", "v6"},
		&response.Diagnostics,
	)
	validateStringChoice(
		config.Status,
		"status",
		[]string{"running", "stopped"},
		&response.Diagnostics,
	)
}

func (r *deploymentResource) ModifyPlan(
	ctx context.Context,
	request resource.ModifyPlanRequest,
	response *resource.ModifyPlanResponse,
) {
	if request.Plan.Raw.IsNull() {
		response.Diagnostics.AddError(
			"Deployment deletion is not supported",
			"Remove the deployment from Terraform state without destroying it.",
		)
		return
	}

	var plan managedDeploymentModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	if request.State.Raw.IsNull() {
		if !plan.Status.IsUnknown() && plan.Status.ValueString() != "running" {
			response.Diagnostics.AddAttributeError(
				path.Root("status"),
				"Invalid initial deployment status",
				"A new deployment must start with status running.",
			)
		}
		return
	}

	var state managedDeploymentModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	changed := immutableDeploymentChanges(plan, state)
	if len(changed) > 0 {
		response.Diagnostics.AddError(
			"Deployment replacement is not supported",
			"These attributes cannot change after creation: "+strings.Join(changed, ", ")+".",
		)
	}
}

func (r *deploymentResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	var plan managedDeploymentModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}
	createCtx, cancel := context.WithTimeout(ctx, defaultDeploymentCreateTimeout)
	defer cancel()

	var created deploymentAPIModel
	_, err := r.platform.Do(createCtx, client.Request{
		Method:  http.MethodPost,
		Path:    "/deployments",
		Timeout: defaultDeploymentCreateTimeout,
		Body: createDeploymentRequest{
			ProjectID:    plan.ProjectID.ValueString(),
			Platform:     plan.Platform.ValueString(),
			Region:       plan.Region.ValueString(),
			Connections:  plan.Connections.ValueInt64(),
			Transactions: plan.Transactions.ValueInt64(),
			Name:         plan.Name.ValueString(),
			Type:         "dedicatedFlex",
			Version:      plan.Version.ValueString(),
			FreeTrial:    false,
		},
		Result: &created,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create deployment", err.Error())
		return
	}
	if created.ID == "" {
		response.Diagnostics.AddError(
			"Unable to create deployment",
			"Platform API response did not include deploymentID.",
		)
		return
	}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && created.Name != "" &&
		created.Name != plan.Name.ValueString() {
		response.Diagnostics.AddWarning(
			"Platform API returned a different deployment name",
			fmt.Sprintf(
				"Requested %q but the Platform API created %q; Terraform will retain the requested name.",
				plan.Name.ValueString(),
				created.Name,
			),
		)
	}

	updateManagedDeployment(&plan, created)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}

	current, err := r.waitForDeployment(createCtx, created.ID, "running")
	if err != nil {
		response.Diagnostics.AddError("Deployment creation did not complete", err.Error())
		return
	}
	updateManagedDeployment(&plan, current)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *deploymentResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	var state managedDeploymentModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	current, exists, err := r.readDeployment(ctx, state.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to read deployment", err.Error())
		return
	}
	if !exists {
		response.State.RemoveResource(ctx)
		return
	}
	updateManagedDeployment(&state, current)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *deploymentResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	var plan managedDeploymentModel
	var state managedDeploymentModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	deploymentID := state.ID.ValueString()
	desiredStatus := plan.Status.ValueString()
	currentStatus := state.Status.ValueString()

	// A transient status cannot accept start or stop, and it may settle on the status opposite
	// to the desired one, so wait for a stable status before deciding what to send.
	if isTransientDeploymentStatus(currentStatus) {
		settled, err := r.waitForStableDeployment(ctx, deploymentID)
		if err != nil {
			response.Diagnostics.AddError("Unable to change deployment status", err.Error())
			return
		}
		currentStatus = settled.Status
	}

	switch currentStatus {
	case desiredStatus:
	case "running", "stopped":
		_, err := r.platform.Do(ctx, client.Request{
			Method: http.MethodPost,
			Path: "/deployments/" + client.EscapePathSegment(deploymentID) +
				"/" + deploymentOperation(desiredStatus),
		})
		if err != nil {
			response.Diagnostics.AddError("Unable to change deployment status", err.Error())
			return
		}
	default:
		response.Diagnostics.AddError(
			"Unable to change deployment status",
			fmt.Sprintf("Deployment is in unsupported status %q.", currentStatus),
		)
		return
	}

	current, err := r.waitForDeployment(ctx, deploymentID, desiredStatus)
	if err != nil {
		response.Diagnostics.AddError("Deployment status change did not complete", err.Error())
		return
	}
	updateManagedDeployment(&plan, current)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *deploymentResource) Delete(
	_ context.Context,
	_ resource.DeleteRequest,
	response *resource.DeleteResponse,
) {
	response.Diagnostics.AddError(
		"Deployment deletion is not supported",
		"No Platform API request was sent. Remove the deployment from Terraform state without destroying it.",
	)
}

func (r *deploymentResource) readDeployment(
	ctx context.Context,
	deploymentID string,
) (deploymentAPIModel, bool, error) {
	var current deploymentAPIModel
	_, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   "/deployments/" + client.EscapePathSegment(deploymentID),
		Result: &current,
	})
	if client.IsStatus(err, http.StatusNotFound) {
		return deploymentAPIModel{}, false, nil
	}
	if err != nil {
		return deploymentAPIModel{}, false, err
	}
	return current, true, nil
}

// pollDeployment reads the deployment until settled reports true, it reports an error, or the
// configured timeout expires.
func (r *deploymentResource) pollDeployment(
	ctx context.Context,
	deploymentID string,
	settled func(deploymentAPIModel) (bool, error),
) (deploymentAPIModel, error) {
	timeoutContext, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	for {
		current, exists, err := r.readDeployment(timeoutContext, deploymentID)
		if err != nil {
			return deploymentAPIModel{}, err
		}
		if !exists {
			return deploymentAPIModel{}, errors.New("deployment no longer exists")
		}
		done, err := settled(current)
		if err != nil {
			return deploymentAPIModel{}, err
		}
		if done {
			return current, nil
		}

		select {
		case <-timeoutContext.Done():
			return deploymentAPIModel{}, fmt.Errorf("wait for deployment: %w", timeoutContext.Err())
		case <-time.After(r.pollInterval):
		}
	}
}

func (r *deploymentResource) waitForDeployment(
	ctx context.Context,
	deploymentID string,
	desiredStatus string,
) (deploymentAPIModel, error) {
	return r.pollDeployment(ctx, deploymentID, func(current deploymentAPIModel) (bool, error) {
		if current.Status == desiredStatus {
			return true, nil
		}
		if isTransientDeploymentStatus(current.Status) {
			return false, nil
		}
		if current.Status == "failed" {
			return false, errors.New("deployment entered failed status")
		}
		return false, fmt.Errorf(
			"deployment returned status %q while waiting for %q",
			current.Status,
			desiredStatus,
		)
	})
}

// waitForStableDeployment waits until the deployment can accept a start or stop operation.
func (r *deploymentResource) waitForStableDeployment(
	ctx context.Context,
	deploymentID string,
) (deploymentAPIModel, error) {
	return r.pollDeployment(ctx, deploymentID, func(current deploymentAPIModel) (bool, error) {
		if current.Status == "running" || current.Status == "stopped" {
			return true, nil
		}
		if isTransientDeploymentStatus(current.Status) {
			return false, nil
		}
		if current.Status == "failed" {
			return false, errors.New("deployment entered failed status")
		}
		return false, fmt.Errorf(
			"deployment is in unsupported status %q",
			current.Status,
		)
	})
}

func isTransientDeploymentStatus(status string) bool {
	return status == "pending" || status == "starting" || status == "stopping"
}

func immutableDeploymentChanges(plan, state managedDeploymentModel) []string {
	changed := make([]string, 0, 7)
	if !plan.ProjectID.IsUnknown() && !plan.ProjectID.Equal(state.ProjectID) {
		changed = append(changed, "project_id")
	}
	if !plan.Platform.IsUnknown() && !plan.Platform.Equal(state.Platform) {
		changed = append(changed, "platform")
	}
	if !plan.Region.IsUnknown() && !plan.Region.Equal(state.Region) {
		changed = append(changed, "region")
	}
	if !plan.Connections.IsUnknown() && !plan.Connections.Equal(state.Connections) {
		changed = append(changed, "connections")
	}
	if !plan.Transactions.IsUnknown() && !plan.Transactions.Equal(state.Transactions) {
		changed = append(changed, "tps")
	}
	if !plan.Name.IsUnknown() && !plan.Name.Equal(state.Name) {
		changed = append(changed, "deployment_name")
	}
	if !plan.Version.IsUnknown() && !plan.Version.Equal(state.Version) {
		changed = append(changed, "deployment_version")
	}
	return changed
}

func deploymentOperation(desiredStatus string) string {
	if desiredStatus == "running" {
		return "start"
	}
	return "stop"
}

func updateManagedDeployment(model *managedDeploymentModel, current deploymentAPIModel) {
	if current.ID != "" {
		model.ID = types.StringValue(current.ID)
	}
	if current.Name != "" && (model.Name.IsNull() || model.Name.IsUnknown()) {
		model.Name = types.StringValue(current.Name)
	}
	if current.Type != "" {
		model.Type = types.StringValue(current.Type)
	}
	if current.Status != "" {
		model.Status = types.StringValue(current.Status)
	}
	if current.Connections > 0 {
		model.Connections = types.Int64Value(current.Connections)
	}
	if current.Transactions > 0 {
		model.Transactions = types.Int64Value(current.Transactions)
	}

	// Terraform rejects a state that still holds an unknown value. Both attributes are computed
	// and stay unknown on create when the Platform API omits them, so resolve them to null.
	if model.Name.IsUnknown() {
		model.Name = types.StringNull()
	}
	if model.Type.IsUnknown() {
		model.Type = types.StringNull()
	}
}

func validatePositiveInt64(value types.Int64, attribute string, diagnostics *diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if value.ValueInt64() <= 0 {
		diagnostics.AddAttributeError(
			path.Root(attribute),
			"Invalid value",
			attribute+" must be greater than zero.",
		)
	}
}

func validateStringChoice(
	value types.String,
	attribute string,
	choices []string,
	diagnostics *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	for _, choice := range choices {
		if value.ValueString() == choice {
			return
		}
	}
	diagnostics.AddAttributeError(
		path.Root(attribute),
		"Invalid value",
		attribute+" must be one of: "+strings.Join(choices, ", ")+".",
	)
}

func validateStringLength(
	value types.String,
	attribute string,
	minimum int,
	maximum int,
	diagnostics *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	length := utf8.RuneCountInString(value.ValueString())
	if length < minimum || length > maximum {
		diagnostics.AddAttributeError(
			path.Root(attribute),
			"Invalid value",
			fmt.Sprintf("%s must contain between %d and %d characters.", attribute, minimum, maximum),
		)
	}
}
