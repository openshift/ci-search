package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
)

type Index struct {
	Search     string
	SearchType string
	Context    int

	MaxAge time.Duration
}

type CommandGenerator interface {
	Command(*Index) (string, []string)
	PathPrefix() string
}

type PathAccessor interface {
	SearchPaths(*Index, []string) []string
	Stats() PathIndexStats
}

type ripgrepGenerator struct {
	execPath     string
	searchPath   string
	dynamicPaths PathAccessor
}

func (g ripgrepGenerator) Command(index *Index) (string, []string) {
	args := []string{g.execPath, "--color", "never", "-S", "--null", "--no-line-number", "--no-heading"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, index.Search)
	args = g.dynamicPaths.SearchPaths(index, args)
	return g.execPath, args
}

func (g ripgrepGenerator) PathPrefix() string {
	return g.searchPath
}

type grepGenerator struct {
	execPath     string
	searchPath   string
	dynamicPaths PathAccessor
}

func (g grepGenerator) Command(index *Index) (string, []string) {
	args := []string{g.execPath, "--color", "never", "-R", "--null"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, index.Search)
	args = g.dynamicPaths.SearchPaths(index, args)
	return g.execPath, args
}

func (g grepGenerator) PathPrefix() string {
	return g.searchPath
}

func NewCommandGenerator(searchPath string, paths PathAccessor) (CommandGenerator, error) {
	if path, err := exec.LookPath("rg"); err == nil {
		glog.Infof("Using ripgrep at %s for searches", path)
		return ripgrepGenerator{execPath: path, searchPath: searchPath, dynamicPaths: paths}, nil
	}
	if path, err := exec.LookPath("grep"); err == nil {
		glog.Infof("Using grep at %s for searches", path)
		return grepGenerator{execPath: path, searchPath: searchPath, dynamicPaths: paths}, nil
	}
	return nil, fmt.Errorf("could not find 'rg' or 'grep' on the path")
}

func executeGrep(ctx context.Context, gen CommandGenerator, index *Index, maxLines int, fn func(name string, lines []bytes.Buffer, moreLines int)) error {
	commandPath, commandArgs := gen.Command(index)
	pathPrefix := gen.PathPrefix()
	cmd := &exec.Cmd{}
	cmd.Path = commandPath
	cmd.Args = commandArgs
	errOut := &bytes.Buffer{}
	cmd.Stderr = errOut
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	br := bufio.NewReaderSize(pr, 512*1024)
	filename := bytes.NewBuffer(make([]byte, 1024))
	linesRead := 0
	matches := 0
	match := make([]bytes.Buffer, maxLines)
	line := 0

	// send dispatches the result to the caller synchronously without allocating
	send := func() error {
		result := match
		if line < len(result) {
			result = result[:line]
		}
		if len(result) == 0 {
			return nil
		}

		name := filename.Bytes()
		name = name[:len(name)-1]
		if !bytes.HasPrefix(name, []byte(pathPrefix)) {
			return fmt.Errorf("grep returned filename %q which doesn't start with %q", string(name), pathPrefix)
		}
		name = name[len(pathPrefix):]

		hidden := (line) - len(result)
		//glog.V(2).Infof("Captured %d lines for %s, %d not shown", line, string(name), hidden)
		matches++
		fn(string(name), result, hidden)
		return nil
	}

	defer func() {
		n, err := io.Copy(ioutil.Discard, pr)
		if n > 0 || (err != nil && err != io.EOF) {
			glog.Errorf("Unread input %d: %v", n, err)
		}
		glog.Infof("Read %d lines", linesRead)
		glog.V(2).Infof("Waiting for command to finish")
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && matches == 0 {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.ExitStatus() == 1 {
					return
				}
			}
			glog.Errorln(errOut.String())
			glog.Errorf("Failed to wait for command: %v", err)
		}
		glog.V(2).Infof("Completed")
	}()

	chunk, isPrefix, err := br.ReadLine()
	if err != nil {
		return err
	}
	for {
		linesRead++
		isMatchLine := len(chunk) != 2 || chunk[0] != '-' || !bytes.Equal(chunk, []byte("--"))
		var nextFilename []byte

		if isMatchLine {
			// beginning of line, find the filename
			filenameEnd := bytes.IndexByte(chunk, 0x00)
			if filenameEnd < 1 {
				glog.V(2).Infof("No filename on line, continuing")
				return fmt.Errorf("grep returned an unexpected empty line")
			}
			// initialize the filename
			nextFilename = chunk[:filenameEnd+1]
			chunk = chunk[filenameEnd+1:]
			if filename.Len() == 0 {
				filename.Write(nextFilename)
			}
		}

		switch {
		case !isMatchLine || !bytes.Equal(nextFilename, filename.Bytes()):
			// filename from current line doesn't match previous filename, so we flush the match to the caller
			if err := send(); err != nil {
				return err
			}

			line = 0
			if isMatchLine {
				filename.Reset()
				filename.Write(nextFilename)

				match[0].Reset()
				match[0].Write(chunk)
				line = 1
			}

		case line >= maxLines:
			// if we're past the max lines for a filename, skip this result
			line++

		default:
			// add line to the current match
			match[line].Reset()
			match[line].Write(chunk)
			line++
		}

		// skip the rest of the line
		if isPrefix {
			for {
				chunk, isPrefix, err = br.ReadLine()
				if err != nil {
					if err := send(); err != nil {
						glog.Errorf("Unable to send last search result: %v", err)
					}
					return err
				}
				if isPrefix {
					break
				}
			}
		} else {
			chunk, isPrefix, err = br.ReadLine()
			if err != nil {
				if err := send(); err != nil {
					glog.Errorf("Unable to send last search result: %v", err)
				}
				return err
			}
		}
	}
}

type pathIndex struct {
	base   string
	maxAge time.Duration

	lock    sync.Mutex
	ordered []pathAge
	stats   PathIndexStats
}

type PathIndexStats struct {
	Entries int
	Size    int64
}

type pathAge struct {
	path  string
	index string
	age   time.Time
}

func (i *pathIndex) Load() error {
	ordered := make([]pathAge, 0, 1024)

	var err error
	start := time.Now()
	defer func() {
		glog.Infof("Refreshed path index in %s, loaded %d: %v", time.Now().Sub(start).Truncate(time.Millisecond), len(ordered), err)
	}()

	mustExpire := i.maxAge != 0
	expiredAt := start.Add(-i.maxAge)

	stats := PathIndexStats{}

	err = filepath.Walk(i.base, func(path string, info os.FileInfo, err error) error {
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

	i.lock.Lock()
	defer i.lock.Unlock()
	i.ordered = ordered
	i.stats = stats

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
		return []string{i.base}
	}

	// grow the map to the desired size up front
	if len(paths) > len(initial) {
		copied := make([]string, len(initial), len(initial)+len(paths))
		copy(copied, initial)
		initial = copied
	}

	all := len(index.SearchType) == 0 || index.SearchType == "all"

	if index.MaxAge > 0 {
		oldest := time.Now().Add(-index.MaxAge)
		for _, path := range paths {
			if path.age.Before(oldest) {
				break
			}
			if all || path.index == index.SearchType {
				initial = append(initial, path.path)
			}
		}
	} else {
		for _, path := range paths {
			if all || path.index == index.SearchType {
				initial = append(initial, path.path)
			}
		}
	}
	return initial
}
