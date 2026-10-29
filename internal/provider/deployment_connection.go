package provider

import (
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type deploymentConnectionAPIModel struct {
	Address     string                   `json:"address"`
	APIEndpoint string                   `json:"apiEndpoint"`
	Ports       []deploymentPortAPIModel `json:"ports"`
}

type deploymentPortAPIModel struct {
	Protocol string `json:"protocol"`
	Port     *int64 `json:"port"`
	Status   string `json:"status"`
}

var deploymentPortTypes = map[string]attr.Type{
	"protocol": types.StringType,
	"port":     types.Int64Type,
	"status":   types.StringType,
}

var deploymentConnectionTypes = map[string]attr.Type{
	"address":      types.StringType,
	"api_endpoint": types.StringType,
	"ports":        types.ListType{ElemType: types.ObjectType{AttrTypes: deploymentPortTypes}},
}

func terraformDeploymentConnection(connection *deploymentConnectionAPIModel) types.Object {
	if connection == nil {
		return types.ObjectNull(deploymentConnectionTypes)
	}
	ports := append([]deploymentPortAPIModel(nil), connection.Ports...)
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		if ports[i].Port == nil {
			return ports[j].Port != nil
		}
		return ports[j].Port != nil && *ports[i].Port < *ports[j].Port
	})
	values := make([]attr.Value, 0, len(ports))
	for _, port := range ports {
		number := types.Int64Null()
		if port.Port != nil {
			number = types.Int64Value(*port.Port)
		}
		values = append(values, types.ObjectValueMust(deploymentPortTypes, map[string]attr.Value{
			"protocol": connectionStringValue(port.Protocol),
			"port":     number,
			"status":   connectionStringValue(port.Status),
		}))
	}
	portList := types.ListNull(types.ObjectType{AttrTypes: deploymentPortTypes})
	if connection.Ports != nil {
		portList = types.ListValueMust(types.ObjectType{AttrTypes: deploymentPortTypes}, values)
	}
	return types.ObjectValueMust(deploymentConnectionTypes, map[string]attr.Value{
		"address":      connectionStringValue(connection.Address),
		"api_endpoint": connectionStringValue(connection.APIEndpoint),
		"ports":        portList,
	})
}

func connectionStringValue(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}
