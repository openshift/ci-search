/*
Copyright 2018 The Kubernetes Authors.

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
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/openshift/ci-search/testgrid/util/gcs"
)

// options configures the updater
type options struct {
	config string
	creds  string
	group  string
	path   string

	groupConcurrency int
	buildConcurrency int

	maxResults    int
	finishedAfter int64
}

// validate ensures sane options
func (o *options) validate() error {
	if o.config == "" {
		return errors.New("empty --config")
	}
	if strings.HasPrefix(o.config, "gs://") {
		if _, err := gcs.NewPath(o.config); err != nil {
			return fmt.Errorf("invalid --config: %v", err)
		}
	}
	if o.groupConcurrency == 0 {
		o.groupConcurrency = 4 * runtime.NumCPU()
	}
	if o.buildConcurrency == 0 {
		o.buildConcurrency = 4 * runtime.NumCPU()
	}

	return nil
}

// gatherOptions reads options from flags
func gatherOptions() options {
	o := options{}
	flag.StringVar(&o.config, "config", "", "Path to a local config file or a gs proto path (gs://path/to/config.pb)")
	flag.StringVar(&o.path, "path", "", "Local directory to write data to")
	flag.StringVar(&o.creds, "gcp-service-account", "", "/path/to/gcp/creds (use local creds if empty)")
	flag.StringVar(&o.group, "test-group", "", "Only update named group if set")
	flag.IntVar(&o.groupConcurrency, "group-concurrency", 0, "Manually define the number of groups to concurrently update if non-zero")
	flag.IntVar(&o.buildConcurrency, "build-concurrency", 0, "Manually define the number of builds to concurrently read if non-zero")

	flag.IntVar(&o.maxResults, "max-results", 200, "The maximum number of build results per group to read in any pass")
	flag.Int64Var(&o.finishedAfter, "finished-after", time.Now().Add(-14*24*time.Hour).Unix(), "A Unix timestamp that a build must be newer than to be considered")

	flag.Parse()
	return o
}

// testGroupPath() returns the path to a test_group proto given this proto
func testGroupPath(g gcs.Path, name string) (*gcs.Path, error) {
	u, err := url.Parse(name)
	if err != nil {
		return nil, fmt.Errorf("invalid url %s: %v", name, err)
	}
	np, err := g.ResolveReference(u)
	if err == nil && np.Bucket() != g.Bucket() {
		return nil, fmt.Errorf("testGroup %s should not change bucket", name)
	}
	return np, nil
}

func main() {
	opt := gatherOptions()
	if err := opt.validate(); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	ctx := context.Background()
	summarize := &LogExtractor{
		path: opt.path,
	}
	if err := ProcessBuilds(ctx, opt, summarize); err != nil {
		log.Fatalf("%v", err)
	}
}
