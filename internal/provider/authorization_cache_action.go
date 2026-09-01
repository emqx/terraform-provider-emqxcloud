package provider

import (
	"context"
	"net/http"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
)

type resetAuthorizationCacheAction struct {
	deploymentAPIResource
}

func newResetAuthorizationCacheAction() action.Action {
	return &resetAuthorizationCacheAction{}
}

func (a *resetAuthorizationCacheAction) Metadata(
	_ context.Context,
	request action.MetadataRequest,
	response *action.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_reset_authorization_cache"
}

func (a *resetAuthorizationCacheAction) Schema(
	_ context.Context,
	_ action.SchemaRequest,
	response *action.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "Reset the EMQX v5 node-wise authorization cache.",
	}
}

func (a *resetAuthorizationCacheAction) Configure(
	_ context.Context,
	request action.ConfigureRequest,
	response *action.ConfigureResponse,
) {
	a.deployment = deploymentClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (a *resetAuthorizationCacheAction) Invoke(
	ctx context.Context,
	_ action.InvokeRequest,
	response *action.InvokeResponse,
) {
	if !a.requireConfigured(&response.Diagnostics) {
		return
	}

	_, err := a.deployment.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   "/authorization/node_cache/reset",
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to reset authorization cache", err.Error())
	}
}
