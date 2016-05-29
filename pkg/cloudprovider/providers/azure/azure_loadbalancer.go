package azure

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/kubernetes/pkg/api"
	// "k8s.io/kubernetes/pkg/cloudprovider"

	//"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/glog"
)

func (az *AzureCloud) GetLoadBalancer(clusterName string, service *api.Service) (status *api.LoadBalancerStatus, exists bool, err error) {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	glog.Infof("get: START clusterName=%q lbName=%q serviceName=%q pipName=%q", clusterName, lbName, service.Name, pipName)

	_, err = az.LoadBalancerClient.Get(az.ResourceGroup, lbName, "")
	if existsLb, err := checkResourceExistsFromError(err); err != nil {
		glog.Errorf("get: FAIL error getting loadbalancer. err=%q", lbName, err)
		return nil, false, err
	} else if !existsLb {
		glog.Errorf("get: FINISH loadbalancer didn't exist lbName=%q", lbName)
		return nil, false, nil
	}

	pip, err := az.PublicIPAddressesClient.Get(az.ResourceGroup, pipName, "")
	if existsLbPip, err := checkResourceExistsFromError(err); err != nil {
		glog.Infof("get: FAIL error getting public-ip. pipName=%q err=%q", pipName, err)
		return nil, false, err
	} else if !existsLbPip {
		glog.Errorf("get: FINISH public-ip didn't exist. pipName=%q", pipName)
		return nil, false, nil
	}

	glog.Info("get: FINISH service=%q lbName=%q", service.Name, lbName)
	return &api.LoadBalancerStatus{
		Ingress: []api.LoadBalancerIngress{{IP: *pip.Properties.IPAddress}},
	}, true, nil
}

func (az *AzureCloud) EnsureLoadBalancer(clusterName string, service *api.Service, hosts []string) (*api.LoadBalancerStatus, error) {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	glog.Infof("ensure: START clusterName=%q lbName=%q serviceName=%q pipName=%q len(hosts)=%q", clusterName, lbName, service.Name, pipName, len(hosts))

	pip, err := az.ensurePublicIPExists(pipName)
	if err != nil {
		return nil, err
	}

	glog.Info("ensure: getting security group")
	sg, err := az.SecurityGroupsClient.Get(az.ResourceGroup, az.SecurityGroupName, "")
	if err != nil {
		return nil, err
	}
	sg, sgNeedsUpdate, err := az.reconcileSecurityGroup(sg, clusterName, service)
	if err != nil {
		return nil, err
	}
	if sgNeedsUpdate {
		_, err := az.SecurityGroupsClient.CreateOrUpdate(az.ResourceGroup, *sg.Name, sg, nil)
		if err != nil {
			return nil, fmt.Errorf("ensure: failed to update security group. err=%q", err)
		}
	}

	glog.Info("ensure: getting loadbalancer")
	lbNeedsCreate := false
	lb, err := az.LoadBalancerClient.Get(az.ResourceGroup, lbName, "")
	if existsLb, err := checkResourceExistsFromError(err); err != nil {
		return nil, err
	} else if !existsLb {
		lb = network.LoadBalancer{
			Name:       &lbName,
			Location:   &az.Location,
			Properties: &network.LoadBalancerPropertiesFormat{},
		}
		lbNeedsCreate = true
		glog.Info("ensure: loadbalancer needs creation")
	}

	lb, lbNeedsUpdate, err := az.reconcileLoadBalancer(lb, pip, clusterName, service, hosts)
	if err != nil {
		return nil, err
	}
	if lbNeedsCreate || lbNeedsUpdate {
		glog.Info("ensure: createOrUpdating loadbalancer")
		_, err = az.LoadBalancerClient.CreateOrUpdate(az.ResourceGroup, *lb.Name, lb, nil)
		if err != nil {
			return nil, err
		}
	}

	// Add the machines to the backend pool if they're not already
	// TODO: handle node lb pool eviction, but how?
	lbBackendName := getBackendPoolName(clusterName)
	lbBackendPoolID := az.getBackendPoolID(lbName, lbBackendName)
	for _, host := range hosts {
		// TODO: parallelize this
		err = az.ensureHostInPool(host, lbBackendPoolID)
		if err != nil {
			return nil, err
		}
	}

	glog.Infof("ensure: FINISH service=%q, pip=%q", service.Name, *pip.Properties.IPAddress)
	return &api.LoadBalancerStatus{
		Ingress: []api.LoadBalancerIngress{{IP: *pip.Properties.IPAddress}},
	}, nil
}

