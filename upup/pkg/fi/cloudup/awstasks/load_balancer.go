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

package awstasks

import (
	"fmt"

	"strconv"

	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/golang/glog"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/upup/pkg/fi/cloudup/terraform"
)

//go:generate fitask -type=LoadBalancer
type LoadBalancer struct {
	Name *string

	// ID is the name in ELB, possibly different from our name
	// (ELB is restricted as to names, so we have limited choices!)
	ID *string

	DNSName      *string
	HostedZoneId *string

	Subnets        []*Subnet
	SecurityGroups []*SecurityGroup

	Listeners map[string]*LoadBalancerListener

	Scheme *string

	HealthCheck *LoadBalancerHealthCheck
	AccessLog   *LoadBalancerAccessLog
	//AdditionalAttributes   []*LoadBalancerAdditionalAttribute
	ConnectionDraining     *LoadBalancerConnectionDraining
	ConnectionSettings     *LoadBalancerConnectionSettings
	CrossZoneLoadBalancing *LoadBalancerCrossZoneLoadBalancing
}

var _ fi.CompareWithID = &LoadBalancer{}

func (e *LoadBalancer) CompareWithID() *string {
	return e.ID
}

type LoadBalancerListener struct {
	InstancePort int
}

func (e *LoadBalancerListener) mapToAWS(loadBalancerPort int64) *elb.Listener {
	return &elb.Listener{
		LoadBalancerPort: aws.Int64(loadBalancerPort),

		Protocol: aws.String("TCP"),

		InstanceProtocol: aws.String("TCP"),
		InstancePort:     aws.Int64(int64(e.InstancePort)),
	}
}

var _ fi.HasDependencies = &LoadBalancerListener{}

func (e *LoadBalancerListener) GetDependencies(tasks map[string]fi.Task) []fi.Task {
	return nil
}

func findLoadBalancer(cloud awsup.AWSCloud, name string) (*elb.LoadBalancerDescription, error) {
	request := &elb.DescribeLoadBalancersInput{
		LoadBalancerNames: []*string{&name},
	}
	found, err := describeLoadBalancers(cloud, request, func(lb *elb.LoadBalancerDescription) bool {
		if aws.StringValue(lb.LoadBalancerName) == name {
			return true
		}

		glog.Warningf("Got ELB with unexpected name: %q", lb.LoadBalancerName)
		return false
	})

	if err != nil {
		if awsError, ok := err.(awserr.Error); ok {
			if awsError.Code() == "LoadBalancerNotFound" {
				return nil, nil
			}
		}

		return nil, fmt.Errorf("error listing ELBs: %v", err)
	}

	if len(found) == 0 {
		return nil, nil
	}

	if len(found) != 1 {
		return nil, fmt.Errorf("Found multiple ELBs with name %q", name)
	}

	return found[0], nil
}

func findLoadBalancerByAlias(cloud awsup.AWSCloud, alias *route53.AliasTarget) (*elb.LoadBalancerDescription, error) {
	// TODO: Any way to avoid listing all ELBs?
	request := &elb.DescribeLoadBalancersInput{}

	dnsName := aws.StringValue(alias.DNSName)
	matchDnsName := strings.TrimSuffix(dnsName, ".")
	if matchDnsName == "" {
		return nil, fmt.Errorf("DNSName not set on AliasTarget")
	}

	matchHostedZoneId := aws.StringValue(alias.HostedZoneId)

	found, err := describeLoadBalancers(cloud, request, func(lb *elb.LoadBalancerDescription) bool {
		if matchHostedZoneId != aws.StringValue(lb.CanonicalHostedZoneNameID) {
			return false
		}

		lbDnsName := aws.StringValue(lb.DNSName)
		lbDnsName = strings.TrimSuffix(lbDnsName, ".")
		return lbDnsName == matchDnsName || "dualstack."+lbDnsName == matchDnsName
	})

	if err != nil {
		return nil, fmt.Errorf("error listing ELBs: %v", err)
	}

	if len(found) == 0 {
		return nil, nil
	}

	if len(found) != 1 {
		return nil, fmt.Errorf("Found multiple ELBs with DNSName %q", dnsName)
	}

	return found[0], nil
}

func describeLoadBalancers(cloud awsup.AWSCloud, request *elb.DescribeLoadBalancersInput, filter func(*elb.LoadBalancerDescription) bool) ([]*elb.LoadBalancerDescription, error) {
	var found []*elb.LoadBalancerDescription
	err := cloud.ELB().DescribeLoadBalancersPages(request, func(p *elb.DescribeLoadBalancersOutput, lastPage bool) (shouldContinue bool) {
		for _, lb := range p.LoadBalancerDescriptions {
			if filter(lb) {
				found = append(found, lb)
			}
		}

		return true
	})

	if err != nil {
		return nil, err
	}

	return found, nil
}

