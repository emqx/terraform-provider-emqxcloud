package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	authenticationUsersPath = "/authentication/password_based:built_in_database/users"
	authorizationRulesPath  = "/authorization/sources/built_in_database/rules"
	bannedPageSize          = 100
	bannedMaxPages          = 100
)

type deploymentAPIResource struct {
	deployment *client.Client
}

func (r *deploymentAPIResource) Configure(
	_ context.Context,
	request resource.ConfigureRequest,
	response *resource.ConfigureResponse,
) {
	r.deployment = deploymentClientFromProviderData(request.ProviderData, &response.Diagnostics)
}

func (r *deploymentAPIResource) requireConfigured(diagnostics *diag.Diagnostics) bool {
	if r.deployment != nil {
		return true
	}
	diagnostics.AddError(
		"Deployment API is not configured",
		"Configure deployment_endpoint, deployment_api_key, and deployment_api_secret.",
	)
	return false
}

type authenticationUserResource struct {
	deploymentAPIResource
}

type authenticationUserModel struct {
	UserID      types.String `tfsdk:"user_id"`
	Password    types.String `tfsdk:"password"`
	IsSuperuser types.Bool   `tfsdk:"is_superuser"`
}

func newAuthenticationUserResource() resource.Resource {
	return &authenticationUserResource{}
}

func (r *authenticationUserResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_authentication_user"
}

func (r *authenticationUserResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "Manage one user in the EMQX v5 built-in authentication database.",
		Attributes: map[string]schema.Attribute{
			"user_id": schema.StringAttribute{
				Required:    true,
				Description: "Authentication user identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Authentication password. The Deployment API does not return this value.",
			},
			"is_superuser": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Whether the authentication user is a superuser.",
			},
		},
	}
}

func (r *authenticationUserResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	var config authenticationUserModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}
	validateNonEmptyString(config.UserID, "user_id", &response.Diagnostics)
	validateNonEmptyString(config.Password, "password", &response.Diagnostics)
}

func (r *authenticationUserResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	var plan authenticationUserModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   authenticationUsersPath,
		Body: map[string]any{
			"user_id":      plan.UserID.ValueString(),
			"password":     plan.Password.ValueString(),
			"is_superuser": plan.IsSuperuser.ValueBool(),
		},
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create authentication user", err.Error())
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *authenticationUserResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	var state authenticationUserModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	var remote struct {
		UserID      string `json:"user_id"`
		IsSuperuser bool   `json:"is_superuser"`
	}
	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   authenticationUsersPath + "/" + client.EscapePathSegment(state.UserID.ValueString()),
		Result: &remote,
	})
	if client.IsStatus(err, http.StatusNotFound) {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to read authentication user", err.Error())
		return
	}
	state.IsSuperuser = types.BoolValue(remote.IsSuperuser)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *authenticationUserResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	var plan authenticationUserModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodPut,
		Path:   authenticationUsersPath + "/" + client.EscapePathSegment(plan.UserID.ValueString()),
		Body: map[string]any{
			"password":     plan.Password.ValueString(),
			"is_superuser": plan.IsSuperuser.ValueBool(),
		},
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to update authentication user", err.Error())
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *authenticationUserResource) Delete(
	ctx context.Context,
	request resource.DeleteRequest,
	response *resource.DeleteResponse,
) {
	var state authenticationUserModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodDelete,
		Path:   authenticationUsersPath + "/" + client.EscapePathSegment(state.UserID.ValueString()),
	})
	if err != nil && !client.IsStatus(err, http.StatusNotFound) {
		response.Diagnostics.AddError("Unable to delete authentication user", err.Error())
	}
}

type authorizationResourceSpec struct {
	typeSuffix        string
	collection        string
	identityAttribute string
	identityField     string
}

type authorizationResource struct {
	deploymentAPIResource
	spec authorizationResourceSpec
}

type authorizationUserModel struct {
	Username  types.String `tfsdk:"username"`
	RulesJSON types.String `tfsdk:"rules_json"`
}

type authorizationClientModel struct {
	ClientID  types.String `tfsdk:"client_id"`
	RulesJSON types.String `tfsdk:"rules_json"`
}

type authorizationState struct {
	Identity  types.String
	RulesJSON types.String
}

