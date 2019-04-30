package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

type PathAccessor interface {
	SearchPaths(*Index, []string) []string
	Stats() PathIndexStats
}

type PathIndexStats struct {
	Entries int
	Size    int64
}

type Result struct {
	FailedAt time.Time
}

type ResultMetadata interface {
	MetadataFor(path string) (Result, bool)
}

type pathIndex struct {
	base   string
	maxAge time.Duration

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

func (index *pathIndex) MetadataFor(path string) (Result, bool) {
	var age time.Time
	var ok bool
	index.lock.Lock()
	position, ok := index.pathIndex[path]
	if ok {
		age = index.ordered[position].age
	}
	index.lock.Unlock()
	if !ok {
		return Result{}, false
	}
	return Result{FailedAt: age}, true
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
		switch info.Name() {
		case "build-log.txt":
			stats.Entries++
			stats.Size += info.Size()
			ordered = append(ordered, pathAge{index: "build-log", path: path, age: info.ModTime()})
		case "junit.failures":
			stats.Entries++
			stats.Size += info.Size()
			ordered = append(ordered, pathAge{index: "junit", path: path, age: info.ModTime()})
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

	for _, path := range paths {
		if path.age.Before(oldest) {
			break
		}
		if all || path.index == index.SearchType {
			initial = append(initial, path.path)
		}
	}
	return initial
}
