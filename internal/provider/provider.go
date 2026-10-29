package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const typeName = "emqxcloud"

type providerModel struct {
	PlatformEndpoint    types.String `tfsdk:"platform_endpoint"`
	PlatformAPIKey      types.String `tfsdk:"platform_api_key"`
	PlatformAPISecret   types.String `tfsdk:"platform_api_secret"`
	DeploymentEndpoint  types.String `tfsdk:"deployment_endpoint"`
	DeploymentAPIKey    types.String `tfsdk:"deployment_api_key"`
	DeploymentAPISecret types.String `tfsdk:"deployment_api_secret"`
}

type credentialInput struct {
	name         string
	endpoint     types.String
	apiKey       types.String
	apiSecret    types.String
	endpointEnv  string
	apiKeyEnv    string
	apiSecretEnv string
}

type credentialGroup struct {
	endpoint  string
	apiKey    string
	apiSecret string
}

type ProviderData struct {
	Platform          *client.Client
	Deployment        *client.Client
	PlatformPending   bool
	DeploymentPending bool
	TerraformVersion  string
}

type emqxCloudProvider struct {
	version   string
	allowHTTP bool
}

func New(version string) func() frameworkprovider.Provider {
	return func() frameworkprovider.Provider {
		return &emqxCloudProvider{version: version}
	}
}

func (p *emqxCloudProvider) Metadata(
	_ context.Context,
	_ frameworkprovider.MetadataRequest,
	response *frameworkprovider.MetadataResponse,
) {
	response.TypeName = typeName
	response.Version = p.version
}

func (p *emqxCloudProvider) Schema(
	_ context.Context,
	_ frameworkprovider.SchemaRequest,
	response *frameworkprovider.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "Manage EMQX Cloud Platform and Deployment resources.",
		Attributes: map[string]schema.Attribute{
			"platform_endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "HTTPS Platform API endpoint, including /public_api/v1; user information, query strings, and fragments are not allowed.",
			},
			"platform_api_key": schema.StringAttribute{
				Optional:    true,
				Description: "Platform API key. May be set with EMQXCLOUD_PLATFORM_API_KEY.",
			},
			"platform_api_secret": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Platform API secret. May be set with EMQXCLOUD_PLATFORM_API_SECRET.",
			},
			"deployment_endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "HTTPS Deployment API endpoint, including /api/v5 for EMQX resources; user information, query strings, and fragments are not allowed.",
			},
			"deployment_api_key": schema.StringAttribute{
				Optional:    true,
				Description: "Deployment API key. May be set with EMQXCLOUD_DEPLOYMENT_API_KEY.",
			},
			"deployment_api_secret": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Deployment API secret. May be set with EMQXCLOUD_DEPLOYMENT_API_SECRET.",
			},
		},
	}
}

