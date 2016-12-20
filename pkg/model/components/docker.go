/*
Copyright 2016 The Kubernetes Authors.

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

package components

import (
	"fmt"
	"k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/loader"
)

// DockerOptionsBuilder adds options for docker to the model
type DockerOptionsBuilder struct {
	Cluster *kops.Cluster
}

var _ loader.OptionsBuilder = &DockerOptionsBuilder{}

func (b *DockerOptionsBuilder) BuildOptions(o interface{}) error {
	options := o.(*kops.ClusterSpec)
	if options.Docker == nil {
		options.Docker = &kops.DockerConfig{}
	}

	if fi.StringValue(options.Docker.Version) == "" {
		if options.KubernetesVersion == "" {
			return fmt.Errorf("KubernetesVersion is required")
		}

		sv, err := kops.ParseKubernetesVersion(options.KubernetesVersion)
		if err != nil {
			return fmt.Errorf("unable to determine kubernetes version from %q", options.KubernetesVersion)
		}

		dockerVersion := ""
		if sv.Major == 1 && sv.Minor >= 5 {
			dockerVersion = "1.12.3"
		} else if sv.Major == 1 && sv.Minor <= 4 {
			dockerVersion = "1.11.2"
		}

		if dockerVersion == "" {
			return fmt.Errorf("unknown version of kubernetes %q (cannot infer docker version)", options.KubernetesVersion)
		}

		options.Docker.Version = &dockerVersion
	}

	return nil
}
