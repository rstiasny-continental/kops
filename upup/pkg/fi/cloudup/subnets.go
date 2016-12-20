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
	"encoding/binary"
	"fmt"
	"github.com/golang/glog"
	"k8s.io/kops/pkg/apis/kops"
	"net"
	"sort"
)

// ByZone implements sort.Interface for []*ClusterSubnetSpec based on
// the Zone field.
type ByZone []*kops.ClusterSubnetSpec

func (a ByZone) Len() int {
	return len(a)
}
func (a ByZone) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
func (a ByZone) Less(i, j int) bool {
	return a[i].Zone < a[j].Zone
}

func assignCIDRsToSubnets(c *kops.Cluster) error {
	// TODO: We probably could query for the existing subnets & allocate appropriately
	// for now we'll require users to set CIDRs themselves

	if allSubnetsHaveCIDRs(c) {
		glog.V(4).Infof("All subnets have CIDRs; skipping asssignment logic")
		return nil
	}

	_, cidr, err := net.ParseCIDR(c.Spec.NetworkCIDR)
	if err != nil {
		return fmt.Errorf("Invalid NetworkCIDR: %q", c.Spec.NetworkCIDR)
	}

	// We split the network range into 8 subnets
	// But we then reserve the lowest one for the private block
	// (and we split _that_ into 8 further subnets, leaving the first one unused/for future use)
	// Note that this limits us to 7 zones
	// TODO: Does this make sense on GCE?
	// TODO: Should we limit this to say 1000 IPs per subnet? (any reason to?)

	bigCIDRs, err := splitInto8Subnets(cidr)
	if err != nil {
		return err
	}

	var bigSubnets []*kops.ClusterSubnetSpec
	var littleSubnets []*kops.ClusterSubnetSpec

	var reserved []*net.IPNet
	for i := range c.Spec.Subnets {
		subnet := &c.Spec.Subnets[i]
		switch subnet.Type {
		case kops.SubnetTypePublic, kops.SubnetTypePrivate:
			bigSubnets = append(bigSubnets, subnet)

		case kops.SubnetTypeUtility:
			littleSubnets = append(littleSubnets, subnet)

		default:
			return fmt.Errorf("subnet %q has unknown type %q", subnet.Name, subnet.Type)
		}

		if subnet.CIDR != "" {
			_, subnetCIDR, err := net.ParseCIDR(subnet.CIDR)
			if err != nil {
				return fmt.Errorf("subnet %q has unexpected CIDR %q", subnet.Name, subnet.CIDR)
			}

			reserved = append(reserved, subnetCIDR)
		}
	}

	// Remove any CIDRs marked as overlapping
	{
		var nonOverlapping []*net.IPNet
		for _, c := range bigCIDRs {
			overlapped := false
			for _, r := range reserved {
				if cidrsOverlap(r, c) {
					overlapped = true
				}
			}
			if !overlapped {
				nonOverlapping = append(nonOverlapping, c)
			}
		}
		bigCIDRs = nonOverlapping
	}

	if len(bigCIDRs) == 0 {
		return fmt.Errorf("could not find any non-overlapping CIDRs in parent NetworkCIDR; cannot automatically assign CIDR to subnet")
	}

	littleCIDRs, err := splitInto8Subnets(bigCIDRs[0])
	if err != nil {
		return err
	}
	bigCIDRs = bigCIDRs[1:]

	// Assign a consistent order
	sort.Sort(ByZone(bigSubnets))
	sort.Sort(ByZone(littleSubnets))

	for _, subnet := range bigSubnets {
		if subnet.CIDR != "" {
			continue
		}

		if len(bigCIDRs) == 0 {
			return fmt.Errorf("insufficient (big) CIDRs remaining for automatic CIDR allocation to subnet %q", subnet.Name)
		}
		subnet.CIDR = bigCIDRs[0].String()
		glog.Infof("Assigned CIDR %s to subnet %s", subnet.CIDR, subnet.Name)

		bigCIDRs = bigCIDRs[1:]
	}

	for _, subnet := range littleSubnets {
		if subnet.CIDR != "" {
			continue
		}

		if len(littleCIDRs) == 0 {
			return fmt.Errorf("insufficient (little) CIDRs remaining for automatic CIDR allocation to subnet %q", subnet.Name)
		}
		subnet.CIDR = littleCIDRs[0].String()
		glog.Infof("Assigned CIDR %s to subnet %s", subnet.CIDR, subnet.Name)

		littleCIDRs = littleCIDRs[1:]
	}

	return nil
}

// splitInto8Subnets splits the parent IPNet into 8 subnets
func splitInto8Subnets(parent *net.IPNet) ([]*net.IPNet, error) {
	networkLength, _ := parent.Mask.Size()
	networkLength += 3

	var subnets []*net.IPNet
	for i := 0; i < 8; i++ {
		ip4 := parent.IP.To4()
		if ip4 != nil {
			n := binary.BigEndian.Uint32(ip4)
			n += uint32(i) << uint(32-networkLength)
			subnetIP := make(net.IP, len(ip4))
			binary.BigEndian.PutUint32(subnetIP, n)

			subnets = append(subnets, &net.IPNet{
				IP:   subnetIP,
				Mask: net.CIDRMask(networkLength, 32),
			})
		} else {
			return nil, fmt.Errorf("Unexpected IP address type: %s", parent)
		}
	}

	return subnets, nil
}

// allSubnetsHaveCIDRs returns true iff each subnet in the cluster has a non-empty CIDR
func allSubnetsHaveCIDRs(c *kops.Cluster) bool {
	for i := range c.Spec.Subnets {
		subnet := &c.Spec.Subnets[i]
		if subnet.CIDR == "" {
			return false
		}
	}

	return true
}

// cidrsOverlap returns true iff the two CIDRs are non-disjoint
func cidrsOverlap(l, r *net.IPNet) bool {
	return l.Contains(r.IP) || r.Contains(l.IP)
}