func newAuthorizationUserResource() resource.Resource {
	return &authorizationResource{spec: authorizationResourceSpec{
		typeSuffix:        "authorization_user",
		collection:        authorizationRulesPath + "/users",
		identityAttribute: "username",
		identityField:     "username",
	}}
}

func newAuthorizationClientResource() resource.Resource {
	return &authorizationResource{spec: authorizationResourceSpec{
		typeSuffix:        "authorization_client",
		collection:        authorizationRulesPath + "/clients",
		identityAttribute: "client_id",
		identityField:     "clientid",
	}}
}

func (r *authorizationResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_" + r.spec.typeSuffix
}

func (r *authorizationResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		Description: "Manage one identity's rules in the EMQX v5 built-in authorization database.",
		Attributes: map[string]schema.Attribute{
			r.spec.identityAttribute: schema.StringAttribute{
				Required:    true,
				Description: "Identity whose complete built-in authorization rule set is managed.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"rules_json": schema.StringAttribute{
				Required:    true,
				Description: "JSON array containing the authorization rules.",
			},
		},
	}
}

func (r *authorizationResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Config)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	validateNonEmptyString(state.Identity, r.spec.identityAttribute, &response.Diagnostics)
	if state.RulesJSON.IsNull() || state.RulesJSON.IsUnknown() {
		return
	}
	if _, err := parseJSONArray(state.RulesJSON.ValueString()); err != nil {
		response.Diagnostics.AddAttributeError(
			path.Root("rules_json"),
			"Invalid authorization rules",
			err.Error(),
		)
	}
}

func (r *authorizationResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Plan)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	payload, err := r.payload(state)
	if err != nil {
		response.Diagnostics.AddError("Invalid authorization rules", err.Error())
		return
	}
	_, err = r.deployment.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   r.spec.collection,
		Body:   []any{payload},
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create authorization rules", err.Error())
		return
	}
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *authorizationResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.State)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	var remote map[string]any
	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   r.itemPath(state),
		Result: &remote,
	})
	if client.IsStatus(err, http.StatusNotFound) {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to read authorization rules", err.Error())
		return
	}
	rawRules, exists := remote["rules"]
	if !exists {
		rawRules = []any{}
	}
	rules, ok := rawRules.([]any)
	if !ok {
		response.Diagnostics.AddError(
			"Unable to read authorization rules",
			"Deployment API returned rules in an unexpected format.",
		)
		return
	}
	projected, err := projectJSONArray(state.RulesJSON.ValueString(), rules)
	if err != nil {
		response.Diagnostics.AddError("Unable to project authorization rules", err.Error())
		return
	}
	state.RulesJSON = types.StringValue(projected)
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *authorizationResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Plan)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	payload, err := r.payload(state)
	if err != nil {
		response.Diagnostics.AddError("Invalid authorization rules", err.Error())
		return
	}
	_, err = r.deployment.Do(ctx, client.Request{
		Method: http.MethodPut,
		Path:   r.itemPath(state),
		Body:   payload,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to update authorization rules", err.Error())
		return
	}
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *authorizationResource) Delete(
	ctx context.Context,
	request resource.DeleteRequest,
	response *resource.DeleteResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.State)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodDelete,
		Path:   r.itemPath(state),
	})
	if err != nil && !client.IsStatus(err, http.StatusNotFound) {
		response.Diagnostics.AddError("Unable to delete authorization rules", err.Error())
	}
}

func (r *authorizationResource) terraformState(
	ctx context.Context,
	getter terraformGetter,
) (authorizationState, diag.Diagnostics) {
	if r.spec.identityAttribute == "username" {
		var model authorizationUserModel
		diagnostics := getter.Get(ctx, &model)
		return authorizationState{Identity: model.Username, RulesJSON: model.RulesJSON}, diagnostics
	}
	var model authorizationClientModel
	diagnostics := getter.Get(ctx, &model)
	return authorizationState{Identity: model.ClientID, RulesJSON: model.RulesJSON}, diagnostics
}