func (az *AzureCloud) UpdateLoadBalancer(clusterName string, service *api.Service, hosts []string) error {
	glog.Infof("update: START clusterName=%q serviceName=%q len(hosts)=%q", clusterName, service.Name, len(hosts))

	_, err := az.EnsureLoadBalancer(clusterName, service, hosts)

	glog.Info("update: FINISH")
	return err
}

func (az *AzureCloud) EnsureLoadBalancerDeleted(clusterName string, service *api.Service) error {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	glog.Infof("delete: START clusterName=%q lbName=%q serviceName=%q pipName=%q", clusterName, lbName, service.Name, pipName)

	// reconcile logic is capable of fully reconcile, so we can use this to delete
	service.Spec.Ports = []api.ServicePort{}

	lb, err := az.LoadBalancerClient.Get(az.ResourceGroup, lbName, "")
	if existsLb, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if existsLb {
		lb, lbNeedsUpdate, err := az.reconcileLoadBalancer(lb, nil, clusterName, service, []string{})
		if err != nil {
			return err
		}
		if lbNeedsUpdate {
			if len(*lb.Properties.FrontendIPConfigurations) > 0 {
				// if we have no more frontend ip configs, we need to remove the whole load balancer
				_, err = az.LoadBalancerClient.CreateOrUpdate(az.ResourceGroup, *lb.Name, lb, nil)
				if err != nil {
					return err
				}
			} else {
				_, err = az.LoadBalancerClient.Delete(az.ResourceGroup, lbName, nil)
				if err != nil {
					return err
				}
			}
		}
	}

	sg, err := az.SecurityGroupsClient.Get(az.ResourceGroup, az.SecurityGroupName, "")
	if existsSg, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if existsSg {
		sg, sgNeedsUpdate, err := az.reconcileSecurityGroup(sg, clusterName, service)
		if err != nil {
			return err
		}
		if sgNeedsUpdate {
			_, err := az.SecurityGroupsClient.CreateOrUpdate(az.ResourceGroup, *sg.Name, sg, nil)
			if err != nil {
				return fmt.Errorf("ensure: failed to update security group. err=%q", err)
			}
		}
	}

	err = az.ensurePublicIPDeleted(pipName)
	if err != nil {
		return fmt.Errorf("delete: failed to remove public-ip: %q. err=%q", pipName, err)
	}

	glog.Info("delete: FINISH")
	return nil
}

func (az *AzureCloud) ensurePublicIPExists(pipName string) (*network.PublicIPAddress, error) {
	pip, err := az.PublicIPAddressesClient.Get(az.ResourceGroup, pipName, "")
	if existsPip, err := checkResourceExistsFromError(err); err != nil {
		return nil, err
	} else if existsPip {
		return &pip, nil
	} else {
		pip.Name = to.StringPtr(pipName)
		pip.Location = to.StringPtr(az.Location)
		pip.Properties = &network.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: network.Static,
		}

		glog.Infof("ensure: creating public-ip: %q", *pip.Name)
		_, err = az.PublicIPAddressesClient.CreateOrUpdate(az.ResourceGroup, *pip.Name, pip, nil)
		if err != nil {
			return nil, err
		}

		glog.Infof("ensure: retrieving public-ip: %q", *pip.Name)
		pip, err = az.PublicIPAddressesClient.Get(az.ResourceGroup, *pip.Name, "")
		if err != nil {
			return nil, err
		}

		return &pip, nil
	}
}

func (az *AzureCloud) ensurePublicIPDeleted(pipName string) error {
	_, err := az.PublicIPAddressesClient.Delete(az.ResourceGroup, pipName, nil)
	if _, err := checkResourceExistsFromError(err); err != nil {
		return err
	}
	return nil
}

