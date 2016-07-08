package azure

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/kubernetes/pkg/api"
	utilerrors "k8s.io/kubernetes/pkg/util/errors"

	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/glog"
)

// GetLoadBalancer returns whether the specified load balancer exists, and
// if so, what its status is.
func (az *AzureCloud) GetLoadBalancer(clusterName string, service *api.Service) (status *api.LoadBalancerStatus, exists bool, err error) {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	serviceName := getServiceName(service)
	glog.Infof("get(%s): START clusterName=%q lbName=%q", serviceName, clusterName, lbName)

	glog.Infof("get(%s): lb(%s) - retrieving", serviceName, lbName)
	_, err = az.LoadBalancerClient.Get(az.ResourceGroup, lbName, "")
	if existsLb, err := checkResourceExistsFromError(err); err != nil {
		glog.Errorf("get(%s): lb(%s) - retrieving failed: %q", serviceName, lbName, err)
		return nil, false, err
	} else if !existsLb {
		glog.Infof("get(%s): lb(%s) - doesn't exist", serviceName, lbName)
		return nil, false, nil
	}

	glog.Infof("get(%s): pip(%s) - retrieving", serviceName, pipName)
	pip, err := az.PublicIPAddressesClient.Get(az.ResourceGroup, pipName, "")
	if existsLbPip, err := checkResourceExistsFromError(err); err != nil {
		glog.Errorf("get(%s): pip(%s) - retrieving failed: %q", serviceName, pipName, err)
		return nil, false, err
	} else if !existsLbPip {
		glog.Infof("get(%s): pip(%s) - doesn't exist", serviceName, pipName)
		return nil, false, nil
	}

	glog.Infof("get(%s): FINISH")
	return &api.LoadBalancerStatus{
		Ingress: []api.LoadBalancerIngress{{IP: *pip.Properties.IPAddress}},
	}, true, nil
}

// EnsureLoadBalancer creates a new load balancer 'name', or updates the existing one. Returns the status of the balancer
func (az *AzureCloud) EnsureLoadBalancer(clusterName string, service *api.Service, hosts []string) (*api.LoadBalancerStatus, error) {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	serviceName := getServiceName(service)
	glog.Infof("ensure(%s): START clusterName=%q lbName=%q", serviceName, clusterName, lbName)

	pip, err := az.ensurePublicIPExists(serviceName, pipName)
	if err != nil {
		return nil, err
	}

	glog.Infof("ensure(%s): sg(%s) - retrieving", serviceName, az.SecurityGroupName)
	sg, err := az.SecurityGroupsClient.Get(az.ResourceGroup, az.SecurityGroupName, "")
	if err != nil {
		glog.Errorf("ensure(%s): sg(%s) - retrieving failed: %q", serviceName, *sg.Name, err)
		return nil, err
	}
	sg, sgNeedsUpdate, err := az.reconcileSecurityGroup(sg, clusterName, service)
	if err != nil {
		return nil, err
	}
	if sgNeedsUpdate {
		glog.Infof("ensure(%s): sg(%s) - updating", serviceName, *sg.Name)
		_, err := az.SecurityGroupsClient.CreateOrUpdate(az.ResourceGroup, *sg.Name, sg, nil)
		if err != nil {
			glog.Errorf("ensure(%s): sg(%s) - updating failed: %q", serviceName, *sg.Name, err)
			return nil, fmt.Errorf("failed to update security group. err=%q", err)
		}
	}

	lbNeedsCreate := false
	glog.Infof("ensure(%s): lb(%s) - retrieving", serviceName, lbName)
	lb, err := az.LoadBalancerClient.Get(az.ResourceGroup, lbName, "")
	if existsLb, err := checkResourceExistsFromError(err); err != nil {
		glog.Infof("ensure(%s): lb(%s) - retrieving failed: %q", serviceName, lbName, err)
		return nil, err
	} else if !existsLb {
		lb = network.LoadBalancer{
			Name:       &lbName,
			Location:   &az.Location,
			Properties: &network.LoadBalancerPropertiesFormat{},
		}
		lbNeedsCreate = true
		glog.Infof("ensure(%s): lb(%s) - needs creation")
	}

	lb, lbNeedsUpdate, err := az.reconcileLoadBalancer(lb, pip, clusterName, service, hosts)
	if err != nil {
		return nil, err
	}
	if lbNeedsCreate || lbNeedsUpdate {
		glog.Infof("ensure(%s): lb(%s) - updating", serviceName, lbName)
		_, err = az.LoadBalancerClient.CreateOrUpdate(az.ResourceGroup, *lb.Name, lb, nil)
		if err != nil {
			glog.Errorf("ensure(%s): lb(%s) - updating failed: %q", serviceName, lbName, err)
			return nil, err
		}
	}

	// Add the machines to the backend pool if they're not already
	// TODO: handle node lb pool eviction, but how?
	lbBackendName := getBackendPoolName(clusterName)
	lbBackendPoolID := az.getBackendPoolID(lbName, lbBackendName)
	hostUpdates := make([]func() error, len(hosts))
	for i, host := range hosts {
		f := func(serviceName, host, lbBackendPoolID string) func() error {
			return func() error {
				glog.Infof("ensureHostInPool(%s): host(%s) - calling", serviceName, host)
				return az.ensureHostInPool(serviceName, host, lbBackendPoolID)
			}
		}(serviceName, host, lbBackendPoolID)
		hostUpdates[i] = f
	}

	errs := utilerrors.AggregateGoroutines(hostUpdates...)
	if errs != nil {
		return nil, utilerrors.Flatten(errs)
	}

	glog.Infof("ensure(%s): FINISH - %s", service.Name, *pip.Properties.IPAddress)
	return &api.LoadBalancerStatus{
		Ingress: []api.LoadBalancerIngress{{IP: *pip.Properties.IPAddress}},
	}, nil
}

