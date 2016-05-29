package azure

import (
	"encoding/json"
	"io"

	"k8s.io/kubernetes/pkg/cloudprovider"

	"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/golang/glog"
)

const AzureProviderName = "azure"

type AzureConfig struct {
	Cloud             string `json:"cloud"`
	TenantID          string `json:"tenantId"`
	SubscriptionID    string `json:"subscriptionId"`
	ResourceGroup     string `json:"resourceGroup"`
	Location          string `json:"location"`
	VnetName          string `json:"vnetName"`
	SubnetName        string `json:"subnetName"`
	SecurityGroupName string `json:"securityGroupName"`

	AdClientID     string `json:"adClientId"`
	AdClientSecret string `json:"adClientSecret"`
	AdTenantID     string `json:"adTenantId"`
}

type AzureCloud struct {
	AzureConfig
	Environment             azure.Environment
	RoutesClient            network.RoutesClient
	SubnetsClient           network.SubnetsClient
	InterfacesClient        network.InterfacesClient
	RouteTablesClient       network.RouteTablesClient
	LoadBalancerClient      network.LoadBalancersClient
	PublicIPAddressesClient network.PublicIPAddressesClient
	SecurityGroupsClient    network.SecurityGroupsClient
	VirtualMachinesClient   compute.VirtualMachinesClient
}

func init() {
	cloudprovider.RegisterCloudProvider(AzureProviderName, func(configReader io.Reader) (cloudprovider.Interface, error) {
		var az AzureCloud
		err := json.NewDecoder(configReader).Decode(&az)
		if err != nil {
			glog.Errorf("azurecp:init: failed to load config")
			return nil, err
		}

		switch az.Cloud {
		case "fairfax":
			az.Environment = azure.USGovernmentCloud
		case "mooncake":
			az.Environment = azure.ChinaCloud
		case "public":
		default:
			az.Environment = azure.PublicCloud
		}

		oauthConfig, err := az.Environment.OAuthConfigForTenant(az.TenantID)
		if err != nil {
			glog.Errorf("azurecp:init: failed to determine oauth configuration")
			return nil, err
		}

		servicePrincipalToken, err := azure.NewServicePrincipalToken(
			*oauthConfig,
			az.AdClientID,
			az.AdClientSecret,
			az.Environment.ServiceManagementEndpoint)
		if err != nil {
			glog.Errorf("azurecp:init: failed to create service principal token")
			return nil, err
		}

		az.SubnetsClient = network.NewSubnetsClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.SubnetsClient.Authorizer = servicePrincipalToken

		az.RouteTablesClient = network.NewRouteTablesClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.RouteTablesClient.Authorizer = servicePrincipalToken

		az.InterfacesClient = network.NewInterfacesClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.InterfacesClient.Authorizer = servicePrincipalToken

		az.RoutesClient = network.NewRoutesClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.RoutesClient.Authorizer = servicePrincipalToken

		az.LoadBalancerClient = network.NewLoadBalancersClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.LoadBalancerClient.Authorizer = servicePrincipalToken

		az.VirtualMachinesClient = compute.NewVirtualMachinesClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.VirtualMachinesClient.Authorizer = servicePrincipalToken

		az.PublicIPAddressesClient = network.NewPublicIPAddressesClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.PublicIPAddressesClient.Authorizer = servicePrincipalToken

		az.SecurityGroupsClient = network.NewSecurityGroupsClientWithBaseURI(
			az.Environment.ResourceManagerEndpoint, az.SubscriptionID)
		az.SecurityGroupsClient.Authorizer = servicePrincipalToken

		return &az, nil
	})
}

func (az *AzureCloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	//return nil, false
	return az, true
}

func (az *AzureCloud) Instances() (cloudprovider.Instances, bool) {
	return az, true
}

func (az *AzureCloud) Zones() (cloudprovider.Zones, bool) {
	return az, true
}

func (az *AzureCloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

func (az *AzureCloud) Routes() (cloudprovider.Routes, bool) {
	return az, true
}

func (az *AzureCloud) ScrubDNS(nameservers, searches []string) (nsOut, srchOut []string) {
	return nameservers, searches
}

func (az *AzureCloud) ProviderName() string {
	return AzureProviderName
}
