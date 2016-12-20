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

package cloudup

import (
	"fmt"
	api "k8s.io/kops/pkg/apis/kops"
	"strings"
	"testing"
)

func buildMinimalNodeInstanceGroup(subnets ...string) *api.InstanceGroup {
	g := &api.InstanceGroup{}
	g.ObjectMeta.Name = "nodes"
	g.Spec.Role = api.InstanceGroupRoleNode
	g.Spec.Subnets = subnets

	return g
}

func buildMinimalMasterInstanceGroup(subnets ...string) *api.InstanceGroup {
	g := &api.InstanceGroup{}
	g.ObjectMeta.Name = "master"
	g.Spec.Role = api.InstanceGroupRoleMaster
	g.Spec.Subnets = subnets

	return g
}

func TestPopulateInstanceGroup_Name_Required(t *testing.T) {
	cluster := buildMinimalCluster()
	g := buildMinimalNodeInstanceGroup()
	g.ObjectMeta.Name = ""

	channel := &api.Channel{}

	expectErrorFromPopulateInstanceGroup(t, cluster, g, channel, "Name")
}

func TestPopulateInstanceGroup_Role_Required(t *testing.T) {
	cluster := buildMinimalCluster()
	g := buildMinimalNodeInstanceGroup()
	g.Spec.Role = ""

	channel := &api.Channel{}

	expectErrorFromPopulateInstanceGroup(t, cluster, g, channel, "Role")
}

func Test_defaultMasterMachineType(t *testing.T) {
	cluster := buildMinimalCluster()

	tests := map[string]string{
		"us-east-1b": "m3.medium",
		"us-east-2b": "c4.large",
		"eu-west-1b": "m3.medium",
	}

	for zone, expected := range tests {
		cluster.Spec.Subnets = []api.ClusterSubnetSpec{
			{
				Name: "subnet-" + zone,
				Zone: zone,
			},
		}
		actual := defaultMasterMachineType(cluster)
		if actual != expected {
			t.Fatalf("zone=%q actual=%q; expected=%q", zone, actual, expected)
		}
	}
}

func expectErrorFromPopulateInstanceGroup(t *testing.T, cluster *api.Cluster, g *api.InstanceGroup, channel *api.Channel, message string) {
	_, err := PopulateInstanceGroupSpec(cluster, g, channel)
	if err == nil {
		t.Fatalf("Expected error from PopulateInstanceGroup")
	}
	actualMessage := fmt.Sprintf("%v", err)
	if !strings.Contains(actualMessage, message) {
		t.Fatalf("Expected error %q, got %q", message, actualMessage)
	}
}
