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
	"strconv"
	"syscall"

	"k8s.io/klog"
)

type CommandGenerator interface {
	Command(index *Index, search string) (cmd string, args []string, err error)
	PathPrefix() string
}

type ripgrepGenerator struct {
	execPath     string
	searchPath   string
	dynamicPaths PathSearcher
}

func (g ripgrepGenerator) Command(index *Index, search string) (string, []string, error) {
	args := []string{g.execPath, "--color", "never", "-S", "--null", "--no-line-number", "--no-heading"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, search)
	var err error
	args, err = g.dynamicPaths.SearchPaths(index, args)
	if err != nil {
		return "", nil, err
	}
	return g.execPath, args, nil
}

func (g ripgrepGenerator) PathPrefix() string {
	return g.searchPath
}

type grepGenerator struct {
	execPath     string
	searchPath   string
	dynamicPaths PathSearcher
}

func (g grepGenerator) Command(index *Index, search string) (string, []string, error) {
	args := []string{g.execPath, "--color=never", "-R", "--null", "-a"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	args = append(args, search)
	var err error
	args, err = g.dynamicPaths.SearchPaths(index, args)
	if err != nil {
		return "", nil, err
	}
	return g.execPath, args, nil
}

func (g grepGenerator) PathPrefix() string {
	return g.searchPath
}

func NewCommandGenerator(searchPath string, paths PathSearcher) (CommandGenerator, error) {
	if path, err := exec.LookPath("rg"); err == nil {
		klog.Infof("Using ripgrep at %s for searches", path)
		return ripgrepGenerator{execPath: path, searchPath: searchPath, dynamicPaths: paths}, nil
	}
	if path, err := exec.LookPath("grep"); err == nil {
		klog.Infof("Using grep at %s for searches", path)
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
	commandPath, commandArgs, err := gen.Command(index, search)
	if err != nil {
		return err
	}
	if commandArgs == nil { // no matching SearchPaths
		return nil
	}
	klog.V(6).Infof("Executing query %s %v", commandPath, commandArgs)

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
	bytesRead := 0
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
		klog.V(7).Infof("Captured %d lines for %s, %d not shown", line, path, hidden)
		matches++
		fn(filepath.ToSlash(relPath), search, result, hidden)
		return nil
	}

	defer func() {
		n, err := io.Copy(ioutil.Discard, pr)
		if n > 0 || (err != nil && err != io.EOF) {
			klog.Errorf("Unread input %d: %v", n, err)
		}
		klog.V(2).Infof("Waiting for command to finish after reading %d lines and %d bytes", linesRead, bytesRead)
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && matches == 0 {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.ExitStatus() == 1 {
					return
				}
			}
			klog.Errorln(errOut.String())
			klog.Errorf("Failed to wait for command: %v", err)
		}
	}()

	chunk, isPrefix, err := br.ReadLine()
	if err != nil {
		return err
	}
	position := 0
	bytesRead += len(chunk)
	for {
		linesRead++
		isMatchLine := len(chunk) != 2 || chunk[0] != '-' || !bytes.Equal(chunk, []byte("--"))
		var nextFilename []byte

		if isMatchLine {
			// beginning of line, find the filename
			filenameEnd := bytes.IndexByte(chunk, 0x00)
			if filenameEnd < 1 {
				if len(chunk) > 140 {
					chunk = chunk[:140]
				}
				return fmt.Errorf("command returned an unexpected empty line at position %d, %d (bytes=%d): %q", position, linesRead, bytesRead, string(chunk))
			}
			// initialize the filename
			nextFilename = chunk[:filenameEnd+1]
			switch {
			case len(nextFilename) == 0:
				klog.Errorf("Found empty filename position %d", position)
			case nextFilename[0] != '/':
				klog.Errorf("Found filename without leading / at position %d: %s", position, string(nextFilename))
			}
			position += filenameEnd + 1
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

		// exhaust the rest of the current line
		for isPrefix {
			position += len(chunk)
			chunk, isPrefix, err = br.ReadLine()
			if err != nil {
				if err := send(); err != nil {
					return err
				}
				return err
			}
			bytesRead += len(chunk)
		}

		// read next line
		position += len(chunk)
		chunk, isPrefix, err = br.ReadLine()
		if err != nil {
			if err := send(); err != nil {
				return err
			}
			return err
		}
		bytesRead += len(chunk)
	}
}