func (r *authorizationResource) setTerraformState(
	ctx context.Context,
	state *tfsdk.State,
	resourceState authorizationState,
) diag.Diagnostics {
	if r.spec.identityAttribute == "username" {
		return state.Set(ctx, &authorizationUserModel{
			Username: resourceState.Identity, RulesJSON: resourceState.RulesJSON,
		})
	}
	return state.Set(ctx, &authorizationClientModel{
		ClientID: resourceState.Identity, RulesJSON: resourceState.RulesJSON,
	})
}

func (r *authorizationResource) payload(state authorizationState) (map[string]any, error) {
	rules, err := parseJSONArray(state.RulesJSON.ValueString())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		r.spec.identityField: state.Identity.ValueString(),
		"rules":              rules,
	}, nil
}

func (r *authorizationResource) itemPath(state authorizationState) string {
	return r.spec.collection + "/" + client.EscapePathSegment(state.Identity.ValueString())
}

type bannedResource struct {
	deploymentAPIResource
}

type bannedModel struct {
	As         types.String `tfsdk:"as"`
	Who        types.String `tfsdk:"who"`
	ConfigJSON types.String `tfsdk:"config_json"`
}

type bannedListResponse struct {
	Data []json.RawMessage `json:"data"`
	Meta struct {
		HasNext bool `json:"hasnext"`
	} `json:"meta"`
}

func newBannedResource() resource.Resource {
	return &bannedResource{}
}

func (r *bannedResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_banned"
}

func (r *bannedResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	requiresReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	response.Schema = schema.Schema{
		Description: "Manage one EMQX v5 banned entry.",
		Attributes: map[string]schema.Attribute{
			"as": schema.StringAttribute{
				Required:      true,
				PlanModifiers: requiresReplace,
				Description:   "Ban matching method: clientid, username, peerhost, clientid_re, username_re, or peerhost_net.",
			},
			"who": schema.StringAttribute{
				Required:      true,
				PlanModifiers: requiresReplace,
				Description:   "Exact identity or pattern matched by the selected ban method.",
			},
			"config_json": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("{}"),
				PlanModifiers: requiresReplace,
				Description:   "Optional JSON object containing at, until, by, and reason.",
			},
		},
	}
}

func (r *bannedResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	var config bannedModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}
	validateNonEmptyString(config.As, "as", &response.Diagnostics)
	validateNonEmptyString(config.Who, "who", &response.Diagnostics)
	if !config.As.IsNull() && !config.As.IsUnknown() {
		if _, err := bannedFilterName(config.As.ValueString()); err != nil {
			response.Diagnostics.AddAttributeError(path.Root("as"), "Invalid ban method", err.Error())
		}
	}
	if config.ConfigJSON.IsNull() || config.ConfigJSON.IsUnknown() {
		return
	}
	if _, err := bannedPayload(config); err != nil {
		response.Diagnostics.AddAttributeError(
			path.Root("config_json"), "Invalid banned configuration", err.Error(),
		)
	}
}

func (r *bannedResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	var plan bannedModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	payload, err := bannedPayload(plan)
	if err != nil {
		response.Diagnostics.AddError("Invalid banned configuration", err.Error())
		return
	}
	_, err = r.deployment.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   "/banned",
		Body:   payload,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create banned entry", err.Error())
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *bannedResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	var state bannedModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	remote, exists, err := r.readRemote(ctx, state)
	if err != nil {
		response.Diagnostics.AddError("Unable to read banned entry", err.Error())
		return
	}
	if !exists {
		response.State.RemoveResource(ctx)
		return
	}
	projected, _, err := projectBannedJSON(state.ConfigJSON.ValueString(), remote)
	if err != nil {
		response.Diagnostics.AddError("Unable to project banned entry", err.Error())
		return
	}
	state.ConfigJSON = types.StringValue(projected)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *bannedResource) Update(
	_ context.Context,
	_ resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	response.Diagnostics.AddError(
		"Banned entry update is not supported",
		"Change to a banned entry must replace the resource.",
	)
}

func (r *bannedResource) Delete(
	ctx context.Context,
	request resource.DeleteRequest,
	response *resource.DeleteResponse,
) {
	var state bannedModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}
	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodDelete,
		Path:   bannedItemPath(state),
	})
	if err != nil && !client.IsStatus(err, http.StatusNotFound) {
		response.Diagnostics.AddError("Unable to delete banned entry", err.Error())
	}
}

