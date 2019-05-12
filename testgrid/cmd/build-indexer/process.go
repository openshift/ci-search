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
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/openshift/ci-search/testgrid/config"
	"github.com/openshift/ci-search/testgrid/metadata/junit"
	"github.com/openshift/ci-search/testgrid/util/gcs"

	"cloud.google.com/go/storage"
)

// Build holds data to builds stored in GCS.
type Build = gcs.Build

// Builds holds a slice of builds, which will sort naturally (aka 2 < 10).
type Builds = gcs.Builds

type GroupSummarizer interface {
	New(config.TestGroup) Summarizer
}

type Summarizer interface {
	New(*gcs.Build) Accumulator
}

type Accumulator interface {
	Artifacts(context.Context, <-chan *storage.ObjectAttrs, chan<- *storage.ObjectAttrs) error
	AddSuites(context.Context, junit.Suites, map[string]string)
	AddMetadata(context.Context, *gcs.Started, *gcs.Finished) (ok bool, err error)
	Finished(context.Context)

	Started() int64
	// LastUpdate is the finished or started date.
	LastUpdate() int64
}

func ProcessBuilds(ctx context.Context, opt options, summarizer GroupSummarizer) error {
	client, err := gcs.ClientWithCreds(ctx, opt.creds)
	if err != nil {
		return fmt.Errorf("Failed to create storage client: %v", err)
	}

	var cfg *config.Configuration
	if strings.HasPrefix(opt.config, "gs://") {
		path, err := gcs.NewPath(opt.config)
		if err != nil {
			return err
		}
		cfgFromGCS, err := config.ReadGCS(ctx, client.Bucket(path.Bucket()).Object(path.Object()))
		if err != nil {
			return fmt.Errorf("Failed to read %s: %v", opt.config, err)
		}
		cfg = cfgFromGCS
	} else {
		data, err := ioutil.ReadFile(opt.config)
		if err != nil {
			return err
		}
		cfg = &config.Configuration{}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return err
		}
	}

	log.Printf("Found %d groups", len(cfg.TestGroups))

	finishedAfter := time.Unix(opt.finishedAfter, 0)

	groups := make(chan config.TestGroup)
	var wg sync.WaitGroup

	for i := 0; i < opt.groupConcurrency; i++ {
		wg.Add(1)
		go func() {
			for tg := range groups {
				s := summarizer.New(tg)

				if err := updateGroup(ctx, client, tg, opt.buildConcurrency, opt.maxResults, finishedAfter, s); err != nil {
					log.Printf("FAIL: %v", err)
				}
			}
			wg.Done()
		}()
	}

	if opt.group != "" { // Just a specific group
		// o := "ci-kubernetes-test-go"
		// o = "ci-kubernetes-node-kubelet-stable3"
		// gs://kubernetes-jenkins/logs/ci-kubernetes-test-go
		// gs://kubernetes-jenkins/pr-logs/pull-ingress-gce-e2e
		o := opt.group
		tg := cfg.FindTestGroup(o)
		if tg == nil {
			log.Fatalf("Failed to find %s in %s", o, opt.config)
		}
		groups <- *tg
	} else { // All groups
		for _, tg := range cfg.TestGroups {
			groups <- *tg
		}
	}
	close(groups)
	wg.Wait()
	return nil
}