// UpdateLoadBalancer updates hosts under the specified load balancer.
func (az *AzureCloud) UpdateLoadBalancer(clusterName string, service *api.Service, hosts []string) error {
	serviceName := getServiceName(service)
	glog.Infof("update(%s): START", serviceName)
	_, err := az.EnsureLoadBalancer(clusterName, service, hosts)
	glog.Infof("update(%s): FINISH", serviceName)
	return err
}

// EnsureLoadBalancerDeleted deletes the specified load balancer if it
// exists, returning nil if the load balancer specified either didn't exist or
// was successfully deleted.
// This construction is useful because many cloud providers' load balancers
// have multiple underlying components, meaning a Get could say that the LB
// doesn't exist even if some part of it is still laying around.
func (az *AzureCloud) EnsureLoadBalancerDeleted(clusterName string, service *api.Service) error {
	lbName := getLoadBalancerName(clusterName)
	pipName := getPublicIPName(clusterName, service)
	serviceName := getServiceName(service)
	glog.Infof("delete(%s): START clusterName=%q lbName=%q", clusterName, lbName)

	// reconcile logic is capable of fully reconcile, so we can use this to delete
	service.Spec.Ports = []api.ServicePort{}

	glog.Infof("delete(%s): lb(%s) - retrieving", serviceName, lbName)
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
				glog.Infof("delete(%s): lb(%s) - updating", serviceName, lbName)
				_, err = az.LoadBalancerClient.CreateOrUpdate(az.ResourceGroup, *lb.Name, lb, nil)
				if err != nil {
					glog.Errorf("delete(%s): lb(%s) - updating failed: %q", serviceName, az.SecurityGroupName, err)
					return err
				}
			} else {
				glog.Infof("delete(%s): lb(%s) - deleting due to no remaining frontendipconfigs", serviceName, lbName)
				_, err = az.LoadBalancerClient.Delete(az.ResourceGroup, lbName, nil)
				if err != nil {
					glog.Errorf("delete(%s): lb(%s) - deleting failed: %q", serviceName, az.SecurityGroupName, err)
					return err
				}
			}
		}
	}

	glog.Infof("delete(%s): sg(%s) - retrieving", serviceName, az.SecurityGroupName)
	sg, err := az.SecurityGroupsClient.Get(az.ResourceGroup, az.SecurityGroupName, "")
	if existsSg, err := checkResourceExistsFromError(err); err != nil {
		glog.Infof("delete(%s): sg(%s) - retrieving failed: %q", serviceName, az.SecurityGroupName, err)
		return err
	} else if existsSg {
		sg, sgNeedsUpdate, err := az.reconcileSecurityGroup(sg, clusterName, service)
		if err != nil {
			return err
		}
		if sgNeedsUpdate {
			glog.Infof("delete(%s): sg(%s) - updating", serviceName, az.SecurityGroupName)
			_, err := az.SecurityGroupsClient.CreateOrUpdate(az.ResourceGroup, *sg.Name, sg, nil)
			if err != nil {
				glog.Errorf("delete(%s): sg(%s) - updating failed: %q", serviceName, az.SecurityGroupName, err)
				return fmt.Errorf("failed to update security group. err=%q", err)
			}
		}
	}

	err = az.ensurePublicIPDeleted(serviceName, pipName)
	if err != nil {
		return err
	}

	glog.Infof("delete(%s): FINISH", serviceName)
	return nil
}

