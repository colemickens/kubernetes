package azure

import (
	"fmt"

	"k8s.io/kubernetes/pkg/cloudprovider"

	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/glog"
)

func (az *AzureCloud) ListRoutes(clusterName string) (routes []*cloudprovider.Route, err error) {
	glog.Infof("list: START clusterName=%q", clusterName)
	routeTableName := getRouteTableName(clusterName)

	glog.Infof("list: getting the route table. routeTableName=%q", routeTableName)
	routeTable, err := az.RouteTablesClient.Get(az.ResourceGroup, routeTableName, "")
	if existsRouteTable, err := checkResourceExistsFromError(err); err != nil {
		return nil, err
	} else if !existsRouteTable {
		glog.Infof("list: routing table didn't exist. routeTableName=%q", routeTableName)
		return []*cloudprovider.Route{}, nil
	}

	var kubeRoutes []*cloudprovider.Route
	if routeTable.Properties.Routes != nil {
		kubeRoutes = make([]*cloudprovider.Route, len(*routeTable.Properties.Routes))
		for i, route := range *routeTable.Properties.Routes {
			instance := getInstanceName(*route.Name)
			cidr := *route.Properties.AddressPrefix
			glog.Infof("list: * instance=%q, cidr=%q", instance, cidr)

			kubeRoutes[i] = &cloudprovider.Route{
				Name:            *route.Name,
				TargetInstance:  instance,
				DestinationCIDR: cidr,
			}
		}
	} else {
		kubeRoutes = []*cloudprovider.Route{}
	}

	glog.Info("list: FINISH")
	return kubeRoutes, nil
}

func (az *AzureCloud) CreateRoute(clusterName string, nameHint string, kubeRoute *cloudprovider.Route) error {
	glog.Infof("create: clusterName=%q instance=%q cidr=%q", clusterName, kubeRoute.TargetInstance, kubeRoute.DestinationCIDR)

	routeTableName := getRouteTableName(clusterName)

	glog.Infof("create: getting the routetable. routeTableName=%q", routeTableName)
	routeTable, err := az.RouteTablesClient.Get(az.ResourceGroup, routeTableName, "")
	if existsRouteTable, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if !existsRouteTable {
		glog.Infof("create: routetable needs creation. routeTableName=%q", routeTableName)
		routeTable = network.RouteTable{
			Name:       to.StringPtr(routeTableName),
			Location:   to.StringPtr(az.Location),
			Properties: &network.RouteTablePropertiesFormat{},
		}

		_, err = az.RouteTablesClient.CreateOrUpdate(az.ResourceGroup, routeTableName, routeTable, nil)
		if err != nil {
			return err
		}

		routeTable, err = az.RouteTablesClient.Get(az.ResourceGroup, routeTableName, "")
		if err != nil {
			return err
		}
	}

	// ensure the subnet is properly configured
	glog.Infof("create: getting the subnet. vnet=%q subnet=%q", az.VnetName, az.SubnetName)
	subnet, err := az.SubnetsClient.Get(az.ResourceGroup, az.VnetName, az.SubnetName, "")
	if existsSubnet, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if !existsSubnet {
		glog.Infof("create: subnet was unexpectedly nil! vnet=%q subnet=%q", az.VnetName, az.SubnetName)
		return fmt.Errorf("failed to retrieve subnet")
	}
	if subnet.Properties.RouteTable != nil {
		if *subnet.Properties.RouteTable.ID == *routeTable.ID {
			glog.Infof("create: subnet is already correctly configured")
		} else {
			glog.Errorf("create: subnet has wrong routetable. active_routetable=%q expected_routetable=%q", *subnet.Properties.RouteTable.ID, *routeTable.ID)
			return fmt.Errorf("The subnet has a route table, but it was unrecognized. Refusing to modify it.")
		}
	} else {
		subnet.Properties.RouteTable = &network.RouteTable{
			ID: routeTable.ID,
		}
		glog.Info("create: updating subnet")
		_, err = az.SubnetsClient.CreateOrUpdate(az.ResourceGroup, az.VnetName, az.SubnetName, subnet, nil)
		if err != nil {
			return err
		}
	}

	targetIP, err := az.getIPForMachine(kubeRoute.TargetInstance)
	if err != nil {
		return err
	}

	routeName := getRouteName(kubeRoute.TargetInstance)
	route := network.Route{
		Name: to.StringPtr(routeName),
		Properties: &network.RoutePropertiesFormat{
			AddressPrefix:    to.StringPtr(kubeRoute.DestinationCIDR),
			NextHopType:      network.RouteNextHopTypeVirtualAppliance,
			NextHopIPAddress: to.StringPtr(targetIP),
		},
	}

	glog.Infof("create: creating route: instance=%q cidr=%q", kubeRoute.TargetInstance, kubeRoute.DestinationCIDR)
	_, err = az.RoutesClient.CreateOrUpdate(az.ResourceGroup, routeTableName, *route.Name, route, nil)
	if err != nil {
		return err
	}

	glog.Info("create: FINISH")
	return nil
}

func (az *AzureCloud) DeleteRoute(clusterName string, kubeRoute *cloudprovider.Route) error {
	glog.Info("delete: START")

	routeTableName := getRouteTableName(clusterName)
	routeName := getRouteName(kubeRoute.TargetInstance)
	_, err := az.RoutesClient.Delete(az.ResourceGroup, routeTableName, routeName, nil)
	if err != nil {
		return err
	}

	glog.Info("delete: FINISH")
	return nil
}

func getRouteTableName(clusterName string) string {
	return fmt.Sprintf("%s", clusterName)
}

func getRouteName(instanceName string) string {
	return fmt.Sprintf("%s", instanceName)
}

func getInstanceName(routeName string) string {
	return fmt.Sprintf("%s", routeName)
}

func (az *AzureCloud) getIPForMachine(machineName string) (string, error) {
	machine, err := az.VirtualMachinesClient.Get(
		az.ResourceGroup,
		machineName,
		"")
	if existsMachine, err := checkResourceExistsFromError(err); err != nil {
		return "", err
	} else if !existsMachine {
		return "", fmt.Errorf("create: target vm didn't exist")
	}

	nicID := getPrimaryNicID(machine)
	nicName := getLastSegment(nicID)

	nic, err := az.InterfacesClient.Get(
		az.ResourceGroup,
		nicName,
		"")
	if existsNic, err := checkResourceExistsFromError(err); err != nil {
		return "", err
	} else if !existsNic {
		return "", fmt.Errorf("create: failed to lookup nic")
	}

	ipConfig := getPrimaryIPConfig(nic)
	targetIP := *ipConfig.Properties.PrivateIPAddress
	return targetIP, nil
}