func (e *LoadBalancer) Find(c *fi.Context) (*LoadBalancer, error) {
	cloud := c.Cloud.(awsup.AWSCloud)

	elbName := fi.StringValue(e.ID)
	if elbName == "" {
		elbName = fi.StringValue(e.Name)
	}

	lb, err := findLoadBalancer(cloud, elbName)
	if err != nil {
		return nil, err
	}
	if lb == nil {
		return nil, nil
	}

	actual := &LoadBalancer{}
	actual.Name = e.Name
	actual.ID = lb.LoadBalancerName
	actual.DNSName = lb.DNSName
	actual.HostedZoneId = lb.CanonicalHostedZoneNameID
	actual.Scheme = lb.Scheme

	for _, subnet := range lb.Subnets {
		actual.Subnets = append(actual.Subnets, &Subnet{ID: subnet})
	}

	for _, sg := range lb.SecurityGroups {
		actual.SecurityGroups = append(actual.SecurityGroups, &SecurityGroup{ID: sg})
	}

	actual.Listeners = make(map[string]*LoadBalancerListener)

	for _, ld := range lb.ListenerDescriptions {
		l := ld.Listener
		loadBalancerPort := strconv.FormatInt(aws.Int64Value(l.LoadBalancerPort), 10)

		actualListener := &LoadBalancerListener{}
		actualListener.InstancePort = int(aws.Int64Value(l.InstancePort))
		actual.Listeners[loadBalancerPort] = actualListener
	}

	healthcheck, err := findHealthCheck(lb)
	if err != nil {
		return nil, err
	}
	actual.HealthCheck = healthcheck

	// Extract attributes
	lbAttributes, err := findELBAttributes(cloud, elbName)
	if err != nil {
		return nil, err
	}
	if lbAttributes != nil {
		actual.AccessLog = &LoadBalancerAccessLog{}
		if lbAttributes.AccessLog.EmitInterval != nil {
			actual.AccessLog.EmitInterval = lbAttributes.AccessLog.EmitInterval
		}
		if lbAttributes.AccessLog.Enabled != nil {
			actual.AccessLog.Enabled = lbAttributes.AccessLog.Enabled
		}
		if lbAttributes.AccessLog.S3BucketName != nil {
			actual.AccessLog.S3BucketName = lbAttributes.AccessLog.S3BucketName
		}
		if lbAttributes.AccessLog.S3BucketPrefix != nil {
			actual.AccessLog.S3BucketPrefix = lbAttributes.AccessLog.S3BucketPrefix
		}

		// We don't map AdditionalAttributes - yet
		//var additionalAttributes []*LoadBalancerAdditionalAttribute
		//for index, additionalAttribute := range lbAttributes.AdditionalAttributes {
		//	additionalAttributes[index] = &LoadBalancerAdditionalAttribute{
		//		Key:   additionalAttribute.Key,
		//		Value: additionalAttribute.Value,
		//	}
		//}
		//actual.AdditionalAttributes = additionalAttributes

		actual.ConnectionDraining = &LoadBalancerConnectionDraining{}
		if lbAttributes.ConnectionDraining.Enabled != nil {
			actual.ConnectionDraining.Enabled = lbAttributes.ConnectionDraining.Enabled
		}
		if lbAttributes.ConnectionDraining.Timeout != nil {
			actual.ConnectionDraining.Timeout = lbAttributes.ConnectionDraining.Timeout
		}

		actual.ConnectionSettings = &LoadBalancerConnectionSettings{}
		if lbAttributes.ConnectionSettings.IdleTimeout != nil {
			actual.ConnectionSettings.IdleTimeout = lbAttributes.ConnectionSettings.IdleTimeout
		}

		actual.CrossZoneLoadBalancing = &LoadBalancerCrossZoneLoadBalancing{}
		if lbAttributes.CrossZoneLoadBalancing.Enabled != nil {
			actual.CrossZoneLoadBalancing.Enabled = lbAttributes.CrossZoneLoadBalancing.Enabled
		}
	}

	// Avoid spurious mismatches
	if subnetSlicesEqualIgnoreOrder(actual.Subnets, e.Subnets) {
		actual.Subnets = e.Subnets
	}
	if e.DNSName == nil {
		e.DNSName = actual.DNSName
	}
	if e.HostedZoneId == nil {
		e.HostedZoneId = actual.HostedZoneId
	}
	if e.ID == nil {
		e.ID = actual.ID
	}

	return actual, nil
}

func (e *LoadBalancer) Run(c *fi.Context) error {
	return fi.DefaultDeltaRunMethod(e, c)
}