func (az *AzureCloud) ensurePublicIPExists(serviceName, pipName string) (*network.PublicIPAddress, error) {
	pip, err := az.PublicIPAddressesClient.Get(az.ResourceGroup, pipName, "")
	if existsPip, err := checkResourceExistsFromError(err); err != nil {
		return nil, err
	} else if existsPip {
		glog.Infof("ensure(%s): pip(%s) - already exists", serviceName, *pip.Name)
		return &pip, nil
	} else {
		pip.Name = to.StringPtr(pipName)
		pip.Location = to.StringPtr(az.Location)
		pip.Properties = &network.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: network.Static,
		}
		pip.Tags = &map[string]*string{"service": &serviceName}

		glog.Infof("ensure(%s): pip(%s) - creating", serviceName, *pip.Name)
		_, err = az.PublicIPAddressesClient.CreateOrUpdate(az.ResourceGroup, *pip.Name, pip, nil)
		if err != nil {
			glog.Errorf("ensure(%s): pip(%s) - creating failed: %q", serviceName, *pip.Name, err)
			return nil, err
		}

		glog.Infof("ensure(%s): pip(%s) - retrieving", serviceName, *pip.Name)
		pip, err = az.PublicIPAddressesClient.Get(az.ResourceGroup, *pip.Name, "")
		if err != nil {
			glog.Errorf("ensure(%s): pip(%s) - retrieving failed: %q", serviceName, *pip.Name, err)
			return nil, err
		}

		return &pip, nil
	}
}

func (az *AzureCloud) ensurePublicIPDeleted(serviceName, pipName string) error {
	glog.Infof("delete(%s): pip(%s) - deleting pip", serviceName, pipName)
	_, err := az.PublicIPAddressesClient.Delete(az.ResourceGroup, pipName, nil)
	if _, err := checkResourceExistsFromError(err); err != nil {
		glog.Errorf("delete(%s): pip(%s) - deleting failed: %q", serviceName, pipName, err)
		return fmt.Errorf("failed to delete public ip: %q", err)
	}
	return nil
}

