package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

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

// shortcut: this gate covers one Provider process; remove it once backend key writes are concurrency-safe.
var deploymentAPIKeyIssuance = make(chan struct{}, 1)

type deploymentAPIKeyResource struct {
	platform *client.Client
	pending  bool
}

type deploymentAPIKeyModel struct {
	DeploymentID types.String `tfsdk:"deployment_id"`
	Whitelist    types.Map    `tfsdk:"whitelist"`
	Description  types.String `tfsdk:"description"`
	AppID        types.String `tfsdk:"app_id"`
	AppSecret    types.String `tfsdk:"app_secret"`
}

type createDeploymentAPIKeyRequest struct {
	Whitelist   map[string][]string `json:"whitelist"`
	Description string              `json:"description"`
}

type deploymentAPIKeyResponse struct {
	AppID     string `json:"appID"`
	AppSecret string `json:"appSecret"`
}

func newDeploymentAPIKeyResource() resource.Resource {
	return &deploymentAPIKeyResource{}
}

func (r *deploymentAPIKeyResource) Metadata(_ context.Context, request resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = request.ProviderTypeName + "_deployment_api_key"
}

func (r *deploymentAPIKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse) {
	useState := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	response.Schema = schema.Schema{
		Description: "Create and retain one Deployment API key using Platform credentials. Refresh retains the local issuance record; remote updates, revocation and deletion are not supported.",
		Attributes: map[string]schema.Attribute{
			"deployment_id": schema.StringAttribute{
				Required: true, Description: "Running Dedicated or Dedicated Flex deployment identifier. Cannot change after issuance.",
			},
			"whitelist": schema.MapAttribute{
				Required: true, ElementType: types.SetType{ElemType: types.StringType},
				Description: "Explicit Deployment API paths and allowed HTTP methods. Cannot change after issuance; omitted permissions are not granted.",
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				Description: "Key description, at most 128 characters. Cannot change after issuance.",
			},
			"app_id": schema.StringAttribute{
				Computed: true, PlanModifiers: useState, Description: "Issued Deployment API App ID (AK).",
			},
			"app_secret": schema.StringAttribute{
				Computed: true, Sensitive: true, PlanModifiers: useState,
				Description: "Deployment API App Secret (SK), returned only on creation and retained in plaintext Terraform state.",
			},
		},
	}
}

func (r *deploymentAPIKeyResource) Configure(_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
	r.platform, r.pending = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
	if data, ok := request.ProviderData.(*ProviderData); ok && !supportsDestroyPlanning(data.TerraformVersion) {
		response.Diagnostics.AddError("Deployment API key issuance requires Terraform 1.3 or later", "Older Terraform versions cannot plan key destruction, so create-before-destroy replacement cannot be safely blocked. Upgrade Terraform before using this resource.")
	}
}

func supportsDestroyPlanning(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 1 || major == 1 && minor >= 3)
}

func (r *deploymentAPIKeyResource) ValidateConfig(ctx context.Context, request resource.ValidateConfigRequest, response *resource.ValidateConfigResponse) {
	var config deploymentAPIKeyModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if !response.Diagnostics.HasError() {
		config.payload(&response.Diagnostics)
	}
}

func (r *deploymentAPIKeyResource) ModifyPlan(ctx context.Context, request resource.ModifyPlanRequest, response *resource.ModifyPlanResponse) {
	if request.Plan.Raw.IsNull() {
		response.Diagnostics.AddError("Deployment API key deletion is not supported", "Revoke the key in Console, then explicitly remove its issuance record from Terraform state.")
		return
	}
	if request.State.Raw.IsNull() {
		return
	}
	var plan, state deploymentAPIKeyModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	if !plan.DeploymentID.Equal(state.DeploymentID) || !plan.Whitelist.Equal(state.Whitelist) || !plan.Description.Equal(state.Description) {
		response.Diagnostics.AddError("Deployment API key changes are not supported", "The deployment identifier, permissions and description must remain known and unchanged. Create a separate issuance resource for a different deployment or permissions. Revoke old keys in Console before explicitly removing their state.")
	}
}