// this ensures load balancer exists and the ip config is setup
func (az *AzureCloud) reconcileLoadBalancer(lb network.LoadBalancer, pip *network.PublicIPAddress, clusterName string, service *api.Service, hosts []string) (network.LoadBalancer, bool, error) {
	lbName := getLoadBalancerName(clusterName)
	lbFrontendIPConfigName := getFrontendIPConfigName(service)
	lbFrontendIPConfigID := az.getFrontendIPConfigID(lbName, lbFrontendIPConfigName)
	lbBackendPoolName := getBackendPoolName(clusterName)
	lbBackendPoolID := az.getBackendPoolID(lbName, lbBackendPoolName)

	wantLb := (pip != nil)
	dirtyLb := false

	// Ensure LoadBalancer's Backend Pool Configuration
	if lb.Properties.BackendAddressPools == nil ||
		len(*lb.Properties.BackendAddressPools) == 0 {
		lb.Properties.BackendAddressPools = &[]network.BackendAddressPool{
			network.BackendAddressPool{
				Name: to.StringPtr(lbBackendPoolName),
			},
		}
		glog.Infof("lb backend pool will be updated")
		dirtyLb = true
	} else if len(*lb.Properties.BackendAddressPools) != 1 ||
		!strings.EqualFold(*(*lb.Properties.BackendAddressPools)[0].ID, lbBackendPoolID) {
		return lb, false, fmt.Errorf("ensure: loadbalancer is already configured with a different backend pool. expected=%q actual=%q", lbBackendPoolID, (*lb.Properties.BackendAddressPools)[0].ID)
	}

	// Ensure LoadBalancer's Frontend IP Configurations
	newConfigs := []network.FrontendIPConfiguration{}
	if lb.Properties.FrontendIPConfigurations != nil {
		newConfigs = *lb.Properties.FrontendIPConfigurations
	}
	if !wantLb {
		for i := len(newConfigs) - 1; i >= 0; i-- {
			config := newConfigs[i]
			if strings.EqualFold(*config.ID, lbFrontendIPConfigID) {
				newConfigs = append(newConfigs[:i],
					newConfigs[i+1:]...)
				dirtyLb = true
			}
		}
	} else {
		foundConfig := false
		for _, config := range newConfigs {
			if strings.EqualFold(*config.ID, lbFrontendIPConfigID) {
				foundConfig = true
				break
			}
		}
		if !foundConfig {
			newConfigs = append(newConfigs,
				network.FrontendIPConfiguration{
					Name: to.StringPtr(lbFrontendIPConfigName),
					Properties: &network.FrontendIPConfigurationPropertiesFormat{
						PublicIPAddress: &network.PublicIPAddress{
							ID: pip.ID,
						},
					},
				})
			dirtyLb = true
		}
	}
	lb.Properties.FrontendIPConfigurations = &newConfigs

	// Ensure Load Balancer Probes and Rules
	expectedProbes := make([]network.Probe, len(service.Spec.Ports))
	expectedRules := make([]network.LoadBalancingRule, len(service.Spec.Ports))
	for i, port := range service.Spec.Ports {
		lbRuleName := getRuleName(service, port)

		transportProto, _, probeProto, err := getProtosFromKubeProto(port.Protocol)
		if err != nil {
			return lb, false, err
		}

		expectedProbes[i] = network.Probe{
			Name: to.StringPtr(lbRuleName),
			Properties: &network.ProbePropertiesFormat{
				Protocol:          probeProto,
				Port:              to.Int32Ptr(port.NodePort),
				IntervalInSeconds: to.Int32Ptr(5),
				NumberOfProbes:    to.Int32Ptr(2),
			},
		}

		expectedRules[i] = network.LoadBalancingRule{
			Name: &lbRuleName,
			Properties: &network.LoadBalancingRulePropertiesFormat{
				Protocol: transportProto,
				FrontendIPConfiguration: &network.SubResource{
					ID: to.StringPtr(lbFrontendIPConfigID),
				},
				BackendAddressPool: &network.SubResource{
					ID: to.StringPtr(lbBackendPoolID),
				},
				Probe: &network.SubResource{
					ID: to.StringPtr(az.getLoadBalancerProbeID(lbName, lbRuleName)),
				},
				FrontendPort: to.Int32Ptr(port.Port),
				BackendPort:  to.Int32Ptr(port.NodePort),
			},
		}
	}

	// remove unwated probes
	updatedProbes := []network.Probe{}
	if lb.Properties.Probes != nil {
		updatedProbes = *lb.Properties.Probes
	}
	for i := len(updatedProbes) - 1; i >= 0; i-- {
		existingProbe := updatedProbes[i]
		if serviceOwnsRule(service, *existingProbe.Name) {
			glog.Infof("reconcile_lb: considering evicting probe. probeName=%q", *existingProbe.Name)
			keepProbe := false
			for _, expectedProbe := range expectedProbes {
				if strings.EqualFold(*existingProbe.ID, az.getLoadBalancerProbeID(lbName, *expectedProbe.Name)) {
					glog.Infof("reconcile_lb: keeping probe. probeName=%q", *existingProbe.Name)
					keepProbe = true
					break
				}
			}
			if !keepProbe {
				updatedProbes = append(updatedProbes[:i], updatedProbes[i+1:]...)
				glog.Infof("reconcile: dropping probe. probeName=%q", *existingProbe.Name)
				dirtyLb = true
			}
		}
	}
	// add missing, wanted probes
	for _, expectedProbe := range expectedProbes {
		foundProbe := false
		for _, existingProbe := range updatedProbes {
			if strings.EqualFold(*existingProbe.ID, az.getLoadBalancerProbeID(lbName, *expectedProbe.Name)) {
				glog.Infof("reconcile_lb: probe already exists. probeName=%q", existingProbe.Name)
				foundProbe = true
				break
			}
		}
		if !foundProbe {
			glog.Infof("reconcile_lb: adding probe. probeName=%q", *expectedProbe.Name)
			updatedProbes = append(updatedProbes, expectedProbe)
			dirtyLb = true
		}
	}
	lb.Properties.Probes = &updatedProbes

	// update rules
	updatedRules := []network.LoadBalancingRule{}
	if lb.Properties.LoadBalancingRules != nil {
		updatedRules = *lb.Properties.LoadBalancingRules
	}
	// remove unwanted rules
	for i := len(updatedRules) - 1; i >= 0; i-- {
		existingRule := updatedRules[i]
		keepRule := false
		if serviceOwnsRule(service, *existingRule.Name) {
			glog.Infof("reconcile_lb: considering evicting rule. ruleName=%q", *existingRule.Name)
			for _, expectedRule := range expectedRules {
				if strings.EqualFold(*existingRule.ID, az.getLoadBalancerRuleID(lbName, *expectedRule.Name)) {
					glog.Infof("reconcile_lb: keeping rule. ruleName=%q", *existingRule.Name)
					keepRule = true
					break
				}
			}
			if !keepRule {
				glog.Infof("reconcile_lb: dropping rule. ruleName=%q", *existingRule.Name)
				updatedRules = append(updatedRules[:i], updatedRules[i+1:]...)
				dirtyLb = true
			}
		}
	}
	// update rules: add needed
	for _, expectedRule := range expectedRules {
		foundRule := false
		for _, existingRule := range updatedRules {
			if strings.EqualFold(*existingRule.ID, az.getLoadBalancerRuleID(lbName, *expectedRule.Name)) {
				glog.Infof("reconcile_lb: rule already exists. ruleName=%q", *existingRule.Name)
				foundRule = true
				break
			}
		}
		if !foundRule {
			glog.Infof("reconcile_lb: adding rule. ruleName=%q", *expectedRule.Name)
			updatedRules = append(updatedRules, expectedRule)
			dirtyLb = true
		}
	}
	lb.Properties.LoadBalancingRules = &updatedRules

	return lb, dirtyLb, nil
}