// ReadBuild asynchronously downloads the files in build from gcs and convert them into a build.
func ReadBuild(inputBuild Build, sum Summarizer) (Accumulator, error) {
	var wg sync.WaitGroup                                                  // Each subtask does wg.Add(1), then we wg.Wait() for them to finish
	ctx, cancel := context.WithTimeout(inputBuild.Context, 30*time.Second) // Allows aborting after first error
	build := inputBuild
	build.Context = ctx
	ec := make(chan error) // Receives errors from anyone

	// Download started.json, send to sc
	wg.Add(1)
	sc := make(chan gcs.Started) // Receives started.json result
	go func() {
		defer wg.Done()
		started, err := build.Started()
		if err != nil {
			select {
			case <-ctx.Done():
			case ec <- err:
			}
			return
		}
		select {
		case <-ctx.Done():
		case sc <- *started:
		}
	}()

	// Download finished.json, send to fc
	wg.Add(1)
	fc := make(chan gcs.Finished) // Receives finished.json result
	go func() {
		defer wg.Done()
		finished, err := build.Finished()
		if err != nil {
			select {
			case <-ctx.Done():
			case ec <- err:
			}
			return
		}
		select {
		case <-ctx.Done():
		case fc <- *finished:
		}
	}()

	// List artifacts to the artifacts channel
	wg.Add(1)
	artifacts := make(chan *storage.ObjectAttrs) // Receives objects
	go func() {
		defer wg.Done()
		defer close(artifacts) // No more artifacts
		if err := build.Artifacts(artifacts); err != nil {
			select {
			case <-ctx.Done():
			case ec <- err:
			}
		}
	}()

	acc := sum.New(&build)

	// Download each artifact, send row map to rc
	// With parallelism: 60s without: 220s
	wg.Add(1)
	suiteArtifacts := make(chan *storage.ObjectAttrs)
	go func() {
		defer wg.Done()
		defer close(suiteArtifacts)
		if err := acc.Artifacts(ctx, artifacts, suiteArtifacts); err != nil {
			select {
			case <-ctx.Done():
			case ec <- err:
			}
		}
	}()

	// Download each artifact, send row map to rc
	// With parallelism: 60s without: 220s
	wg.Add(1)
	suitesChan := make(chan gcs.SuitesMeta)
	go func() {
		defer wg.Done()
		defer close(suitesChan)
		if err := build.Suites(suiteArtifacts, suitesChan); err != nil {
			select {
			case <-ctx.Done():
			case ec <- err:
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for suitesMeta := range suitesChan {
			acc.AddSuites(ctx, suitesMeta.Suites, suitesMeta.Metadata)
		}
	}()

	// Wait for everyone to complete their work
	go func() {
		wg.Wait()
		select {
		case <-ctx.Done():
			return
		case ec <- nil:
		}
	}()
	var finished *gcs.Finished
	var started *gcs.Started
	for { // Wait until we receive started and finished and/or an error
		select {
		case err := <-ec:
			if err != nil {
				cancel()
				return nil, fmt.Errorf("failed to read %s: %v", build, err)
			}
			break
		case s := <-sc:
			started = &s
		case f := <-fc:
			finished = &f
		}
		if started != nil && finished != nil {
			break
		}
	}
	if ok, err := acc.AddMetadata(ctx, started, finished); !ok || err != nil {
		cancel()
		return acc, err
	}

	select {
	case <-ctx.Done():
		cancel()
		return nil, fmt.Errorf("interrupted reading %s", build)
	case err := <-ec:
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to read %s: %v", build, err)
		}
	}

	acc.Finished(ctx)

	cancel()
	return acc, nil
}

// ReadBuilds will asynchronously construct a Grid for the group out of the specified builds.
func ReadBuilds(parent context.Context, group config.TestGroup, builds Builds, retrievedIn time.Duration, max int, finishedAfter time.Time, concurrency int, sum Summarizer) error {
	// Spawn build readers
	if concurrency == 0 {
		return fmt.Errorf("zero readers for %s", group.Name)
	}
	ctx, cancel := context.WithCancel(parent)
	lb := len(builds)
	if lb > max {
		log.Printf("  Truncating %d %s results to %d", lb, group.Name, max)
		lb = max
	}
	finishedUnix := finishedAfter.Unix()
	acc := make([]Accumulator, lb)
	log.Printf("UPDATE: %s since %s (%d), list took %s", group.Name, finishedAfter, finishedUnix, retrievedIn.Truncate(time.Second))
	ec := make(chan error)
	old := make(chan int)
	var wg sync.WaitGroup

	// Send build indices to readers
	indices := make(chan int)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(indices)
		for i := range builds[:lb] {
			select {
			case <-ctx.Done():
				return
			case <-old:
				return
			case indices <- i:
			}
		}
	}()

	// Concurrently receive indices and read builds
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, open := <-indices:
					if !open {
						return
					}
					b := builds[i]
					var err error
					var buildAcc Accumulator
					for i := 2; i >= 0; i++ {
						buildAcc, err = ReadBuild(b, sum)
						if err == nil {
							break
						}
						log.Printf("RETRY: Failed to read %s, retrying: %v", b, err)
						time.Sleep(5 * time.Second)
					}
					if err != nil {
						ec <- err
						return
					}
					acc[i] = buildAcc
					if lastUpdate := buildAcc.LastUpdate(); lastUpdate < finishedUnix && lastUpdate != 0 {
						select {
						case <-ctx.Done():
						case old <- i:
							log.Printf("STOP: %d %s started at %d < %d", i, b.Prefix, lastUpdate, finishedUnix)
						default: // Someone else may have already reported an old result
						}
					}
				}
			}
		}()
	}

	// Wait for everyone to finish
	go func() {
		wg.Wait()
		select {
		case <-ctx.Done():
		case ec <- nil: // No error
		}
	}()

	// Determine if we got an error
	select {
	case <-ctx.Done():
		cancel()
		return fmt.Errorf("interrupted reading %s", group.Name)
	case err := <-ec:
		if err != nil {
			cancel()
			return fmt.Errorf("error reading %s: %v", group.Name, err)
		}
	}

	cancel()
	return nil
}

func updateGroup(ctx context.Context, client *storage.Client, tg config.TestGroup, concurrency int, maxResults int, finishedAt time.Time, sum Summarizer) error {
	o := tg.Name

	var tgPath gcs.Path
	if err := tgPath.Set("gs://" + tg.GcsPrefix); err != nil {
		return fmt.Errorf("group %s has an invalid gcs_prefix %s: %v", o, tg.GcsPrefix, err)
	}

	// g := state.Grid{}
	// g.Columns = append(g.Columns, &state.Column{Build: "first", Started: 1})
	log.Printf("LIST: %s", tgPath)
	start := time.Now()
	builds, err := gcs.ListBuilds(ctx, client, tgPath)
	if err != nil {
		return fmt.Errorf("failed to list %s builds: %v", o, err)
	}
	duration := time.Now().Sub(start)
	if err := ReadBuilds(ctx, tg, builds, duration, maxResults, finishedAt, concurrency, sum); err != nil {
		return err
	}
	return nil
}

func Days(days int) time.Duration {
	return time.Hour * 24 * time.Duration(days)
}