func (p *emqxCloudProvider) Configure(
	ctx context.Context,
	request frameworkprovider.ConfigureRequest,
	response *frameworkprovider.ConfigureResponse,
) {
	var config providerModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	// #nosec G101 -- values come from Provider configuration or named environment variables, not hardcoded credentials.
	platformCredentials, err := resolveCredentials(credentialInput{
		name:         "Platform",
		endpoint:     config.PlatformEndpoint,
		apiKey:       config.PlatformAPIKey,
		apiSecret:    config.PlatformAPISecret,
		endpointEnv:  "EMQXCLOUD_PLATFORM_ENDPOINT",
		apiKeyEnv:    "EMQXCLOUD_PLATFORM_API_KEY",
		apiSecretEnv: "EMQXCLOUD_PLATFORM_API_SECRET",
	})
	if err != nil {
		response.Diagnostics.AddError("Invalid Platform API configuration", err.Error())
	}
	// #nosec G101 -- values come from Provider configuration or named environment variables, not hardcoded credentials.
	deploymentCredentials, err := resolveCredentials(credentialInput{
		name:         "Deployment",
		endpoint:     config.DeploymentEndpoint,
		apiKey:       config.DeploymentAPIKey,
		apiSecret:    config.DeploymentAPISecret,
		endpointEnv:  "EMQXCLOUD_DEPLOYMENT_ENDPOINT",
		apiKeyEnv:    "EMQXCLOUD_DEPLOYMENT_API_KEY",
		apiSecretEnv: "EMQXCLOUD_DEPLOYMENT_API_SECRET",
	})
	if err != nil {
		response.Diagnostics.AddError("Invalid Deployment API configuration", err.Error())
	}
	if response.Diagnostics.HasError() {
		return
	}

	providerData := &ProviderData{
		TerraformVersion:  request.TerraformVersion,
		PlatformPending:   config.PlatformEndpoint.IsUnknown() || config.PlatformAPIKey.IsUnknown() || config.PlatformAPISecret.IsUnknown(),
		DeploymentPending: config.DeploymentEndpoint.IsUnknown() || config.DeploymentAPIKey.IsUnknown() || config.DeploymentAPISecret.IsUnknown(),
	}
	if platformCredentials != nil {
		providerData.Platform, err = newAPIClient(platformCredentials, "/public_api/v1", p.allowHTTP)
		if err != nil {
			response.Diagnostics.AddError("Invalid Platform API endpoint", err.Error())
		}
	}
	if deploymentCredentials != nil {
		providerData.Deployment, err = newAPIClient(deploymentCredentials, "/api/v5", p.allowHTTP)
		if err != nil {
			response.Diagnostics.AddError("Invalid Deployment API endpoint", err.Error())
		}
	}
	if response.Diagnostics.HasError() {
		return
	}

	response.DataSourceData = providerData
	response.ResourceData = providerData
	response.ActionData = providerData
}

func (p *emqxCloudProvider) Actions(_ context.Context) []func() action.Action {
	return []func() action.Action{
		newResetAuthorizationCacheAction,
	}
}

func (p *emqxCloudProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newProjectsDataSource,
		newDeploymentsDataSource,
		newDeploymentDataSource,
	}
}

func (p *emqxCloudProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newDeploymentResource,
		newDeploymentAPIKeyResource,
		newDeploymentTLSResource,
		newConnectorResource,
		newActionResource,
		newSourceResource,
		newRuleResource,
		newAuthenticationUserResource,
		newAuthorizationUserResource,
		newAuthorizationClientResource,
		newBannedResource,
	}
}

func resolveCredentials(input credentialInput) (*credentialGroup, error) {
	// Resource outputs are unknown during the first plan. Never replace them with environment credentials.
	if input.endpoint.IsUnknown() || input.apiKey.IsUnknown() || input.apiSecret.IsUnknown() {
		return nil, nil
	}
	endpoint, apiKey, apiSecret := input.endpoint.ValueString(), input.apiKey.ValueString(), input.apiSecret.ValueString()
	if input.endpoint.IsNull() && input.apiKey.IsNull() && input.apiSecret.IsNull() {
		endpoint, apiKey, apiSecret = os.Getenv(input.endpointEnv), os.Getenv(input.apiKeyEnv), os.Getenv(input.apiSecretEnv)
		if endpoint == "" && apiKey == "" && apiSecret == "" {
			return nil, nil
		}
	}
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(apiKey) == "" || strings.TrimSpace(apiSecret) == "" {
		return nil, fmt.Errorf(
			"%s endpoint, API key, and API secret must be configured together as non-blank values, either entirely in Provider configuration or entirely in environment variables",
			input.name,
		)
	}
	return &credentialGroup{
		endpoint:  endpoint,
		apiKey:    apiKey,
		apiSecret: apiSecret,
	}, nil
}

func newAPIClient(credentials *credentialGroup, requiredPathSuffix string, allowHTTP bool) (*client.Client, error) {
	if !strings.HasSuffix(strings.TrimRight(credentials.endpoint, "/"), requiredPathSuffix) {
		return nil, fmt.Errorf("API endpoint must end with %s", requiredPathSuffix)
	}
	apiClient, err := client.New(client.Options{
		Endpoint:  credentials.endpoint,
		APIKey:    credentials.apiKey,
		APISecret: credentials.apiSecret,
		AllowHTTP: allowHTTP,
	})
	if err != nil {
		return nil, err
	}
	return apiClient, nil
}
