package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/golang/glog"
)

type Index struct {
	Search     string
	SearchType string
	Context    int
}

type CommandGenerator interface {
	Command(*Index) (string, []string)
	PathPrefix() string
}

type ripgrepGenerator struct {
	execPath   string
	searchPath string
}

func (g ripgrepGenerator) Command(index *Index) (string, []string) {
	args := []string{"--color", "never", "-S", "--sortr", "modified", "--null", "--no-line-number", "--no-heading"}
	if index.Context > 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	}
	switch index.SearchType {
	case "junit":
		args = append(args, "-g", "junit.failures")
	}
	args = append(args, index.Search, g.searchPath)
	return g.execPath, args
}

func (g ripgrepGenerator) PathPrefix() string {
	return g.searchPath
}

type grepGenerator struct {
	execPath   string
	searchPath string
}

func (g grepGenerator) Command(index *Index) (string, []string) {
	args := []string{"--color", "never", "-R", "--null"}
	if index.Context > 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	}
	switch index.SearchType {
	case "junit":
		args = append(args, "--include", "junit.failures")
	}
	args = append(args, index.Search, g.searchPath)
	return g.execPath, args
}

func (g grepGenerator) PathPrefix() string {
	return g.searchPath
}

func NewCommandGenerator(searchPath string) (CommandGenerator, error) {
	if path, err := exec.LookPath("rg"); err == nil {
		glog.Infof("Using ripgrep at %s for searches", path)
		return ripgrepGenerator{execPath: path, searchPath: searchPath}, nil
	}
	if path, err := exec.LookPath("grep"); err == nil {
		glog.Infof("Using grep at %s for searches", path)
		return grepGenerator{execPath: path, searchPath: searchPath}, nil
	}
	return nil, fmt.Errorf("could not find 'rg' or 'grep' on the path")
}

func executeGrep(ctx context.Context, gen CommandGenerator, index *Index, maxLines int, fn func(name string, lines []bytes.Buffer, moreLines int)) error {
	commandPath, commandArgs := gen.Command(index)
	pathPrefix := gen.PathPrefix()
	cmd := exec.Command(commandPath, commandArgs...)
	errOut := &bytes.Buffer{}
	cmd.Stderr = errOut
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	glog.V(2).Infof("Running: %s", strings.Join(cmd.Args, " "))
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
