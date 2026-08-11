package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	defaultEMQXPollInterval = time.Second
	defaultEMQXTimeout      = time.Minute
	maskedValue             = "******"
)

type emqxJSONResourceSpec struct {
	typeSuffix string
	collection string
	named      bool
	toggle     bool
}

type emqxJSONResource struct {
	deploymentAPIResource
	spec         emqxJSONResourceSpec
	pollInterval time.Duration
	timeout      time.Duration
}

type namedJSONResourceModel struct {
	Type       types.String `tfsdk:"type"`
	Name       types.String `tfsdk:"name"`
	ConfigJSON types.String `tfsdk:"config_json"`
}

type ruleJSONResourceModel struct {
	RuleID     types.String `tfsdk:"rule_id"`
	ConfigJSON types.String `tfsdk:"config_json"`
}

type emqxJSONState struct {
	Type       types.String
	Name       types.String
	RuleID     types.String
	ConfigJSON types.String
}

type terraformGetter interface {
	Get(context.Context, any) diag.Diagnostics
}

func newConnectorResource() resource.Resource {
	return newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "connector",
		collection: "/connectors",
		named:      true,
		toggle:     true,
	})
}

func newActionResource() resource.Resource {
	return newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "action",
		collection: "/actions",
		named:      true,
		toggle:     true,
	})
}

func newRuleResource() resource.Resource {
	return newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "rule",
		collection: "/rules",
	})
}

func newEMQXJSONResource(spec emqxJSONResourceSpec) *emqxJSONResource {
	return &emqxJSONResource{
		spec:         spec,
		pollInterval: defaultEMQXPollInterval,
		timeout:      defaultEMQXTimeout,
	}
}

func (r *emqxJSONResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_" + r.spec.typeSuffix
}

func (r *emqxJSONResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	requiresReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attributes := map[string]schema.Attribute{
		"config_json": schema.StringAttribute{
			Required:    true,
			Sensitive:   true,
			Description: "JSON object containing the resource configuration.",
		},
	}
	if r.spec.named {
		attributes["type"] = schema.StringAttribute{
			Required:      true,
			PlanModifiers: requiresReplace,
			Description:   "Deployment API resource type.",
		}
		attributes["name"] = schema.StringAttribute{
			Required:      true,
			PlanModifiers: requiresReplace,
			Description:   "Deployment API resource name.",
		}
	} else {
		attributes["rule_id"] = schema.StringAttribute{
			Required:      true,
			PlanModifiers: requiresReplace,
			Description:   "EMQX rule identifier.",
		}
	}
	response.Schema = schema.Schema{
		Description: "Manage one EMQX v5 " + r.spec.typeSuffix + " using generic JSON.",
		Attributes:  attributes,
	}
}

func (r *emqxJSONResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Config)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}

	if r.spec.named {
		validateNonEmptyString(state.Type, "type", &response.Diagnostics)
		validateNonEmptyString(state.Name, "name", &response.Diagnostics)
	} else {
		validateNonEmptyString(state.RuleID, "rule_id", &response.Diagnostics)
	}
	if state.ConfigJSON.IsNull() || state.ConfigJSON.IsUnknown() {
		return
	}
	config, err := parseJSONObject(state.ConfigJSON.ValueString())
	if err != nil {
		response.Diagnostics.AddAttributeError(
			path.Root("config_json"),
			"Invalid JSON configuration",
			err.Error(),
		)
		return
	}
	for _, identityField := range r.identityFields() {
		if _, exists := config[identityField]; exists {
			response.Diagnostics.AddAttributeError(
				path.Root("config_json"),
				"Duplicated identity field",
				fmt.Sprintf(
					"config_json must not contain %q; configure it with the resource attribute.",
					identityField,
				),
			)
		}
	}
	if r.spec.toggle {
		if enabled, exists := config["enable"]; exists {
			if _, ok := enabled.(bool); !ok {
				response.Diagnostics.AddAttributeError(
					path.Root("config_json"),
					"Invalid enable value",
					"config_json enable must be a boolean.",
				)
			}
		}
	}
}

