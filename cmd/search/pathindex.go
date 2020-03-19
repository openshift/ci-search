package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog"
)

type PathSearcher interface {
	// SearchPaths searches for paths matching the index's SearchType
	// and MaxAge, and returns them as a slice of filesystem paths.
	SearchPaths(*Index, []string) ([]string, error)
}

type PathAccessor interface {
	// Stats returns aggregate statistics for the indexed paths.
	Stats() PathIndexStats

	// LastModified returns the time the requested path reported a failure at
	// or the zero time.
	LastModified(path string) time.Time
}

type PathIndexStats struct {
	Entries int
	Size    int64
}

type PathResolver interface {
	// MetadataFor returns metadata for the slash-separated path
	// resolved relative to the index base.
	MetadataFor(path string) (*Result, error)
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
	var result Result

	parts := strings.SplitN(path, "/", 8)
	last := len(parts) - 1

	result.URI = index.baseURI.ResolveReference(&url.URL{Path: strings.Join(parts[:last], "/")})

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
		return nil, err
	}

	if last < 3 {
		return nil, fmt.Errorf("not enough parts (%d < 3)", last)
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

	return &result, nil
}

func (index *pathIndex) LastModified(path string) time.Time {
	index.lock.Lock()
	defer index.lock.Unlock()
	if position, ok := index.pathIndex[path]; ok {
		return index.ordered[position].age
	}
	return time.Time{}
}

func (index *pathIndex) Load() error {
	ordered := make([]pathAge, 0, 1024)

	var err error
	start := time.Now()
	defer func() {
		klog.Infof("Refreshed path index in %s, loaded %d: %v", time.Now().Sub(start).Truncate(time.Millisecond), len(ordered), err)
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

func (i *pathIndex) SearchPaths(index *Index, initial []string) ([]string, error) {
	var reJob *regexp.Regexp
	if index.Job != nil {
		re, err := regexp.Compile(fmt.Sprintf("%s.*%s.*%s", string(filepath.Separator), index.Job.String(), string(filepath.Separator)))
		if err != nil {
			return nil, fmt.Errorf("unable to build search path regexp: %v", err)
		}
		reJob = re
	}

	var paths []pathAge
	i.lock.Lock()
	paths = i.ordered
	i.lock.Unlock()

	// search all if we haven't built an index yet
	if len(paths) == 0 {
		return append(initial, i.base), nil
	}

	// grow the map to the desired size up front
	if len(paths) > len(initial) {
		copied := make([]string, len(initial), len(initial)+len(paths))
		copy(copied, initial)
		initial = copied
	}

	searchType := index.SearchType
	all := len(searchType) == 0 || searchType == "all"
	if searchType == "bug+junit" {
		searchType = "junit"
	}

	var oldest time.Time
	if index.MaxAge > 0 {
		oldest = time.Now().Add(-index.MaxAge)
	}

	var grown bool
	for _, path := range paths {
		if path.age.Before(oldest) {
			break
		}
		if all || path.index == searchType {
			if reJob != nil && !reJob.MatchString(path.path) {
				continue
			}
			initial = append(initial, filepath.Join(i.base, filepath.FromSlash(path.path)))
			grown = true
		}
	}

	if !grown {
		return nil, fmt.Errorf("no entries")
	}

	return initial, nil
}