func (r *deploymentAPIKeyResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var plan deploymentAPIKeyModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, r.pending, &response.Diagnostics) {
		return
	}
	payload := plan.payload(&response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}
	if payload == nil || plan.DeploymentID.IsUnknown() {
		response.Diagnostics.AddError("Unknown Deployment API key configuration", "The deployment and explicit permissions must be known before issuing a key.")
		return
	}
	select {
	case deploymentAPIKeyIssuance <- struct{}{}:
		defer func() { <-deploymentAPIKeyIssuance }()
	case <-ctx.Done():
		response.Diagnostics.AddError("Deployment API key issuance cancelled", "No request was sent while waiting for another key creation to finish.")
		return
	}
	deploymentPath := "/deployments/" + client.EscapePathSegment(plan.DeploymentID.ValueString())
	var deployment deploymentAPIModel
	_, err := r.platform.Do(ctx, client.Request{Method: http.MethodGet, Path: deploymentPath, Result: &deployment})
	if err != nil {
		response.Diagnostics.AddError("Unable to read deployment before key issuance", err.Error())
		return
	}
	if deployment.ID != plan.DeploymentID.ValueString() || deployment.Status != "running" ||
		(deployment.Type != "dedicated" && deployment.Type != "dedicatedFlex") {
		response.Diagnostics.AddError("Deployment is not ready for key issuance", "The exact deployment must be a running Dedicated or Dedicated Flex deployment.")
		return
	}
	var issued deploymentAPIKeyResponse
	status, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodPost, Path: deploymentPath + "/applications", Body: payload, Result: &issued,
	})
	if status == http.StatusCreated && err == nil && validIssuedCredential(issued.AppID) && validIssuedCredential(issued.AppSecret) {
		plan.AppID = types.StringValue(issued.AppID)
		plan.AppSecret = types.StringValue(issued.AppSecret)
		response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
		return
	}
	// A lost response can conceal a successful issuance. Keep a receipt so a later apply cannot silently repeat it.
	uncertain := true
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		uncertain = false
	default:
		plan.AppID, plan.AppSecret = types.StringNull(), types.StringNull()
		if validIssuedCredential(issued.AppID) {
			plan.AppID = types.StringValue(issued.AppID)
		}
		if validIssuedCredential(issued.AppSecret) {
			plan.AppSecret = types.StringValue(issued.AppSecret)
		}
		response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
	}
	detail := fmt.Sprintf("The creation response was not a complete HTTP 201 result (HTTP %d).", status)
	if err != nil {
		detail = err.Error()
	}
	if uncertain {
		detail += " The outcome may be unknown; inspect the Console key list before any new issuance."
		if validIssuedCredential(issued.AppID) && validIssuedCredential(issued.AppSecret) {
			detail += " The returned credentials were saved in state and Terraform marks this failed creation as tainted. If Console verification confirms this key is usable, run terraform untaint for this resource to retain it; do not remove its state and issue another key."
		}
	} else {
		detail += " The Platform API rejected key creation."
	}
	response.Diagnostics.AddError("Unable to create Deployment API key", detail+" This request was not retried.")
}

func (r *deploymentAPIKeyResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var state deploymentAPIKeyModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	if !validIssuedCredential(state.AppID.ValueString()) || !validIssuedCredential(state.AppSecret.ValueString()) {
		response.Diagnostics.AddError("Deployment API key issuance is incomplete", "Inspect and resolve the prior issuance in Console before explicitly removing this record. Terraform cannot recover a missing secret or automatically issue another key.")
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *deploymentAPIKeyResource) Update(_ context.Context, _ resource.UpdateRequest, response *resource.UpdateResponse) {
	response.Diagnostics.AddError("Deployment API key changes are not supported", "Create a separate issuance resource and manage revocation explicitly in Console.")
}

func (r *deploymentAPIKeyResource) Delete(_ context.Context, _ resource.DeleteRequest, response *resource.DeleteResponse) {
	response.Diagnostics.AddError("Deployment API key deletion is not supported", "Revoke the key in Console before explicitly removing its issuance record from Terraform state.")
}

func validIssuedCredential(value string) bool {
	return strings.TrimSpace(value) != "" && value != "******"
}

func (m deploymentAPIKeyModel) payload(diagnostics *diag.Diagnostics) *createDeploymentAPIKeyRequest {
	if !m.DeploymentID.IsNull() && !m.DeploymentID.IsUnknown() && strings.TrimSpace(m.DeploymentID.ValueString()) == "" {
		diagnostics.AddAttributeError(path.Root("deployment_id"), "Empty deployment identifier", "deployment_id must not be blank.")
	}
	validateStringLength(m.Description, "description", 0, 128, diagnostics)
	if m.Whitelist.IsUnknown() || m.DeploymentID.IsUnknown() || m.Description.IsUnknown() {
		return nil
	}
	if m.DeploymentID.IsNull() || m.Whitelist.IsNull() || len(m.Whitelist.Elements()) == 0 {
		diagnostics.AddError("Invalid Deployment API key configuration", "A deployment_id and a non-empty explicit whitelist are required.")
		return nil
	}
	whitelist := make(map[string][]string, len(m.Whitelist.Elements()))
	unknown := false
	for apiPath, value := range m.Whitelist.Elements() {
		if strings.TrimSpace(apiPath) == "" {
			diagnostics.AddAttributeError(path.Root("whitelist"), "Invalid permission path", "Permission paths must not be empty.")
		}
		if value.IsUnknown() {
			unknown = true
			continue
		}
		methods := value.(types.Set)
		if methods.IsNull() || len(methods.Elements()) == 0 {
			diagnostics.AddAttributeError(path.Root("whitelist"), "Invalid permission methods", "Every path must have a non-empty set of HTTP methods.")
		}
		for _, method := range methods.Elements() {
			if method.IsUnknown() {
				unknown = true
				continue
			}
			verb := method.(types.String).ValueString()
			switch verb {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
				whitelist[apiPath] = append(whitelist[apiPath], verb)
			default:
				diagnostics.AddAttributeError(path.Root("whitelist"), "Invalid permission method", "Use non-null, uppercase HTTP methods: GET, POST, PUT, PATCH, DELETE, HEAD or OPTIONS.")
			}
		}
		sort.Strings(whitelist[apiPath])
	}
	if unknown || diagnostics.HasError() {
		return nil
	}
	return &createDeploymentAPIKeyRequest{Whitelist: whitelist, Description: m.Description.ValueString()}
}
