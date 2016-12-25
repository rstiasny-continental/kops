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

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
	"io/ioutil"
	"k8s.io/kops/cloudmock/aws/mockec2"
	"k8s.io/kops/cloudmock/aws/mockroute53"
	"k8s.io/kops/cmd/kops/util"
	"k8s.io/kops/pkg/diff"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/util/pkg/vfs"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMinimal runs the test on a minimum configuration, similar to kops create cluster minimal.example.com --zones us-west-1a
func TestMinimal(t *testing.T) {
	runTest(t, "minimal.example.com", "../../tests/integration/minimal", "v1alpha0", false)
	runTest(t, "minimal.example.com", "../../tests/integration/minimal", "v1alpha1", false)
	runTest(t, "minimal.example.com", "../../tests/integration/minimal", "v1alpha2", false)
}

// TestMinimal_141 runs the test on a configuration from 1.4.1 release
func TestMinimal_141(t *testing.T) {
	runTest(t, "minimal-141.example.com", "../../tests/integration/minimal-141", "v1alpha0", false)
}

// TestPrivateWeave runs the test on a configuration with private topology, weave networking
func TestPrivateWeave(t *testing.T) {
	runTest(t, "privateweave.example.com", "../../tests/integration/privateweave", "v1alpha1", true)
	runTest(t, "privateweave.example.com", "../../tests/integration/privateweave", "v1alpha2", true)
}

// TestPrivateCalico runs the test on a configuration with private topology, calico networking
func TestPrivateCalico(t *testing.T) {
	runTest(t, "privatecalico.example.com", "../../tests/integration/privatecalico", "v1alpha1", true)
	runTest(t, "privatecalico.example.com", "../../tests/integration/privatecalico", "v1alpha2", true)
}

func runTest(t *testing.T, clusterName string, srcDir string, version string, private bool) {
	var stdout bytes.Buffer

	inputYAML := "in-" + version + ".yaml"
	expectedTFPath := "kubernetes.tf"

	factoryOptions := &util.FactoryOptions{}
	factoryOptions.RegistryPath = "memfs://tests"

	h := NewIntegrationTestHarness(t)
	defer h.Close()

	h.SetupMockAWS()

	factory := util.NewFactory(factoryOptions)

	{
		options := &CreateOptions{}
		options.Filenames = []string{path.Join(srcDir, inputYAML)}

		err := RunCreate(factory, &stdout, options)
		if err != nil {
			t.Fatalf("error running %q create: %v", inputYAML, err)
		}
	}

	{
		options := &CreateSecretPublickeyOptions{}
		options.ClusterName = clusterName
		options.Name = "admin"
		options.PublicKeyPath = path.Join(srcDir, "id_rsa.pub")

		err := RunCreateSecretPublicKey(factory, &stdout, options)
		if err != nil {
			t.Fatalf("error running %q create: %v", inputYAML, err)
		}
	}

	{
		options := &UpdateClusterOptions{}
		options.InitDefaults()
		options.Target = "terraform"
		options.OutDir = path.Join(h.TempDir, "out")
		options.MaxTaskDuration = 30 * time.Second

		// We don't test it here, and it adds a dependency on kubectl
		options.CreateKubecfg = false

		err := RunUpdateCluster(factory, clusterName, &stdout, options)
		if err != nil {
			t.Fatalf("error running update cluster %q: %v", clusterName, err)
		}
	}

	// Compare main files
	{
		files, err := ioutil.ReadDir(path.Join(h.TempDir, "out"))
		if err != nil {
			t.Fatalf("failed to read dir: %v", err)
		}

		var fileNames []string
		for _, f := range files {
			fileNames = append(fileNames, f.Name())
		}
		sort.Strings(fileNames)

		actualFilenames := strings.Join(fileNames, ",")
		expectedFilenames := "data,kubernetes.tf"
		if actualFilenames != expectedFilenames {
			t.Fatalf("unexpected files.  actual=%q, expected=%q", actualFilenames, expectedFilenames)
		}

		actualTF, err := ioutil.ReadFile(path.Join(h.TempDir, "out", "kubernetes.tf"))
		if err != nil {
			t.Fatalf("unexpected error reading actual terraform output: %v", err)
		}
		expectedTF, err := ioutil.ReadFile(path.Join(srcDir, expectedTFPath))
		if err != nil {
			t.Fatalf("unexpected error reading expected terraform output: %v", err)
		}

		if !bytes.Equal(actualTF, expectedTF) {
			diffString := diff.FormatDiff(string(expectedTF), string(actualTF))
			t.Logf("diff:\n%s\n", diffString)

			t.Fatalf("terraform output differed from expected")
		}
	}

	// Compare data files
	{
		files, err := ioutil.ReadDir(path.Join(h.TempDir, "out", "data"))
		if err != nil {
			t.Fatalf("failed to read data dir: %v", err)
		}

		var actualFilenames []string
		for _, f := range files {
			actualFilenames = append(actualFilenames, f.Name())
		}

		expectedFilenames := []string{
			"aws_iam_role_masters." + clusterName + "_policy",
			"aws_iam_role_nodes." + clusterName + "_policy",
			"aws_iam_role_policy_masters." + clusterName + "_policy",
			"aws_iam_role_policy_nodes." + clusterName + "_policy",
			"aws_key_pair_kubernetes." + clusterName + "-c4a6ed9aa889b9e2c39cd663eb9c7157_public_key",
			"aws_launch_configuration_master-us-test-1a.masters." + clusterName + "_user_data",
			"aws_launch_configuration_nodes." + clusterName + "_user_data",
		}

		if private {
			expectedFilenames = append(expectedFilenames, []string{
				"aws_iam_role_bastions." + clusterName + "_policy",
				"aws_iam_role_policy_bastions." + clusterName + "_policy",

				// bastions don't have any userdata
				// "aws_launch_configuration_bastions." + clusterName + "_user_data",
			}...)
		}
		sort.Strings(expectedFilenames)
		if !reflect.DeepEqual(actualFilenames, expectedFilenames) {
			t.Fatalf("unexpected data files.  actual=%q, expected=%q", actualFilenames, expectedFilenames)
		}

		// TODO: any verification of data files?
	}
}

