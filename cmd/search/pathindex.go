package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

type PathAccessor interface {
	// SearchPaths searches for paths matching the index's SearchType
	// and MaxAge, and returns them as a slice of filesystem paths.
	SearchPaths(*Index, []string) []string

	// Stats returns aggregate statistics for the indexed paths.
	Stats() PathIndexStats
}

type PathIndexStats struct {
	Entries int
	Size    int64
}

type Result struct {
	// FailedAt is the time when the job failed.
	FailedAt time.Time

	// JobURI is the job detail page, e.g. https://prow.svc.ci.openshift.org/view/gcs/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309
	JobURI *url.URL

	// FileType is the type of file where the match was found, e.g. "build-log" or "junit".
	FileType string

	// Trigger is "pull" or "build".
	Trigger string

	// Name is the name of the job, e.g. release-openshift-ocp-installer-e2e-aws-4.1 or pull-ci-openshift-origin-master-e2e-aws.
	Name string

	// Number is the job number, e.g. 309 for origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 or 5466 for origin-ci-test/pr-logs/pull/openshift_installer/1650/pull-ci-openshift-installer-master-e2e-aws/5466.
	Number int
}

type ResultMetadata interface {
	// MetadataFor returns metadata for the slash-separated path
	// resolved relative to the index base.
	MetadataFor(path string) (*Result, bool)
}

type pathIndex struct {
	base    string
	baseURI *url.URL
	maxAge  time.Duration

	lock      sync.Mutex
	ordered   []pathAge
	stats     PathIndexStats
	pathIndex map[string]int
}

type pathAge struct {
	path  string
	index string
	age   time.Time
}

func (index *pathIndex) parseJobPath(path string) (*Result, error) {
	result := &Result{}

	parts := strings.SplitN(path, "/", 8)
	last := len(parts) - 1

	result.JobURI = index.baseURI.ResolveReference(&url.URL{Path: strings.Join(parts[:last], "/")})

	switch parts[last] {
	case "build-log.txt":
		result.FileType = "build-log"
	case "junit.failures":
		result.FileType = "junit"
	default:
		result.FileType = parts[last]
	}

	var err error
	result.Number, err = strconv.Atoi(parts[last-1])
	if err != nil {
		return result, err
	}

	if last < 3 {
		return result, fmt.Errorf("not enough parts (%d < 3)", last)
	}
	result.Name = parts[last-2]

	switch parts[1] {
	case "logs":
		result.Trigger = "build"
	case "pr-logs":
		result.Trigger = "pull"
	default:
		result.Trigger = parts[1]
	}

	return result, nil
}

func (index *pathIndex) MetadataFor(path string) (*Result, bool) {
	result, err := index.parseJobPath(path)
	if err != nil {
		glog.Errorf("Failed to parse job path for %q: %v", path, err)
		return result, false
	}

	index.lock.Lock()
	position, ok := index.pathIndex[path]
	if ok {
		result.FailedAt = index.ordered[position].age
	}
	index.lock.Unlock()
	if !ok {
		return result, false
	}

	return result, true
}

func (index *pathIndex) Load() error {
	ordered := make([]pathAge, 0, 1024)

	var err error
	start := time.Now()
	defer func() {
		glog.Infof("Refreshed path index in %s, loaded %d: %v", time.Now().Sub(start).Truncate(time.Millisecond), len(ordered), err)
	}()

	mustExpire := index.maxAge != 0
	expiredAt := start.Add(-index.maxAge)

	stats := PathIndexStats{}

	err = filepath.Walk(index.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if mustExpire && expiredAt.After(info.ModTime()) {
			os.RemoveAll(path)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(index.base, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		switch info.Name() {
		case "build-log.txt":
			stats.Entries++
			stats.Size += info.Size()
			ordered = append(ordered, pathAge{index: "build-log", path: relPath, age: info.ModTime()})
		case "junit.failures":
			stats.Entries++
			stats.Size += info.Size()
			ordered = append(ordered, pathAge{index: "junit", path: relPath, age: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return err
	}

	sort.Slice(ordered, func(i, j int) bool { return ordered[i].age.After(ordered[j].age) })
	pathIndex := make(map[string]int, len(ordered))
	for i, item := range ordered {
		path := strings.TrimPrefix(item.path, index.base)
		pathIndex[path] = i
	}

	index.lock.Lock()
	defer index.lock.Unlock()
	index.ordered = ordered
	index.pathIndex = pathIndex
	index.stats = stats

	return nil
}

func (i *pathIndex) Stats() PathIndexStats {
	i.lock.Lock()
	defer i.lock.Unlock()
	return i.stats
}

func (i *pathIndex) SearchPaths(index *Index, initial []string) []string {
	var paths []pathAge
	i.lock.Lock()
	paths = i.ordered
	i.lock.Unlock()

	// search all if we haven't built an index yet
	if len(paths) == 0 {
		return append(initial, i.base)
	}

	// grow the map to the desired size up front
	if len(paths) > len(initial) {
		copied := make([]string, len(initial), len(initial)+len(paths))
		copy(copied, initial)
		initial = copied
	}

	all := len(index.SearchType) == 0 || index.SearchType == "all"

	var oldest time.Time
	if index.MaxAge > 0 {
		oldest = time.Now().Add(-index.MaxAge)
	}

	var grown bool
	for _, path := range paths {
		if index.Job != nil {
			metadata, err := i.parseJobPath(path.path)
			if err == nil && index.Job.FindStringIndex(metadata.Name) == nil {
				continue
			}
		}
		if path.age.Before(oldest) {
			break
		}
		if all || path.index == index.SearchType {
			initial = append(initial, filepath.Join(i.base, filepath.FromSlash(path.path)))
			grown = true
		}
	}

	if !grown {
		return nil
	}

	return initial
}
