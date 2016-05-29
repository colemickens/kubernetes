package azure

import (
	"fmt"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/cloudprovider"
)

// TODO: probably remove everything in here that requires credentialed access
// since this is called by kubelet and it'd be great to not ship creds to node
// boxes...

// NodeAddresses returns the addresses of the specified instance.
func (az *AzureCloud) NodeAddresses(name string) ([]api.NodeAddress, error) {
	machine, err := az.VirtualMachinesClient.Get(az.ResourceGroup, name, "")
	if existsMachine, err := checkResourceExistsFromError(err); err != nil {
		return nil, err
	} else if !existsMachine {
		return nil, cloudprovider.InstanceNotFound
	}

	nicID, err := getPrimaryNicID(machine)
	if err != nil {
		return nil, err
	}
	nicName := getLastSegment(nicID)
	nic, err := az.InterfacesClient.Get(az.ResourceGroup, nicName, "")
	if err != nil {
		return nil, err
	}
	ipConfig, err := getPrimaryIPConfig(nic)
	if err != nil {
		return nil, err
	}
	return []api.NodeAddress{
		api.NodeAddress{
			Type:    api.NodeInternalIP,
			Address: *ipConfig.Properties.PrivateIPAddress,
		},
		api.NodeAddress{
			Type:    api.NodeHostName,
			Address: name,
		},
	}, nil
}

// ExternalID returns the cloud provider ID of the specified instance (deprecated).
func (az *AzureCloud) ExternalID(name string) (string, error) {
	// TODO: is this okay?
	return az.InstanceID(name)
}

// InstanceID returns the cloud provider ID of the specified instance.
// Note that if the instance does not exist or is no longer running, we must return ("", cloudprovider.InstanceNotFound)
func (az *AzureCloud) InstanceID(name string) (string, error) {
	machine, err := az.VirtualMachinesClient.Get(az.ResourceGroup, name, "")
	if existsMachine, err := checkResourceExistsFromError(err); err != nil {
		return "", err
	} else if !existsMachine {
		return "", cloudprovider.InstanceNotFound
	}
	return *machine.ID, nil
}

// InstanceType returns the type of the specified instance.
// Note that if the instance does not exist or is no longer running, we must return ("", cloudprovider.InstanceNotFound)
// (Implementer Note): This is used by kubelet. Kubelet will label the node. Real log from kubelet:
//       Adding node label from cloud provider: beta.kubernetes.io/instance-type=[value]
func (az *AzureCloud) InstanceType(name string) (string, error) {
	machine, err := az.VirtualMachinesClient.Get(az.ResourceGroup, name, "")
	if existsMachine, err := checkResourceExistsFromError(err); err != nil {
		return "", err
	} else if !existsMachine {
		return "", cloudprovider.InstanceNotFound
	}
	return string(machine.Properties.HardwareProfile.VMSize), nil
}

// List lists instances that match 'filter' which is a regular expression which must match the entire instance name (fqdn)
func (az *AzureCloud) List(filter string) ([]string, error) {
	// TODO is this okay?
	return nil, fmt.Errorf("not supported")
}

// AddSSHKeyToAllInstances adds an SSH public key as a legal identity for all instances
// expected format for the key is standard ssh-keygen format: <protocol> <blob>
func (az *AzureCloud) AddSSHKeyToAllInstances(user string, keyData []byte) error {
	// TODO: implement properly
	return fmt.Errorf("not supported")
}

// CurrentNodeName returns the name of the node we are currently running on
// On most clouds (e.g. GCE) this is the hostname, so we provide the hostname
func (az *AzureCloud) CurrentNodeName(hostname string) (string, error) {
	return hostname, nil
}