// This ensures load balancer exists and the frontend ip config is setup.
// This also reconciles the Service's Ports  with the LoadBalancer config.
// This entails adding rules/probes for expected Ports and removing stale rules/ports.
func (az *AzureCloud) reconcileLoadBalancer(lb network.LoadBalancer, pip *network.PublicIPAddress, clusterName string, service *api.Service, hosts []string) (network.LoadBalancer, bool, error) {
	lbName := getLoadBalancerName(clusterName)
	serviceName := getServiceName(service)
	lbFrontendIPConfigName := getFrontendIPConfigName(service)
	lbFrontendIPConfigID := az.getFrontendIPConfigID(lbName, lbFrontendIPConfigName)
	lbBackendPoolName := getBackendPoolName(clusterName)
	lbBackendPoolID := az.getBackendPoolID(lbName, lbBackendPoolName)

	wantLb := (pip != nil)
	dirtyLb := false

	// Ensure LoadBalancer's Backend Pool Configuration
	if wantLb && lb.Properties.BackendAddressPools == nil ||
		len(*lb.Properties.BackendAddressPools) == 0 {
		lb.Properties.BackendAddressPools = &[]network.BackendAddressPool{
			network.BackendAddressPool{
				Name: to.StringPtr(lbBackendPoolName),
			},
		}
		glog.Infof("reconcile(%s)(%t): lb backendpool - adding", serviceName, wantLb)
		dirtyLb = true
	} else if len(*lb.Properties.BackendAddressPools) != 1 ||
		!strings.EqualFold(*(*lb.Properties.BackendAddressPools)[0].ID, lbBackendPoolID) {
		glog.Errorf("reconcile(%s)(%t): lb backendpool - misconfigured", serviceName, wantLb)
		return lb, false, fmt.Errorf("loadbalancer is misconfigured with a different backend pool")
	}

	// Ensure LoadBalancer's Frontend IP Configurations
	dirtyConfigs := false
	newConfigs := []network.FrontendIPConfiguration{}
	if lb.Properties.FrontendIPConfigurations != nil {
		newConfigs = *lb.Properties.FrontendIPConfigurations
	}
	if !wantLb {
		for i := len(newConfigs) - 1; i >= 0; i-- {
			config := newConfigs[i]
			if strings.EqualFold(*config.ID, lbFrontendIPConfigID) {
				glog.Infof("reconcile(%s)(%t): lb frontendconfig(%s) - dropping", serviceName, wantLb, lbFrontendIPConfigName)
				newConfigs = append(newConfigs[:i],
					newConfigs[i+1:]...)
				dirtyConfigs = true
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
			glog.Infof("reconcile(%s)(%t): lb frontendconfig(%s) - adding", serviceName, wantLb, lbFrontendIPConfigName)
			dirtyConfigs = true
		}
	}
	if dirtyConfigs {
		glog.Infof("reconcile(%s)(%t): lb(%s) is dirty", serviceName, wantLb, lbName)
		dirtyLb = true
		lb.Properties.FrontendIPConfigurations = &newConfigs
	}

	// update probes/rules
	expectedProbes := make([]network.Probe, len(service.Spec.Ports))
	expectedRules := make([]network.LoadBalancingRule, len(service.Spec.Ports))
	for i, port := range service.Spec.Ports {
		lbRuleName := getRuleName(service, port)

		transportProto, _, probeProto, err := getProtosFromKubeProto(port.Protocol)
		if err != nil {
			return lb, false, err
		}

		expectedProbes[i] = network.Probe{
			Name: &lbRuleName,
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
	dirtyProbes := false
	updatedProbes := []network.Probe{}
	if lb.Properties.Probes != nil {
		updatedProbes = *lb.Properties.Probes
	}
	existingProbeCount := len(updatedProbes)
	for i := len(updatedProbes) - 1; i >= 0; i-- {
		existingProbe := updatedProbes[i]
		if serviceOwnsRule(service, *existingProbe.Name) {
			glog.Infof("reconcile(%s)(%t): lb probe(%s) - considering evicting", serviceName, wantLb, *existingProbe.Name)
			keepProbe := false
			for _, expectedProbe := range expectedProbes {
				if strings.EqualFold(*existingProbe.ID, az.getLoadBalancerProbeID(lbName, *expectedProbe.Name)) {
					glog.Infof("reconcile(%s)(%t): lb probe(%s) - keeping", serviceName, wantLb, *existingProbe.Name)
					keepProbe = true
					break
				}
			}
			if !keepProbe {
				updatedProbes = append(updatedProbes[:i], updatedProbes[i+1:]...)
				glog.Infof("reconcile(%s)(%t): lb probe(%s) - dropping", serviceName, wantLb, *existingProbe.Name)
				dirtyProbes = true
			}
		}
	}
	// add missing, wanted probes
	for _, expectedProbe := range expectedProbes {
		foundProbe := false
		for _, existingProbe := range updatedProbes[:existingProbeCount] {
			if strings.EqualFold(*existingProbe.ID, az.getLoadBalancerProbeID(lbName, *expectedProbe.Name)) {
				glog.Infof("reconcile(%s)(%t): lb probe(%s) - already exists", serviceName, wantLb, *existingProbe.Name)
				foundProbe = true
				break
			}
		}
		if !foundProbe {
			glog.Infof("reconcile: adding probe. probeName=%q", *expectedProbe.Name)
			updatedProbes = append(updatedProbes, expectedProbe)
			dirtyProbes = true
		}
	}
	if dirtyProbes {
		glog.Infof("reconcile(%s)(%t): lb(%s) is dirty", serviceName, wantLb, lbName)
		dirtyLb = true
		lb.Properties.Probes = &updatedProbes
	}

	// update rules
	dirtyRules := false
	updatedRules := []network.LoadBalancingRule{}
	if lb.Properties.LoadBalancingRules != nil {
		updatedRules = *lb.Properties.LoadBalancingRules
	}
	existingRuleCount := len(updatedRules)
	// update rules: remove unwanted
	for i := len(updatedRules) - 1; i >= 0; i-- {
		existingRule := updatedRules[i]
		keepRule := false
		if serviceOwnsRule(service, *existingRule.Name) {
			glog.Infof("reconcile(%s)(%t): lb rule(%s) - considering evicting", serviceName, wantLb, *existingRule.Name)
			for _, expectedRule := range expectedRules {
				if strings.EqualFold(*existingRule.ID, az.getLoadBalancerRuleID(lbName, *expectedRule.Name)) {
					glog.Infof("reconcile(%s)(%t): lb rule(%s) - keeping", serviceName, wantLb, *existingRule.Name)
					keepRule = true
					break
				}
			}
			if !keepRule {
				glog.Infof("reconcile(%s)(%t) lb rule(%s) - dropping", serviceName, wantLb, *existingRule.Name)
				updatedRules = append(updatedRules[:i], updatedRules[i+1:]...)
				dirtyRules = true
			}
		}
	}
	// update rules: add needed
	for _, expectedRule := range expectedRules {
		foundRule := false
		for _, existingRule := range updatedRules[:existingRuleCount] {
			if strings.EqualFold(*existingRule.ID, az.getLoadBalancerRuleID(lbName, *expectedRule.Name)) {
				glog.Infof("reconcile(%s)(%t): lb rule(%s) already exists", serviceName, wantLb, *existingRule.Name)
				foundRule = true
				break
			}
		}
		if !foundRule {
			glog.Infof("reconcile(%s)(%t): lb rule(%s) adding", serviceName, wantLb, *expectedRule.Name)
			updatedRules = append(updatedRules, expectedRule)
			dirtyRules = true
		}
	}
	if dirtyRules {
		glog.Infof("reconcile(%s)(%t): lb(%s) is dirty", serviceName, wantLb, lbName)
		dirtyLb = true
		lb.Properties.LoadBalancingRules = &updatedRules
	}

	return lb, dirtyLb, nil
}

// This reconciles the Network Security Group similar to how the LB is reconciled.
// This entails adding required, missing SecurityRules and removing stale rules.
func (az *AzureCloud) reconcileSecurityGroup(sg network.SecurityGroup, clusterName string, service *api.Service) (network.SecurityGroup, bool, error) {
	serviceName := getServiceName(service)
	wantLb := len(service.Spec.Ports) > 0
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
			glog.Infof("reconcile(%s)(%t): sg rule(%s) - considering evicting", serviceName, wantLb, *existingRule.Name)
			keepRule := false

			for _, expectedRule := range expectedSecurityRules {
				if strings.EqualFold(*existingRule.ID, az.getSecurityRuleID(*expectedRule.Name)) {
					glog.Infof("reconcile(%s)(%t): sg rule(%s) - keeping", serviceName, wantLb, *existingRule.Name)
					keepRule = true
					break
				}
			}
			if !keepRule {
				glog.Infof("reconcile(%s)(%t): sg rule(%s) - dropping", serviceName, wantLb, *existingRule.Name)
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
				glog.Infof("reconcile(%s)(%t): sg rule(%s) - already exists", serviceName, wantLb, *existingRule.Name)
				foundRule = true
				break
			}
		}
		if !foundRule {
			glog.Infof("reconcile(%s)(%t): sg rule(%s) - adding", serviceName, wantLb, *expectedRule.Name)

			nextAvailablePriority, err := getNextAvailablePriority(updatedRules)
			if err != nil {
				return sg, false, err
			}

			expectedRule.Properties.Priority = to.Int32Ptr(nextAvailablePriority)
			updatedRules = append(updatedRules, expectedRule)
			dirtySg = true
		}
	}
	if dirtySg {
		glog.Infof("reconcile(%s)(%t): sg(%s) - dirty", serviceName, wantLb, az.SecurityGroupName)
		sg.Properties.SecurityRules = &updatedRules
	}

	return sg, dirtySg, nil
}

// This ensures the given VM's Primary NIC's Primary IP Configuration is
// participating in the specified LoadBalancer Backend Pool.
func (az *AzureCloud) ensureHostInPool(serviceName, machineName string, backendPoolID string) error {
	glog.Infof("nicupdate(%s): vm(%s) - retrieving", serviceName, machineName)
	machine, err := az.VirtualMachinesClient.Get(az.ResourceGroup, machineName, "")
	if err != nil {
		glog.Errorf("nicupdate(%s): vm(%s) - retrieving failed: %q", serviceName, machineName, err)
		return fmt.Errorf("failed to retrieve vm instance. instance=%q", machineName)
	}

	primaryNicID, err := getPrimaryNicID(machine)
	if err != nil {
		return err
	}
	nicName := getLastSegment(primaryNicID)

	nic, err := az.InterfacesClient.Get(az.ResourceGroup, nicName, "")
	if existsNic, err := checkResourceExistsFromError(err); err != nil {
		return err
	} else if !existsNic {
		glog.Errorf("nicupdate(%s): nic(%s) - retrieving failed: %q", serviceName, nicName, err)
		return fmt.Errorf("failed to retrieve vm nic to assign to backend pool. nic=%q", nicName)
	}

	var primaryIPConfig *network.InterfaceIPConfiguration
	primaryIPConfig, err = getPrimaryIPConfig(nic)
	if err != nil {
		return err
	}

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
		glog.Infof("nicupdate(%s): nic(%s) - backendpool already correct", serviceName, nicName)
	} else {
		glog.Infof("nicupdate(%s): nic(%s) - backendpool needs update", serviceName, nicName)
		newBackendPools = append(newBackendPools,
			network.BackendAddressPool{
				ID: to.StringPtr(backendPoolID),
			})

		primaryIPConfig.Properties.LoadBalancerBackendAddressPools = &newBackendPools

		glog.Infof("nicupdate(%s): nic(%s) - updating", serviceName, nicName)
		_, err := az.InterfacesClient.CreateOrUpdate(az.ResourceGroup, *nic.Name, nic, nil)
		if err != nil {
			glog.Errorf("nicupdate(%s): nic(%s) - updating failed: %q", serviceName, nicName, err)
			return fmt.Errorf("failed to update nic. machine=%q err=%q", machineName, err)
		}
	}
	return nil
}