func (r *bannedResource) readRemote(
	ctx context.Context,
	state bannedModel,
) (map[string]any, bool, error) {
	filterName, err := bannedFilterName(state.As.ValueString())
	if err != nil {
		return nil, false, err
	}
	for pageNumber := 1; pageNumber <= bannedMaxPages; pageNumber++ {
		query := url.Values{
			"limit": []string{strconv.Itoa(bannedPageSize)},
			"page":  []string{strconv.Itoa(pageNumber)},
		}
		if filterName != "" {
			query.Set(filterName, state.Who.ValueString())
		}
		var page bannedListResponse
		_, err = r.deployment.Do(ctx, client.Request{
			Method: http.MethodGet,
			Path:   "/banned",
			Query:  query,
			Result: &page,
		})
		if err != nil {
			return nil, false, err
		}
		for _, rawRecord := range page.Data {
			record, err := parseJSONObject(string(rawRecord))
			if err != nil {
				return nil, false, fmt.Errorf("decode banned entry: %w", err)
			}
			if record["as"] == state.As.ValueString() && record["who"] == state.Who.ValueString() {
				return record, true, nil
			}
		}
		// An empty page ends the listing even when the API keeps reporting hasnext.
		if !page.Meta.HasNext || len(page.Data) == 0 {
			return nil, false, nil
		}
	}
	// Absence is unconfirmed, so report an error instead of dropping the entry from state.
	return nil, false, fmt.Errorf("banned listing exceeded %d pages", bannedMaxPages)
}

func projectBannedJSON(configJSON string, remote map[string]any) (string, bool, error) {
	configured, err := parseJSONObject(configJSON)
	if err != nil {
		return "", false, err
	}
	for _, key := range []string{"at", "until"} {
		configuredTime, configuredOK := bannedTime(configured[key])
		remoteTime, remoteOK := bannedTime(remote[key])
		if configuredOK && remoteOK && configuredTime.Equal(remoteTime) {
			remote[key] = configured[key]
		}
	}
	return projectVisibleJSON(configJSON, remote)
}

func bannedTime(value any) (time.Time, bool) {
	switch value := value.(type) {
	case json.Number:
		seconds, err := value.Int64()
		if err == nil {
			return time.Unix(seconds, 0), true
		}
	case string:
		parsed, err := time.Parse(time.RFC3339, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func bannedPayload(state bannedModel) (map[string]any, error) {
	if _, err := bannedFilterName(state.As.ValueString()); err != nil {
		return nil, err
	}
	payload, err := parseJSONObject(state.ConfigJSON.ValueString())
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"at": true, "until": true, "by": true, "reason": true}
	for key := range payload {
		if key == "as" || key == "who" {
			return nil, fmt.Errorf(
				"config_json must not contain %q; configure it with the resource attribute",
				key,
			)
		}
		if !allowed[key] {
			return nil, fmt.Errorf("config_json contains unsupported field %q", key)
		}
	}
	payload["as"] = state.As.ValueString()
	payload["who"] = state.Who.ValueString()
	return payload, nil
}

func bannedFilterName(as string) (string, error) {
	switch as {
	case "clientid", "username", "peerhost":
		return as, nil
	case "clientid_re":
		return "like_clientid", nil
	case "username_re":
		return "like_username", nil
	case "peerhost_net":
		return "like_peerhost_net", nil
	default:
		return "", fmt.Errorf(
			"as must be one of clientid, username, peerhost, clientid_re, username_re, or peerhost_net",
		)
	}
}

func bannedItemPath(state bannedModel) string {
	return "/banned/" + client.EscapePathSegment(state.As.ValueString()) + "/" +
		client.EscapePathSegment(state.Who.ValueString())
}

func parseJSONArray(input string) ([]any, error) {
	var value any
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		return nil, fmt.Errorf("rules_json must contain valid JSON: %w", err)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, errors.New("rules_json must be a JSON array")
	}
	return array, nil
}

func projectJSONArray(configJSON string, remote []any) (string, error) {
	configured, err := parseJSONArray(configJSON)
	if err != nil {
		return "", err
	}
	projected := projectVisibleValue(configured, remote).([]any)
	if reflect.DeepEqual(configured, projected) {
		return configJSON, nil
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return "", fmt.Errorf("encode projected rules_json: %w", err)
	}
	return string(encoded), nil
}