func (r *emqxJSONResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Plan)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	payload, err := r.createPayload(state)
	if err != nil {
		response.Diagnostics.AddError("Invalid JSON configuration", err.Error())
		return
	}
	_, err = r.deployment.Do(ctx, client.Request{
		Method: http.MethodPost,
		Path:   r.spec.collection,
		Body:   payload,
	})
	if err != nil {
		response.Diagnostics.AddError("Unable to create EMQX resource", err.Error())
		return
	}

	// Persist the remote identity before polling so a failed create remains destroyable.
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
	if response.Diagnostics.HasError() {
		return
	}

	projected, err := r.waitForRemote(ctx, state)
	if err != nil {
		response.Diagnostics.AddError("EMQX resource creation did not complete", err.Error())
		return
	}
	state.ConfigJSON = types.StringValue(projected)
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *emqxJSONResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.State)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	remote, exists, err := r.readRemote(ctx, state)
	if err != nil {
		response.Diagnostics.AddError("Unable to read EMQX resource", err.Error())
		return
	}
	if !exists {
		response.State.RemoveResource(ctx)
		return
	}
	projected, _, err := projectVisibleJSON(state.ConfigJSON.ValueString(), remote)
	if err != nil {
		response.Diagnostics.AddError("Unable to project EMQX resource state", err.Error())
		return
	}
	state.ConfigJSON = types.StringValue(projected)
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *emqxJSONResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	state, diagnostics := r.terraformState(ctx, request.Plan)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() || !r.requireConfigured(&response.Diagnostics) {
		return
	}

	payload, enabled, err := r.updatePayload(state)
	if err != nil {
		response.Diagnostics.AddError("Invalid JSON configuration", err.Error())
		return
	}
	itemPath := r.itemPath(state)
	if len(payload) > 0 {
		_, err = r.deployment.Do(ctx, client.Request{
			Method: http.MethodPut,
			Path:   itemPath,
			Body:   payload,
		})
		if err != nil {
			response.Diagnostics.AddError("Unable to update EMQX resource", err.Error())
			return
		}
	}
	if enabled != nil {
		_, err = r.deployment.Do(ctx, client.Request{
			Method: http.MethodPut,
			Path:   itemPath + "/enable/" + strconv.FormatBool(*enabled),
		})
		if err != nil {
			response.Diagnostics.AddError("Unable to change EMQX resource status", err.Error())
			return
		}
	}

	projected, err := r.waitForRemote(ctx, state)
	if err != nil {
		response.Diagnostics.AddError("EMQX resource update did not complete", err.Error())
		return
	}
	state.ConfigJSON = types.StringValue(projected)
	response.Diagnostics.Append(r.setTerraformState(ctx, &response.State, state)...)
}

func (r *emqxJSONResource) Delete(
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
	if client.IsStatus(err, http.StatusNotFound) {
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to delete EMQX resource", err.Error())
	}
}

func (r *emqxJSONResource) terraformState(
	ctx context.Context,
	getter terraformGetter,
) (emqxJSONState, diag.Diagnostics) {
	if r.spec.named {
		var model namedJSONResourceModel
		diagnostics := getter.Get(ctx, &model)
		state := emqxJSONState{
			Type:       model.Type,
			Name:       model.Name,
			ConfigJSON: model.ConfigJSON,
		}
		return state, diagnostics
	}

	var model ruleJSONResourceModel
	diagnostics := getter.Get(ctx, &model)
	state := emqxJSONState{
		RuleID:     model.RuleID,
		ConfigJSON: model.ConfigJSON,
	}
	return state, diagnostics
}

func (r *emqxJSONResource) setTerraformState(
	ctx context.Context,
	state *tfsdk.State,
	resourceState emqxJSONState,
) diag.Diagnostics {
	if r.spec.named {
		model := namedJSONResourceModel{
			Type:       resourceState.Type,
			Name:       resourceState.Name,
			ConfigJSON: resourceState.ConfigJSON,
		}
		return state.Set(ctx, &model)
	}

	model := ruleJSONResourceModel{
		RuleID:     resourceState.RuleID,
		ConfigJSON: resourceState.ConfigJSON,
	}
	return state.Set(ctx, &model)
}

func (r *emqxJSONResource) createPayload(state emqxJSONState) (map[string]any, error) {
	payload, err := parseJSONObject(state.ConfigJSON.ValueString())
	if err != nil {
		return nil, err
	}
	for _, identityField := range r.identityFields() {
		if _, exists := payload[identityField]; exists {
			return nil, fmt.Errorf("config_json must not contain identity field %q", identityField)
		}
	}
	if r.spec.named {
		payload["type"] = state.Type.ValueString()
		payload["name"] = state.Name.ValueString()
	} else {
		payload["id"] = state.RuleID.ValueString()
	}
	return payload, nil
}