func (az *AzureCloud) reconcileSecurityGroup(sg network.SecurityGroup, clusterName string, service *api.Service) (network.SecurityGroup, bool, error) {
	expectedSecurityRules := make([]network.SecurityRule, len(service.Spec.Ports))
	for i, port := range service.Spec.Ports {
		securityRuleName := getRuleName(service, port)
		_, securityProto, _, err := getProtosFromKubeProto(port.Protocol)
		if err != nil {
			return sg, false, err
		}

		expectedSecurityRules[i] = network.SecurityRule{
			Name: to.StringPtr(securityRuleName),
			Properties: &network.SecurityRulePropertiesFormat{
				Protocol:                 securityProto,
				SourcePortRange:          to.StringPtr("*"),
				DestinationPortRange:     to.StringPtr(strconv.Itoa(int(port.NodePort))),
				SourceAddressPrefix:      to.StringPtr("Internet"),
				DestinationAddressPrefix: to.StringPtr("*"),
				Access:    network.Allow,
				Direction: network.Inbound,
			},
		}
	}

	// update security rules
	dirtySg := false
	updatedRules := []network.SecurityRule{}
	if sg.Properties.SecurityRules != nil {
		updatedRules = *sg.Properties.SecurityRules
	}
	// update security rules: remove unwanted
	for i := len(updatedRules) - 1; i >= 0; i-- {
		existingRule := updatedRules[i]
		if serviceOwnsRule(service, *existingRule.Name) {
			glog.Infof("reconcile_sg: considering evicting rule. ruleName=%q", *existingRule.Name)
			wantRule := false

			for _, expectedRule := range expectedSecurityRules {
				if strings.EqualFold(*existingRule.ID, az.getSecurityRuleID(*expectedRule.Name)) {
					glog.Infof("reconcile_sg: keeping rule. ruleName=%q", *existingRule.Name)
					wantRule = true
					break
				}
			}
			if !wantRule {
				glog.Infof("reconcile_sg: dropping rule. ruleName=%q", *existingRule.Name)
				updatedRules = append(updatedRules[:i], updatedRules[i+1:]...)
				dirtySg = true
			}
		}
	}
	// update security rules: add needed
	for _, expectedRule := range expectedSecurityRules {
		foundRule := false
		for _, existingRule := range *sg.Properties.SecurityRules {
			if strings.EqualFold(*existingRule.ID, az.getSecurityRuleID(*expectedRule.Name)) {
				glog.Infof("reconcile_sg: rule already exists. ruleName=%q", *existingRule.Name)
				foundRule = true
				break
			}
		}
		if !foundRule {
			glog.Infof("reconcile_sg: adding rule. ruleName=%q", *expectedRule.Name)

			nextAvailablePriority, err := getNextAvailablePriority(updatedRules)
			if err != nil {
				return sg, false, err
			}

			expectedRule.Properties.Priority = to.Int32Ptr(nextAvailablePriority)
			updatedRules = append(updatedRules, expectedRule)
			dirtySg = true
		}
	}
	sg.Properties.SecurityRules = &updatedRules

	return sg, dirtySg, nil
}

