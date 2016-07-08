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
	loadBalancerMinimumPriority = 500
	loadBalancerMaximumPriority = 4096
)

// returns the full identifier of a machine
func (az *AzureCloud) getMachineID(machineName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		machineName)
}

// returns the full identifier of a loadbalancer frontendipconfiguration.
func (az *AzureCloud) getFrontendIPConfigID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		backendPoolName)
}

// returns the full identifier of a loadbalancer backendpool.
func (az *AzureCloud) getBackendPoolID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		backendPoolName)
}

// returns the full identifier of a loadbalancer rule.
func (az *AzureCloud) getLoadBalancerRuleID(lbName, lbRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/loadBalancingRules/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		lbRuleName)
}

// returns the full identifier of a loadbalancer probe.
func (az *AzureCloud) getLoadBalancerProbeID(lbName, lbRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/probes/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		lbName,
		lbRuleName)
}

// returns the full identifier of a network security group security rule.
func (az *AzureCloud) getSecurityRuleID(securityRuleName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/%s/securityRules/%s",
		az.SubscriptionID,
		az.ResourceGroup,
		az.SecurityGroupName,
		securityRuleName)
}

// returns the deepest child's identifier from a full identifier string.
func getLastSegment(ID string) string {
	parts := strings.Split(ID, "/")
	name := parts[len(parts)-1]

	return name
}

// returns the equivalent LoadBalancerRule, SecurityRule and LoadBalancerProbe
// protocol types for the given Kubernetes protocol type.
func getProtosFromKubeProto(protocol api.Protocol) (network.TransportProtocol, network.SecurityRuleProtocol, network.ProbeProtocol, error) {
	if protocol == api.ProtocolTCP {
		return network.TransportProtocolTCP, network.TCP, network.ProbeProtocolTCP, nil
	} else if protocol == api.ProtocolUDP {
		return network.TransportProtocolUDP, network.UDP, network.ProbeProtocolTCP, nil
	} else {
		return "", "", "", fmt.Errorf("Unknown Protocol was requested")
	}
}

// This returns the full identifier of the primary NIC for the given VM.
func getPrimaryNicID(machine compute.VirtualMachine) (string, error) {
	var nicRef *compute.NetworkInterfaceReference

	if len(*machine.Properties.NetworkProfile.NetworkInterfaces) == 1 {
		nicRef = &(*machine.Properties.NetworkProfile.NetworkInterfaces)[0]
	} else {
		for _, ref := range *machine.Properties.NetworkProfile.NetworkInterfaces {
			if *ref.Properties.Primary {
				nicRef = &ref
				break
			}
		}
	}

	if nicRef == nil {
		return "", fmt.Errorf("failed to find a primary nic for the vm. vmname=%q", *machine.Name)
	}

	return *nicRef.ID, nil
}

// This returns the full identifier of the primary ipconfig for a given NIC.
func getPrimaryIPConfig(nic network.Interface) (*network.InterfaceIPConfiguration, error) {
	var ipconfigRef *network.InterfaceIPConfiguration
	if len(*nic.Properties.IPConfigurations) == 1 {
		ipconfigRef = &((*nic.Properties.IPConfigurations)[0])
	} else {
		// we're hosed here because of an Azure bug:
		// https://github.com/Azure/azure-rest-api-specs/issues/305
		return nil, fmt.Errorf("cannot determine primary ipconfig")
	}

	if ipconfigRef == nil {
		return nil, fmt.Errorf("failed to find a primary ipconfig for the nic. nicname=%q", *nic.Name)
	}

	return ipconfigRef, nil
}

// This returns the name of the loadbalancer to expect/create.
// This is scoped at the cluster(Name) level because Azure enforces a 1:1:1 relationship
// between a VM, a LB BackendPool, and the LoadBalancer itself. This means we have one
// Azure LoadBalancer object shared across the cluster.
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

// This returns a human-readable version of the Service used to tag some resources.
// This is only used for human-readable convenience, and not to filter.
func getServiceName(service *api.Service) string {
	return fmt.Sprintf("%s/%s", service.Namespace, service.Name)
}

// This returns a prefix for loadbalancer/security rules.
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

// This returns the next available rule priority level for a given set of security rules.
func getNextAvailablePriority(rules []network.SecurityRule) (int32, error) {
	var smallest int32 = loadBalancerMinimumPriority
	var spread int32 = 1

outer:
	for smallest < loadBalancerMaximumPriority {
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