type IntegrationTestHarness struct {
	TempDir string
	T       *testing.T
}

func NewIntegrationTestHarness(t *testing.T) *IntegrationTestHarness {
	h := &IntegrationTestHarness{}
	tempDir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	h.TempDir = tempDir

	vfs.Context.ResetMemfsContext(true)

	return h
}

func (h *IntegrationTestHarness) Close() {
	if h.TempDir != "" {
		if os.Getenv("KEEP_TEMP_DIR") != "" {
			glog.Infof("NOT removing temp directory, because KEEP_TEMP_DIR is set: %s", h.TempDir)
		} else {
			err := os.RemoveAll(h.TempDir)
			if err != nil {
				h.T.Fatalf("failed to remove temp dir %q: %v", h.TempDir, err)
			}
		}
	}
}

func (h *IntegrationTestHarness) SetupMockAWS() {
	cloud := awsup.InstallMockAWSCloud("us-test-1", "abc")
	mockEC2 := &mockec2.MockEC2{}
	cloud.MockEC2 = mockEC2
	mockRoute53 := &mockroute53.MockRoute53{}
	cloud.MockRoute53 = mockRoute53

	mockRoute53.Zones = append(mockRoute53.Zones, &route53.HostedZone{
		Id:   aws.String("/hostedzone/Z1AFAKE1ZON3YO"),
		Name: aws.String("example.com."),
	})

	mockEC2.Images = append(mockEC2.Images, &ec2.Image{
		ImageId: aws.String("ami-12345678"),
		Name:    aws.String("k8s-1.4-debian-jessie-amd64-hvm-ebs-2016-10-21"),
		OwnerId: aws.String(awsup.WellKnownAccountKopeio),
	})
}

func MakeSSHKeyPair(publicKeyPath string, privateKeyPath string) error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return err
	}

	var privateKeyBytes bytes.Buffer
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	if err := pem.Encode(&privateKeyBytes, privateKeyPEM); err != nil {
		return err
	}
	if err := ioutil.WriteFile(privateKeyPath, privateKeyBytes.Bytes(), os.FileMode(0700)); err != nil {
		return err
	}

	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)
	if err := ioutil.WriteFile(publicKeyPath, publicKeyBytes, os.FileMode(0744)); err != nil {
		return err
	}

	return nil
}
