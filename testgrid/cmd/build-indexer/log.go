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
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"

	"github.com/openshift/ci-search/testgrid/config"
	"github.com/openshift/ci-search/testgrid/metadata/junit"
	"github.com/openshift/ci-search/testgrid/util/gcs"
)

type LogExtractor struct {
	path string
}

func (e *LogExtractor) New(testGroup config.TestGroup) Summarizer {
	return &LogSummarizer{
		group:     &testGroup,
		extractor: e,
	}
}

type LogSummarizer struct {
	group     *config.TestGroup
	extractor *LogExtractor
}

func (s *LogSummarizer) New(build *gcs.Build) Accumulator {
	prefix := filepath.FromSlash(build.Prefix)
	number := path.Base(build.Prefix)
	buildPath := filepath.Join(s.extractor.path, build.BucketPath, prefix)

	exists := make(map[string]struct{})
	files, _ := ioutil.ReadDir(buildPath)
	for _, file := range files {
		exists[filepath.Base(file.Name())] = struct{}{}
	}

	return &LogAccumulator{
		summarizer: s,
		build:      build,

		path:   buildPath,
		number: number,

		exists: exists,
	}
}

type LogAccumulator struct {
	summarizer *LogSummarizer
	build      *gcs.Build
	path       string
	number     string

	started    int64
	finished   int64
	lastUpdate int64

	exists map[string]struct{}
}

func (a *LogAccumulator) AddSuites(ctx context.Context, suites junit.Suites, meta map[string]string) {
	if _, ok := a.exists["junit.failures"]; ok {
		return
	}
	var f *os.File
	for _, suite := range suites.Suites {
		for _, test := range suite.Results {
			if test.Failure == nil && test.Error == nil {
				continue
			}
			if f == nil {
				if err := os.MkdirAll(a.path, 0755); err != nil {
					log.Printf("unable to create test dir: %v", err)
					return
				}
				var err error
				f, err = os.OpenFile(filepath.Join(a.path, "junit.failures"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
				if err != nil {
					log.Printf("Unable to open local test summary file: %v", err)
					return
				}
				defer func() {
					if err := f.Close(); err != nil {
						log.Printf("Unable to close local test summary file: %v", err)
					}
				}()
			}

			var out string
			switch {
			case test.Failure != nil:
				out = *test.Failure
			case test.Error != nil:
				out = *test.Error
			}
			fmt.Fprintf(f, "\n\n# %s\n", test.Name)
			fmt.Fprintf(f, out)
		}
	}
}

func (a *LogAccumulator) AddMetadata(ctx context.Context, started *gcs.Started, finished *gcs.Finished) (ok bool, err error) {
	if started == nil || finished == nil || finished.Timestamp == nil {
		return false, nil
	}
	a.started = started.Timestamp
	a.finished = *finished.Timestamp
	if a.finished > a.started {
		a.lastUpdate = a.finished
	} else {
		a.lastUpdate = a.started
	}
	return true, nil
}

func (a *LogAccumulator) Finished(ctx context.Context) {
	if a.finished > 0 {
		for _, file := range []string{"junit.failures"} {
			_, ok := a.exists[file]
			if ok {
				continue
			}
			at := time.Unix(a.finished, 0)
			if err := os.Chtimes(filepath.Join(a.path, file), at, at); err != nil && !os.IsNotExist(err) {
				glog.Errorf("Unable to set modification time of %s to %d: %v", file, a.finished, err)
			}
			if err := os.Chtimes(a.path, at, at); err != nil && !os.IsNotExist(err) {
				glog.Errorf("Unable to set modification time of %s to %d: %v", file, a.finished, err)
			}
		}
	}
}

func (a *LogAccumulator) Started() int64 {
	return a.started
}

func (a *LogAccumulator) LastUpdate() int64 {
	return a.lastUpdate
}

func (a *LogAccumulator) downloadIfMissing(ctx context.Context, artifact, base string) error {
	if _, ok := a.exists[base]; ok {
		return nil
	}
	if err := os.MkdirAll(a.path, 0755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(a.path, base))
	if err != nil {
		return err
	}
	h := a.build.Bucket.Object(artifact)
	r, err := h.NewReader(ctx)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	defer r.Close()
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	return nil
}

func (a *LogAccumulator) Artifacts(ctx context.Context, artifacts <-chan string, unprocessedArtifacts chan<- string) error {
	var wg sync.WaitGroup
	ec := make(chan error)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for art := range artifacts {
		switch {
		case strings.HasSuffix(art, "e2e.log"):
			break

			// TODO: enable later
			wg.Add(1)
			go func(art string) {
				defer wg.Done()
				if err := a.downloadIfMissing(ctx, art, "e2e.log"); err != nil {
					log.Printf("error: Unable to download %s: %v", art, err)
					select {
					case <-ctx.Done():
					case ec <- err:
					}
				}
			}(art)

		default:
			unprocessedArtifacts <- art
			continue
		}
	}

	go func() {
		wg.Wait()
		select {
		case ec <- nil: // tell parent we exited cleanly
		case <-ctx.Done(): // parent already exited
		}
		close(ec) // no one will send t
	}()

	// TODO(fejta): refactor to return the suites chan, so we can control channel closure
	// Until then don't return until all go functions return
	select {
	case <-ctx.Done(): // parent context marked as finished.
		wg.Wait()
		return ctx.Err()
	case err := <-ec: // finished listing
		cancel()
		wg.Wait()
		return err
	}
}