func (s *LoadBalancer) CheckChanges(a, e, changes *LoadBalancer) error {
	if a == nil {
		if fi.StringValue(e.Name) == "" {
			return fi.RequiredField("Name")
		}
		if len(e.SecurityGroups) == 0 {
			return fi.RequiredField("SecurityGroups")
		}
		if len(e.Subnets) == 0 {
			return fi.RequiredField("Subnets")
		}

		if e.AccessLog != nil {
			if e.AccessLog.Enabled == nil {
				return fi.RequiredField("Acceslog.Enabled")
			}
			if *e.AccessLog.Enabled {
				if e.AccessLog.S3BucketName == nil {
					return fi.RequiredField("Acceslog.S3Bucket")
				}
			}
		}
		if e.ConnectionDraining != nil {
			if e.ConnectionDraining.Enabled == nil {
				return fi.RequiredField("ConnectionDraining.Enabled")
			}
		}
		//if e.ConnectionSettings != nil {
		//	if e.ConnectionSettings.IdleTimeout == nil {
		//		return fi.RequiredField("ConnectionSettings.IdleTimeout")
		//	}
		//}
		if e.CrossZoneLoadBalancing != nil {
			if e.CrossZoneLoadBalancing.Enabled == nil {
				return fi.RequiredField("CrossZoneLoadBalancing.Enabled")
			}
		}
	}

	return nil
}

func (_ *LoadBalancer) RenderAWS(t *awsup.AWSAPITarget, a, e, changes *LoadBalancer) error {
	elbName := e.ID
	if elbName == nil {
		elbName = e.Name
	}

	if elbName == nil {
		return fi.RequiredField("ID")
	}

	if a == nil {
		request := &elb.CreateLoadBalancerInput{}
		request.LoadBalancerName = elbName
		request.Scheme = e.Scheme

		for _, subnet := range e.Subnets {
			request.Subnets = append(request.Subnets, subnet.ID)
		}

		for _, sg := range e.SecurityGroups {
			request.SecurityGroups = append(request.SecurityGroups, sg.ID)
		}

		request.Listeners = []*elb.Listener{}

		for loadBalancerPort, listener := range e.Listeners {
			loadBalancerPortInt, err := strconv.ParseInt(loadBalancerPort, 10, 64)
			if err != nil {
				return fmt.Errorf("error parsing load balancer listener port: %q", loadBalancerPort)
			}
			awsListener := listener.mapToAWS(loadBalancerPortInt)
			request.Listeners = append(request.Listeners, awsListener)
		}

		glog.V(2).Infof("Creating ELB with Name:%q", *e.ID)

		response, err := t.Cloud.ELB().CreateLoadBalancer(request)
		if err != nil {
			return fmt.Errorf("error creating ELB: %v", err)
		}

		e.DNSName = response.DNSName
		e.ID = elbName

		lb, err := findLoadBalancer(t.Cloud, *e.ID)
		if err != nil {
			return err
		}
		if lb == nil {
			// TODO: Retry?  Is this async
			return fmt.Errorf("Unable to find newly created ELB")
		}

		e.HostedZoneId = lb.CanonicalHostedZoneNameID
	} else {
		if changes.Subnets != nil {
			return fmt.Errorf("subnet changes on LoadBalancer not yet implemented")
		}

		if changes.Listeners != nil {
			request := &elb.CreateLoadBalancerListenersInput{}
			request.LoadBalancerName = elbName

			for loadBalancerPort, listener := range changes.Listeners {
				loadBalancerPortInt, err := strconv.ParseInt(loadBalancerPort, 10, 64)
				if err != nil {
					return fmt.Errorf("error parsing load balancer listener port: %q", loadBalancerPort)
				}
				awsListener := listener.mapToAWS(loadBalancerPortInt)
				request.Listeners = append(request.Listeners, awsListener)
			}

			glog.V(2).Infof("Creating LoadBalancer listeners")

			_, err := t.Cloud.ELB().CreateLoadBalancerListeners(request)
			if err != nil {
				return fmt.Errorf("error creating LoadBalancerListeners: %v", err)
			}
		}
	}

	if changes.HealthCheck != nil && e.HealthCheck != nil {
		request := &elb.ConfigureHealthCheckInput{}
		request.LoadBalancerName = e.ID
		request.HealthCheck = &elb.HealthCheck{
			Target:             e.HealthCheck.Target,
			HealthyThreshold:   e.HealthCheck.HealthyThreshold,
			UnhealthyThreshold: e.HealthCheck.UnhealthyThreshold,
			Interval:           e.HealthCheck.Interval,
			Timeout:            e.HealthCheck.Timeout,
		}

		glog.V(2).Infof("Configuring health checks on ELB %q", *e.ID)

		_, err := t.Cloud.ELB().ConfigureHealthCheck(request)
		if err != nil {
			return fmt.Errorf("error configuring health checks on ELB: %v", err)
		}
	}
	return t.AddELBTags(*e.ID, t.Cloud.BuildTags(e.Name))
}

