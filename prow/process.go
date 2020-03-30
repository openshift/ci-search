package prow

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

	"cloud.google.com/go/storage"
	"k8s.io/klog"

	"github.com/openshift/ci-search/testgrid/metadata/junit"
	"github.com/openshift/ci-search/testgrid/util/gcs"
)

// Build holds data to builds stored in GCS.
type Build = gcs.Build

// Builds holds a slice of builds, which will sort naturally (aka 2 < 10).
type Builds = gcs.Builds

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

// ReadBuild asynchronously downloads the files in build from gcs and convert them into a build.
func ReadBuild(inputBuild Build, acc Accumulator) error {
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

	// Download each artifact
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

	// Process each suite
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
				return fmt.Errorf("failed to read %s: %v", build, err)
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
		return err
	}

	select {
	case <-ctx.Done():
		cancel()
		return fmt.Errorf("interrupted reading %s", build)
	case err := <-ec:
		if err != nil {
			cancel()
			return fmt.Errorf("failed to read %s: %v", build, err)
		}
	}

	acc.Finished(ctx)

	cancel()
	return nil
}

func Days(days int) time.Duration {
	return time.Hour * 24 * time.Duration(days)
}

func NewAccumulator(base string, build *gcs.Build, modifiedBefore time.Time) (*LogAccumulator, bool) {
	prefix := filepath.FromSlash(build.Prefix)
	number := path.Base(build.Prefix)
	buildPath := filepath.Join(base, build.BucketPath, prefix)

	if !modifiedBefore.IsZero() {
		if fi, err := os.Stat(buildPath); err == nil {
			mod := fi.ModTime()
			if !mod.Before(modifiedBefore) {
				return nil, false
			}
		} else if !os.IsNotExist(err) {
			klog.Errorf("unable to read filesystem date: %v", err)
		}
	}

	exists := make(map[string]struct{})
	files, _ := ioutil.ReadDir(buildPath)
	for _, file := range files {
		exists[filepath.Base(file.Name())] = struct{}{}
	}

	return &LogAccumulator{
		build: build,

		path:   buildPath,
		number: number,

		exists: exists,

		hasMetadata: make(chan struct{}),
	}, true
}

type LogAccumulator struct {
	build     *gcs.Build
	path      string
	number    string
	succeeded bool

	started    int64
	finished   int64
	lastUpdate int64

	exists map[string]struct{}

	hasMetadata chan struct{}

	lock     sync.Mutex
	failures int
}

func (a *LogAccumulator) MarkCompleted(at time.Time) error {
	if err := os.MkdirAll(a.path, 0755); err != nil {
		return err
	}
	return os.Chtimes(a.path, at, at)
}

type fileTail struct {
	buf  [][]byte
	base string
}

func (t *fileTail) Write(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(path, t.base))
	if err != nil {
		return err
	}
	for _, buf := range t.buf {
		if _, err := f.Write(buf); err != nil {
			f.Close()
			os.Remove(f.Name())
			return err
		}
	}
	if err := f.Close(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	return nil
}

func (a *LogAccumulator) AddSuites(ctx context.Context, suites junit.Suites, meta map[string]string) {
	if _, ok := a.exists["junit.failures"]; ok {
		return
	}
	failures := 0
	var f *os.File
	for _, suite := range suites.Suites {
		for _, test := range suite.Results {
			if test.Failure == nil && test.Error == nil {
				continue
			}
			failures++
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

	a.lock.Lock()
	defer a.lock.Unlock()
	a.failures += failures
}

func (a *LogAccumulator) AddMetadata(ctx context.Context, started *gcs.Started, finished *gcs.Finished) (ok bool, err error) {
	defer close(a.hasMetadata)
	if started == nil || finished == nil || finished.Timestamp == nil {
		return false, nil
	}
	a.started = started.Timestamp
	a.finished = *finished.Timestamp
	a.succeeded = finished.Result == "SUCCESS"
	if a.finished > a.started {
		a.lastUpdate = a.finished
	} else {
		a.lastUpdate = a.started
	}
	if err := os.MkdirAll(a.path, 0755); err != nil {
		return false, fmt.Errorf("unable to create directory: %v", err)
	}
	if err := os.Chtimes(a.path, time.Unix(a.started, 0), time.Unix(a.started, 0)); err != nil {
		return false, fmt.Errorf("unable to set start time on directory: %v", err)
	}
	return true, nil
}

func (a *LogAccumulator) Finished(ctx context.Context) {
	if a.finished == 0 {
		// job isn't done, do nothing
		return
	}

	at := time.Unix(a.finished, 0)

	// update the timestamps of things we always write
	if err := os.Chtimes(a.path, at, at); err != nil && !os.IsNotExist(err) {
		klog.Errorf("Unable to set modification time of %s to %d: %v", a.path, a.finished, err)
	}
	for _, file := range []string{"junit.failures", "build-log.txt"} {
		_, ok := a.exists[file]
		if ok {
			continue
		}
		if err := os.Chtimes(filepath.Join(a.path, file), at, at); err != nil && !os.IsNotExist(err) {
			klog.Errorf("Unable to set modification time of %s to %d: %v", file, a.finished, err)
		}
	}
}

func (a *LogAccumulator) Started() int64 {
	return a.started
}

func (a *LogAccumulator) LastUpdate() int64 {
	return a.lastUpdate
}

func (a *LogAccumulator) waitMetadata(ctx context.Context) bool {
	select {
	case <-a.hasMetadata:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *LogAccumulator) downloadIfMissing(ctx context.Context, artifact *storage.ObjectAttrs, base string) error {
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
	h := a.build.Bucket.Object(artifact.Name)
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

func (a *LogAccumulator) downloadIfMissingTail(ctx context.Context, artifact *storage.ObjectAttrs, base string, length int64) error {
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
	h := a.build.Bucket.Object(artifact.Name)

	var r *storage.Reader
	r, err = h.NewRangeReader(ctx, -length, -1)
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

func (a *LogAccumulator) Artifacts(ctx context.Context, artifacts <-chan *storage.ObjectAttrs, unprocessedArtifacts chan<- *storage.ObjectAttrs) error {
	var wg sync.WaitGroup
	ec := make(chan error)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for art := range artifacts {
		var rel string
		if strings.HasPrefix(art.Name, a.build.Prefix) {
			rel = art.Name[len(a.build.Prefix):]
		}
		switch {
		case rel == "build-log.txt":
			wg.Add(1)
			go func(art *storage.ObjectAttrs) {
				defer wg.Done()
				// if we can't get metadata, haven't finished, or haven't failed, don't download build log
				if !a.waitMetadata(ctx) || a.succeeded || a.finished == 0 {
					return
				}
				if err := a.downloadIfMissingTail(ctx, art, "build-log.txt", 20*1024*1024); err != nil {
					log.Printf("error: Unable to download %s: %v", art.Name, err)
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
