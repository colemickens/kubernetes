package azure

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	"k8s.io/kubernetes/pkg/cloudprovider"
)

const InstanceInfoURL = "http://169.254.169.254/metadata/v1/InstanceInfo"

var faultDomain *string

type InstanceInfo struct {
	ID           string `json:"ID"`
	UpdateDomain string `json:"UD"`
	FaultDomain  string `json:"FD"`
}

func (az *AzureCloud) GetZone() (cloudprovider.Zone, error) {
	if faultDomain == nil {
		var err error
		faultDomain, err = getFaultDomain()
		if err != nil {
			return cloudprovider.Zone{}, err
		}
	}

	return cloudprovider.Zone{
		FailureDomain: *faultDomain,
		Region:        az.Location,
	}, nil
}

func getFaultDomain() (*string, error) {
	var instanceInfo InstanceInfo

	resp, err := http.Get(InstanceInfoURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &instanceInfo)
	if err != nil {
		return nil, err
	}

	return &instanceInfo.FaultDomain, nil
}