func (az *AzureCloud) ensureHostInPool(machineName string, backendPoolID string) error {
	machine, err := az.VirtualMachinesClient.Get(az.ResourceGroup, machineName, "")

	if existsVm, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if !existsVm {
		return fmt.Errorf("failed to retrieve vm to assign to backend pool. instance=%q", machineName)
	}

	primaryNicID := getPrimaryNicID(machine)
	nicName := getLastSegment(primaryNicID)

	nic, err := az.InterfacesClient.Get(az.ResourceGroup, nicName, "")
	if existsNic, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if !existsNic {
		return fmt.Errorf("failed to retrieve vm nic to assign to backend pool. nic=%q", nicName)
	}

	var primaryIPConfig *network.InterfaceIPConfiguration = getPrimaryIPConfig(nic)

	foundPool := false
	newBackendPools := []network.BackendAddressPool{}
	if primaryIPConfig.Properties.LoadBalancerBackendAddressPools != nil {
		newBackendPools = *primaryIPConfig.Properties.LoadBalancerBackendAddressPools
	}
	for _, existingPool := range newBackendPools {
		if strings.EqualFold(backendPoolID, *existingPool.ID) {
			foundPool = true
			break
		}
	}
	if foundPool {
		glog.Infof("ensure: nic: already in correct backend pool. machine=%q", machineName)
		return nil
	} else {
		newBackendPools = append(newBackendPools,
			network.BackendAddressPool{
				ID: to.StringPtr(backendPoolID),
			})

		primaryIPConfig.Properties.LoadBalancerBackendAddressPools = &newBackendPools

		glog.Infof("ensure: nic: update start machine=%q nic=%q", *machine.Name, *nic.Name)
		_, err := az.InterfacesClient.CreateOrUpdate(az.ResourceGroup, *nic.Name, nic, nil)
		if err != nil {
			return fmt.Errorf("failed to update nic. machine=%q err=%q", machineName, err)
		}
		glog.Infof("ensure: nic: update finish machine=%q nic=%q", *machine.Name, *nic.Name)
	}
	return nil
}
