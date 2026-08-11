package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	defaultTLSPollInterval = time.Minute
	defaultTLSTimeout      = 30 * time.Minute
)

type deploymentTLSResource struct {
	platform     *client.Client
	pollInterval time.Duration
	timeout      time.Duration
}

type deploymentTLSModel struct {
	DeploymentID  types.String `tfsdk:"deployment_id"`
	TLSType       types.String `tfsdk:"tls_type"`
	Certificate   types.String `tfsdk:"certificate_pem"`
	PrivateKey    types.String `tfsdk:"private_key_pem"`
	CACertificate types.String `tfsdk:"ca_certificate_pem"`
	Status        types.String `tfsdk:"status"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
	CommonName    types.String `tfsdk:"common_name"`
}

type deploymentTLSRequest struct {
	TLSType       string `json:"tlsType"`
	Certificate   string `json:"cert"`
	PrivateKey    string `json:"key"`
	CACertificate string `json:"cacert,omitempty"`
}

type deploymentTLSResponse struct {
	TLSType    string `json:"tlsType"`
	Status     string `json:"status"`
	ExpiresAt  string `json:"expire"`
	CommonName string `json:"cn"`
}

func newDeploymentTLSResource() resource.Resource {
	return &deploymentTLSResource{
		pollInterval: defaultTLSPollInterval,
		timeout:      defaultTLSTimeout,
	}
}

func (r *deploymentTLSResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_deployment_tls"
}

func (r *deploymentTLSResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	requiresReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	response.Schema = schema.Schema{
		Description: "Manage the TLS certificate for one Platform deployment.",
		Attributes: map[string]schema.Attribute{
			"deployment_id": schema.StringAttribute{
				Required:      true,
				PlanModifiers: requiresReplace,
				Description:   "Identifier of the Platform deployment whose TLS configuration is managed.",
			},
			"tls_type": schema.StringAttribute{
				Required:      true,
				PlanModifiers: requiresReplace,
				Description:   "TLS mode: one-way or two-way.",
			},
			"certificate_pem": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "PEM-encoded server certificate chain.",
			},
			"private_key_pem": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "PEM-encoded private key for the server certificate.",
			},
			"ca_certificate_pem": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "PEM-encoded client CA certificate required for two-way TLS.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "TLS configuration status returned by the Platform API.",
			},
			"expires_at": schema.StringAttribute{
				Computed:    true,
				Description: "Certificate expiration time returned by the Platform API.",
			},
			"common_name": schema.StringAttribute{
				Computed:    true,
				Description: "Certificate common name returned by the Platform API.",
			},
		},
	}
}

func (r *deploymentTLSResource) Configure(
	_ context.Context,
	request resource.ConfigureRequest,
	response *resource.ConfigureResponse,
) {
	r.platform = platformClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (r *deploymentTLSResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	var config deploymentTLSModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	validateNonEmptyString(config.DeploymentID, "deployment_id", &response.Diagnostics)
	validateNonEmptyString(config.Certificate, "certificate_pem", &response.Diagnostics)
	validateNonEmptyString(config.PrivateKey, "private_key_pem", &response.Diagnostics)
	if config.TLSType.IsNull() || config.TLSType.IsUnknown() {
		return
	}

	switch config.TLSType.ValueString() {
	case "one-way":
		if !config.CACertificate.IsNull() && !config.CACertificate.IsUnknown() &&
			config.CACertificate.ValueString() != "" {
			response.Diagnostics.AddAttributeError(
				path.Root("ca_certificate_pem"),
				"Unexpected CA certificate",
				"ca_certificate_pem is only valid when tls_type is two-way.",
			)
		}
	case "two-way":
		if config.CACertificate.IsNull() ||
			(!config.CACertificate.IsUnknown() && config.CACertificate.ValueString() == "") {
			response.Diagnostics.AddAttributeError(
				path.Root("ca_certificate_pem"),
				"Missing CA certificate",
				"ca_certificate_pem is required when tls_type is two-way.",
			)
		}
	default:
		response.Diagnostics.AddAttributeError(
			path.Root("tls_type"),
			"Invalid TLS type",
			"tls_type must be one-way or two-way.",
		)
	}
}

func (r *deploymentTLSResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	var plan deploymentTLSModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	var created deploymentTLSResponse
	_, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   deploymentTLSPath(plan.DeploymentID.ValueString()),
		Body:   tlsRequest(plan),
		Result: &created,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create deployment TLS", err.Error())
		return
	}

	// Persist the remote identity before polling so a failed create remains destroyable.
	updateTLSState(&plan, created)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}

	current, err := r.waitForTLS(ctx, plan.DeploymentID.ValueString(), false)
	if err != nil {
		response.Diagnostics.AddError("Deployment TLS creation did not complete", err.Error())
		return
	}
	updateTLSState(&plan, current)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *deploymentTLSResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	var state deploymentTLSModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	current, exists, err := r.readTLS(ctx, state.DeploymentID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to read deployment TLS", err.Error())
		return
	}
	if !exists {
		response.State.RemoveResource(ctx)
		return
	}
	updateTLSState(&state, current)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *deploymentTLSResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	var plan deploymentTLSModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	var updated deploymentTLSResponse
	_, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodPut,
		Path:   deploymentTLSPath(plan.DeploymentID.ValueString()),
		Body:   tlsRequest(plan),
		Result: &updated,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to update deployment TLS", err.Error())
		return
	}

	current, err := r.waitForTLS(ctx, plan.DeploymentID.ValueString(), false)
	if err != nil {
		response.Diagnostics.AddError("Deployment TLS update did not complete", err.Error())
		return
	}
	updateTLSState(&plan, current)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *deploymentTLSResource) Delete(
	ctx context.Context,
	request resource.DeleteRequest,
	response *resource.DeleteResponse,
) {
	var state deploymentTLSModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !requirePlatform(r.platform, &response.Diagnostics) {
		return
	}

	if err := r.deleteTLS(ctx, state.DeploymentID.ValueString()); err != nil {
		response.Diagnostics.AddError("Deployment TLS deletion did not complete", err.Error())
	}
}

func (r *deploymentTLSResource) deleteTLS(ctx context.Context, deploymentID string) error {
	_, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodDelete,
		Path:   deploymentTLSPath(deploymentID),
	})
	if client.IsStatus(err, http.StatusNotFound) {
		_, exists, readErr := r.readTLS(ctx, deploymentID)
		if readErr != nil {
			return fmt.Errorf("verify deployment TLS after DELETE returned 404: %w", readErr)
		}
		if exists {
			return errors.New("deployment TLS still exists after DELETE returned 404")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = r.waitForTLS(ctx, deploymentID, true)
	return err
}

func (r *deploymentTLSResource) readTLS(
	ctx context.Context,
	deploymentID string,
) (deploymentTLSResponse, bool, error) {
	var current deploymentTLSResponse
	_, err := r.platform.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   deploymentTLSPath(deploymentID),
		Result: &current,
	})
	if client.IsStatus(err, http.StatusNotFound) {
		return deploymentTLSResponse{}, false, nil
	}
	if err != nil {
		return deploymentTLSResponse{}, false, err
	}
	exists := current.TLSType != "" || current.Status != ""
	return current, exists, nil
}

func (r *deploymentTLSResource) waitForTLS(
	ctx context.Context,
	deploymentID string,
	deleting bool,
) (deploymentTLSResponse, error) {
	timeoutContext, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	for {
		current, exists, err := r.readTLS(timeoutContext, deploymentID)
		if err != nil {
			return deploymentTLSResponse{}, err
		}
		if deleting && !exists {
			return deploymentTLSResponse{}, nil
		}
		if exists {
			switch current.Status {
			case "running":
				if !deleting {
					return current, nil
				}
			case "failed":
				return deploymentTLSResponse{}, errors.New("deployment TLS entered failed status")
			case "pending", "terminated":
			default:
				return deploymentTLSResponse{}, fmt.Errorf(
					"deployment TLS returned unknown status %q",
					current.Status,
				)
			}
		}

		select {
		case <-timeoutContext.Done():
			return deploymentTLSResponse{}, fmt.Errorf("wait for deployment TLS: %w", timeoutContext.Err())
		case <-time.After(r.pollInterval):
		}
	}
}

func deploymentTLSPath(deploymentID string) string {
	return "/deployments/" + client.EscapePathSegment(deploymentID) + "/tls"
}

func tlsRequest(model deploymentTLSModel) deploymentTLSRequest {
	return deploymentTLSRequest{
		TLSType:       model.TLSType.ValueString(),
		Certificate:   model.Certificate.ValueString(),
		PrivateKey:    model.PrivateKey.ValueString(),
		CACertificate: model.CACertificate.ValueString(),
	}
}

func updateTLSState(model *deploymentTLSModel, current deploymentTLSResponse) {
	// tls_type is a required, replace-only attribute. The Platform API marks it optional in
	// every TLS response, so only overwrite the configured value when it is actually reported.
	if current.TLSType != "" {
		model.TLSType = types.StringValue(current.TLSType)
	}
	model.Status = types.StringValue(current.Status)
	model.ExpiresAt = types.StringValue(current.ExpiresAt)
	model.CommonName = types.StringValue(current.CommonName)
}

func validateNonEmptyString(value types.String, attribute string, diagnostics *diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if value.ValueString() == "" {
		diagnostics.AddAttributeError(
			path.Root(attribute),
			"Empty value",
			attribute+" must not be empty.",
		)
	}
}
