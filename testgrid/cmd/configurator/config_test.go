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
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	config_pb "github.com/openshift/ci-search/testgrid/config"
)

type SQConfig struct {
	Data map[string]string `yaml:"data,omitempty"`
}

var (
	companies = []string{
		"canonical",
		"cos",
		"cri-o",
		"istio",
		"google",
		"kopeio",
		"redhat",
		"vmware",
	}
	orgs = []string{
		"conformance",
		"presubmits",
		"sig",
		"wg",
	}
	prefixes = [][]string{orgs, companies}
)

// Shared testgrid config, loaded at TestMain.
var cfg *config_pb.Configuration

func TestMain(m *testing.M) {
	//make sure we can parse config.yaml
	yamlData, err := ioutil.ReadFile("../../config.yaml")
	if err != nil {
		fmt.Printf("IO Error : Cannot Open File config.yaml")
		os.Exit(1)
	}

	c := Config{}
	if err := c.Update(yamlData); err != nil {
		fmt.Printf("Yaml2Proto - Conversion Error %v", err)
		os.Exit(1)
	}

	cfg, err = c.Raw()
	if err != nil {
		fmt.Printf("Error validating config: %v", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