func (r *emqxJSONResource) updatePayload(
	state emqxJSONState,
) (map[string]any, *bool, error) {
	payload, err := parseJSONObject(state.ConfigJSON.ValueString())
	if err != nil {
		return nil, nil, err
	}
	for _, identityField := range r.identityFields() {
		if _, exists := payload[identityField]; exists {
			return nil, nil, fmt.Errorf(
				"config_json must not contain identity field %q",
				identityField,
			)
		}
	}
	if !r.spec.toggle {
		return payload, nil, nil
	}

	var enabled *bool
	if rawEnabled, exists := payload["enable"]; exists {
		enabledValue, ok := rawEnabled.(bool)
		if !ok {
			return nil, nil, errors.New("config_json enable must be a boolean")
		}
		enabled = &enabledValue
		delete(payload, "enable")
	}
	return payload, enabled, nil
}

func (r *emqxJSONResource) readRemote(
	ctx context.Context,
	state emqxJSONState,
) (map[string]any, bool, error) {
	var rawResponse json.RawMessage
	_, err := r.deployment.Do(ctx, client.Request{
		Method: http.MethodGet,
		Path:   r.itemPath(state),
		Result: &rawResponse,
	})
	if client.IsStatus(err, http.StatusNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	remote, err := parseJSONObject(string(rawResponse))
	if err != nil {
		return nil, false, fmt.Errorf("decode EMQX resource: %w", err)
	}
	return remote, true, nil
}

func (r *emqxJSONResource) waitForRemote(
	ctx context.Context,
	state emqxJSONState,
) (string, error) {
	timeoutContext, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	for {
		remote, exists, err := r.readRemote(timeoutContext, state)
		if err != nil {
			return "", err
		}
		if exists {
			projected, matches, err := projectVisibleJSON(
				state.ConfigJSON.ValueString(),
				remote,
			)
			if err != nil {
				return "", err
			}
			if matches {
				return projected, nil
			}
		}

		select {
		case <-timeoutContext.Done():
			return "", fmt.Errorf("wait for EMQX resource: %w", timeoutContext.Err())
		case <-time.After(r.pollInterval):
		}
	}
}

func (r *emqxJSONResource) itemPath(state emqxJSONState) string {
	return r.spec.collection + "/" + client.EscapePathSegment(r.identity(state))
}

func (r *emqxJSONResource) identity(state emqxJSONState) string {
	if r.spec.named {
		return state.Type.ValueString() + ":" + state.Name.ValueString()
	}
	return state.RuleID.ValueString()
}

func (r *emqxJSONResource) identityFields() []string {
	if r.spec.named {
		return []string{"type", "name"}
	}
	return []string{"id"}
}

func parseJSONObject(input string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(input))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("config_json must contain valid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("config_json must be a JSON object")
	}
	return object, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config_json contains invalid trailing data: %w", err)
	}
	return errors.New("config_json must contain exactly one JSON object")
}

func projectVisibleJSON(configJSON string, remote map[string]any) (string, bool, error) {
	configured, err := parseJSONObject(configJSON)
	if err != nil {
		return "", false, err
	}
	projected := projectVisibleValue(configured, remote).(map[string]any)
	if reflect.DeepEqual(configured, projected) {
		return configJSON, true, nil
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return "", false, fmt.Errorf("encode projected config_json: %w", err)
	}
	return string(encoded), false, nil
}

func projectVisibleValue(configured any, remote any) any {
	if remoteString, ok := remote.(string); ok && remoteString == maskedValue {
		return configured
	}

	configuredObject, configuredIsObject := configured.(map[string]any)
	remoteObject, remoteIsObject := remote.(map[string]any)
	if configuredIsObject && remoteIsObject {
		projected := make(map[string]any, len(configuredObject))
		for key, configuredValue := range configuredObject {
			remoteValue, exists := remoteObject[key]
			if !exists {
				projected[key] = configuredValue
				continue
			}
			projected[key] = projectVisibleValue(configuredValue, remoteValue)
		}
		return projected
	}

	configuredList, configuredIsList := configured.([]any)
	remoteList, remoteIsList := remote.([]any)
	if configuredIsList && remoteIsList {
		projected := make([]any, len(remoteList))
		for index, remoteValue := range remoteList {
			if index < len(configuredList) {
				projected[index] = projectVisibleValue(configuredList[index], remoteValue)
				continue
			}
			projected[index] = remoteValue
		}
		return projected
	}
	return remote
}

func deploymentClientFromProviderData(data any, diagnostics interface {
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
	return providerData.Deployment
}
