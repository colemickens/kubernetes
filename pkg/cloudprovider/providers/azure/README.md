
# Azure cloud provider

## Assumptions

1. Everything is in one resource group in one location.

2. The node hostname is the same as the machine name in Azure and will not be changed.


## Azure Limitations (Bring-up)

1. The user must manually configure a cloud-config file on each node with the necessary fields for the Azure Cloudprovider. (TODO: link to the struct type definition) since there is no metadata service.


## Azure Limitations (LoadBalancer)

Source: https://azure.microsoft.com/en-us/documentation/articles/azure-subscription-service-limits/#networking-limits---azure-resource-manager

1. There can only be one load balancer per availability set, and a given node can only be a member of a single availability set. Thus, all LoadBalancer Services have to share a single load balancer.

2. By default, only 20 Reserved IP Addresses are allowed per subscription. This means that you may only have 20 services exposed for a given **subscription**. This limit can be raised up to an unspecified amount by contacting support.

3. By default, only 5 frontend IPs per load balancer are allowed. Meaning that only 5 services can be exposed for a given **cluster**. This limit can be raised up to an unspecified amount by contacting support.

4. By default, only 200 Security Group Rules are allowed per Security Group. This means that across all exposed services, only 200 ports may be exposed for a given **cluster**. This limit can be raised by up to 500 Rules per Security Group by contacting support.

## Azure Limitations (Routes)

Source: https://azure.microsoft.com/en-us/documentation/articles/azure-subscription-service-limits/#networking-limits---azure-resource-manager

1. By default, Route Tables only support 100 entries, so your cluster is limited to 100 nodes. This can be raised to 400 by contacting Support.


## Notes / Questions

1. What is Route's nameHint supposed to be used for? Especially given that it's only supplied during Create and not during Delete?

2. Are ports cleared out whenever EnsureDeleted is called? If not, is it okay to do that and then use my same reconcile logic?
