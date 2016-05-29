package azure

import (
	"fmt"
	"net/http"
	"strings"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/cloudprovider"

	"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/go-autorest/autorest"
)

const (
	// Actually is this right?
	// Portal says starts at 100...
	// TODO(azure):
	// Siva says n->50000 which isn't right either
	LOADBALANCER_PRIORITY_MIN = 500
	LOADBALANCER_PRIORITY_MAX = 4096
)

func (az *AzureCloud) getMachineID(machineName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		machineName)
}

func (az *AzureCloud) getFrontendIPConfigID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		backendPoolName)
}

func (az *AzureCloud) getBackendPoolID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		backendPoolName)
}

func (az *AzureCloud) getLoadBalancerRuleID(lbName, lbRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/loadBalancingRules/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		lbRuleName)
}

func (az *AzureCloud) getLoadBalancerProbeID(lbName, lbRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/probes/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		lbRuleName)
}

func (az *AzureCloud) getSecurityRuleID(securityRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/%s/securityRules/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		az.SecurityGroupName,
		securityRuleName)
}

func getLastSegment(ID string) string {
	parts := strings.Split(ID, "/")
	name := parts[len(parts)-1]

	return name
}

func getProtosFromKubeProto(protocol api.Protocol) (network.TransportProtocol, network.SecurityRuleProtocol, network.ProbeProtocol, error) {
	if protocol == api.ProtocolTCP {
		return network.TransportProtocolTCP, network.TCP, network.ProbeProtocolTCP, nil
	} else if protocol == api.ProtocolUDP {
		return network.TransportProtocolUDP, network.UDP, network.ProbeProtocolTCP, nil
	} else {
		return "", "", "", fmt.Errorf("Unknown Protocol was requested")
	}
}

// TODO: https://github.com/Azure/azure-sdk-for-go/issues/259
// TODO: https://github.com/Azure/azure-rest-api-specs/issues/305
func getPrimaryNicID(machine compute.VirtualMachine) string {
	var nicRef *compute.NetworkInterfaceReference
	//for _, ref := range *machine.Properties.NetworkProfile.NetworkInterfaces {
	//	if *ref.Properties.Primary {
	//		nicRef = ref
	//		break
	//	}
	//}
	//if nicRef == nil {
	//	return nil, fmt.Errorf("failed to find a primary nic for the vm. vmname=%q", host)
	//}

	nicRef = &(*machine.Properties.NetworkProfile.NetworkInterfaces)[0]
	return *nicRef.ID
}

func getPrimaryIPConfig(nic network.Interface) *network.InterfaceIPConfiguration {
	return &((*nic.Properties.IPConfigurations)[0])
}

func getLoadBalancerName(clusterName string) string {
	return clusterName
}

func getBackendPoolName(clusterName string) string {
	return clusterName
}

func getRuleName(service *api.Service, port api.ServicePort) string {
	return fmt.Sprintf("%s-%s-%d-%d",
		getRulePrefix(service),
		port.Protocol, port.Port, port.NodePort)
}

func getRulePrefix(service *api.Service) string {
	return cloudprovider.GetLoadBalancerName(service)
}

func serviceOwnsRule(service *api.Service, rule string) bool {
	prefix := getRulePrefix(service)
	return strings.HasPrefix(strings.ToUpper(rule), strings.ToUpper(prefix))
}

func getFrontendIPConfigName(service *api.Service) string {
	return cloudprovider.GetLoadBalancerName(service)
}

func getPublicIPName(clusterName string, service *api.Service) string {
	return fmt.Sprintf("%s-%s", clusterName, cloudprovider.GetLoadBalancerName(service))
}

func getNextAvailablePriority(rules []network.SecurityRule) (int32, error) {
	var smallest int32 = LOADBALANCER_PRIORITY_MIN
	var spread int32 = 1

outer:
	for smallest < LOADBALANCER_PRIORITY_MAX {
		for _, rule := range rules {
			if *rule.Properties.Priority == smallest {
				smallest += spread
				continue outer
			}
		}
		// no one else had it
		return smallest, nil
	}

	return -1, fmt.Errorf("azurecp:loadbalancer: out of nsg priorities")
}

// checkExistsFromError inspects an error and returns a true if err is nil,
// false if error is an autorest.Error with StatusCode=404 and will return the
// error back if error is another status code or another type of error.
func checkResourceExistsFromError(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	v, ok := err.(autorest.DetailedError)
	if ok && v.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, v
}
