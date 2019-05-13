package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/golang/glog"
)

type Index struct {
	Search []string

	// Filtering the body of material being searched.

	// Search excludes jobs whose Result.FileType does not match.
	SearchType string

	// Job excludes jobs whose Result.Name does not match.
	Job *regexp.Regexp

	// MaxAge excludes jobs which failed longer than MaxAge ago.
	MaxAge time.Duration

	// Output configuration.

	// Context includes this many lines of context around each match.
	Context int
}

type CommandGenerator interface {
	Command(index *Index, search string) (cmd string, args []string)
	PathPrefix() string
}

type ripgrepGenerator struct {
	execPath     string
	searchPath   string
	dynamicPaths PathAccessor
}

func (g ripgrepGenerator) Command(index *Index, search string) (string, []string) {
	args := []string{g.execPath, "--color", "never", "-S", "--null", "--no-line-number", "--no-heading"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, search)
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

func (g grepGenerator) Command(index *Index, search string) (string, []string) {
	args := []string{g.execPath, "--color=never", "-R", "--null"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, search)
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

// executeGrep search for matches to index and, for each match found,
// calls fn with the following arguments:
//
// * name, the name of the matching file, as a slash-slash-separated path
//   resolved relative to the index base.
// * search, the string from Index.Search which resulted in the callback.
// * lines, the match with its surrounding context.
// * moreLines, the number of elided lines, when the match and context
//   is truncated due to excessive length.
func executeGrep(ctx context.Context, gen CommandGenerator, index *Index, maxLines int, fn func(name string, search string, lines []bytes.Buffer, moreLines int)) error {
	// FIXME: parallelize this
	for _, search := range index.Search {
		err := executeGrepSingle(ctx, gen, index, search, maxLines, fn)
		if err != nil && err != io.EOF {
			return err
		}
	}

	return nil
}

func executeGrepSingle(ctx context.Context, gen CommandGenerator, index *Index, search string, maxLines int, fn func(name string, search string, lines []bytes.Buffer, moreLines int)) error {
	commandPath, commandArgs := gen.Command(index, search)
	if commandArgs == nil { // no matching SearchPaths
		return nil
	}
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

		path := filename.String()
		path = path[:len(path)-1]
		relPath, err := filepath.Rel(pathPrefix, path)
		if err != nil {
			return err
		}

		hidden := (line) - len(result)
		//glog.V(2).Infof("Captured %d lines for %s, %d not shown", line, path, hidden)
		matches++
		fn(filepath.ToSlash(relPath), search, result, hidden)
		return nil
	}

	defer func() {
		n, err := io.Copy(ioutil.Discard, pr)
		if n > 0 || (err != nil && err != io.EOF) {
			glog.Errorf("Unread input %d: %v", n, err)
		}
		glog.V(2).Infof("Waiting for command to finish after reading %d lines", linesRead)
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && matches == 0 {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.ExitStatus() == 1 {
					return
				}
			}
			glog.Errorln(errOut.String())
			glog.Errorf("Failed to wait for command: %v", err)
		}
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