type terraformLoadBalancer struct {
	Name           *string                          `json:"name"`
	Listener       []*terraformLoadBalancerListener `json:"listener"`
	SecurityGroups []*terraform.Literal             `json:"security_groups"`
	Subnets        []*terraform.Literal             `json:"subnets"`
	Internal       *bool                            `json:"internal,omitempty"`

	HealthCheck *terraformLoadBalancerHealthCheck `json:"health_check,omitempty"`
	AccessLog   *terraformLoadBalancerAccessLog   `json:"access_logs,omitempty"`

	ConnectionDraining        *bool  `json:"connection_draining,omitempty"`
	ConnectionDrainingTimeout *int64 `json:"connection_draining_timeout,omitempty"`

	CrossZoneLoadBalancing *bool `json:"cross_zone_load_balancing,omitempty"`

	IdleTimeout *int64 `json:"idle_timeout,omitempty"`
}

type terraformLoadBalancerListener struct {
	InstancePort     int    `json:"instance_port"`
	InstanceProtocol string `json:"instance_protocol"`
	LBPort           int64  `json:"lb_port"`
	LBProtocol       string `json:"lb_protocol"`
}

type terraformLoadBalancerHealthCheck struct {
	Target             *string `json:"target"`
	HealthyThreshold   *int64  `json:"healthy_threshold"`
	UnhealthyThreshold *int64  `json:"unhealthy_threshold"`
	Interval           *int64  `json:"interval"`
	Timeout            *int64  `json:"timeout"`
}

func (_ *LoadBalancer) RenderTerraform(t *terraform.TerraformTarget, a, e, changes *LoadBalancer) error {
	elbName := e.ID
	if elbName == nil {
		elbName = e.Name
	}

	tf := &terraformLoadBalancer{
		Name: elbName,
	}
	if fi.StringValue(e.Scheme) == "internal" {
		tf.Internal = fi.Bool(true)
	}

	for _, subnet := range e.Subnets {
		tf.Subnets = append(tf.Subnets, subnet.TerraformLink())
	}

	for _, sg := range e.SecurityGroups {
		tf.SecurityGroups = append(tf.SecurityGroups, sg.TerraformLink())
	}

	for loadBalancerPort, listener := range e.Listeners {
		loadBalancerPortInt, err := strconv.ParseInt(loadBalancerPort, 10, 64)
		if err != nil {
			return fmt.Errorf("error parsing load balancer listener port: %q", loadBalancerPort)
		}

		tf.Listener = append(tf.Listener, &terraformLoadBalancerListener{
			InstanceProtocol: "TCP",
			InstancePort:     listener.InstancePort,
			LBPort:           loadBalancerPortInt,
			LBProtocol:       "TCP",
		})
	}

	if e.HealthCheck != nil {
		tf.HealthCheck = &terraformLoadBalancerHealthCheck{
			Target:             e.HealthCheck.Target,
			HealthyThreshold:   e.HealthCheck.HealthyThreshold,
			UnhealthyThreshold: e.HealthCheck.UnhealthyThreshold,
			Interval:           e.HealthCheck.Interval,
			Timeout:            e.HealthCheck.Timeout,
		}
	}

	if e.AccessLog != nil {
		tf.AccessLog = &terraformLoadBalancerAccessLog{
			EmitInterval:   e.AccessLog.EmitInterval,
			Enabled:        e.AccessLog.Enabled,
			S3BucketName:   e.AccessLog.S3BucketName,
			S3BucketPrefix: e.AccessLog.S3BucketPrefix,
		}
	}

	if e.ConnectionDraining != nil {
		tf.ConnectionDraining = e.ConnectionDraining.Enabled
		tf.ConnectionDrainingTimeout = e.ConnectionDraining.Timeout
	}

	if e.ConnectionSettings != nil {
		tf.IdleTimeout = e.ConnectionSettings.IdleTimeout
	}

	if e.CrossZoneLoadBalancing != nil {
		tf.CrossZoneLoadBalancing = e.CrossZoneLoadBalancing.Enabled
	}

	return t.RenderResource("aws_elb", *e.Name, tf)
}

func (e *LoadBalancer) TerraformLink(params ...string) *terraform.Literal {
	prop := "id"
	if len(params) > 0 {
		prop = params[0]
	}
	return terraform.LiteralProperty("aws_elb", *e.Name, prop)
}
