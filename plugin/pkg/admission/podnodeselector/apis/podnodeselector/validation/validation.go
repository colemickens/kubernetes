/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package validation

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"
	internalapi "k8s.io/kubernetes/plugin/pkg/admission/podnodeselector/apis/podnodeselector"
)

// ValidateConfiguration validates the configuration.
func ValidateConfiguration(config *internalapi.Configuration) error {
	allErrs := field.ErrorList{}
	fldpath := field.NewPath("podnodeselector")

	// TODO: return the parsed selectors instead of requiring caller to do it again?

	// TODO: pull the string out, validate it as a flat map node selector thing
	//allErrs = append(allErrs, validation.ValidateNodeSelector(config.ClusterDefaultNodeSelectors, fldpath.Child("clusterDefaultNodeSelectors"))...)
	// TODO: how to validate all of the node selectors in a map/list
	//allErrs = append(allErrs, validation.ValidateNodeSelector(config.Whitelist, fldpath.Child("whitelist"))...)
	_ = fldpath

	if len(allErrs) > 0 {
		return fmt.Errorf("invalid config: %v", allErrs)
	}
	return nil
}
