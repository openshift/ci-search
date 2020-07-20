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
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"
)

var ErrMaxBytes = fmt.Errorf("reached maximum search length, more results not shown")

type CommandGenerator interface {
	Command(index *Index, search string, jobNames sets.String) (cmd string, args []string, paths []string, err error)
	PathPrefix() string
}

type RipgrepSourceArguments interface {
	// SearchPaths searches for paths matching the index's SearchType
	// and MaxAge, and returns them as a slice of filesystem paths.
	RipgrepSourceArguments(*Index, sets.String) (args []string, paths []string, err error)
}

type ripgrepGenerator struct {
	execPath   string
	searchPath string
	arguments  RipgrepSourceArguments
}

func (g ripgrepGenerator) Command(index *Index, search string, jobNames sets.String) (string, []string, []string, error) {
	args := []string{g.execPath, "-a", "-z", "-u", "--color", "never", "-S", "--null", "--no-line-number", "--no-heading"}
	if index.Context >= 0 {
		args = append(args, "--context", strconv.Itoa(index.Context))
	} else {
		args = append(args, "--context", "0")
	}
	if index.MaxMatches > 0 {
		// always capture at least one more result than requested because rg terminates
		// its search at the last result and won't return context for that result
		if index.Context > 0 {
			args = append(args, "--max-count", strconv.Itoa(index.MaxMatches+1))
		} else {
			args = append(args, "--max-count", strconv.Itoa(index.MaxMatches))
		}
	}
	args = append(args, search)
	newArgs, paths, err := g.arguments.RipgrepSourceArguments(index, jobNames)
	if err != nil {
		return "", nil, nil, err
	}
	return g.execPath, append(args, newArgs...), paths, nil
}

func (g ripgrepGenerator) PathPrefix() string {
	return g.searchPath
}

func NewCommandGenerator(searchPath string, arguments RipgrepSourceArguments) (CommandGenerator, error) {
	if path, err := exec.LookPath("rg"); err == nil {
		klog.Infof("Using ripgrep at %s for searches", path)
		return ripgrepGenerator{execPath: path, searchPath: searchPath, arguments: arguments}, nil
	}
	return nil, fmt.Errorf("could not find 'rg' on the path")
}

type GrepFunc func(name string, search string, lines []bytes.Buffer, moreLines int) error

// executeGrep search for matches to index and, for each match found,
// calls fn with the following arguments:
//
// * name, the name of the matching file, as a slash-slash-separated path
//   resolved relative to the index base.
// * search, the string from Index.Search which resulted in the callback.
// * lines, the match with its surrounding context.
// * moreLines, the number of elided lines, when the match and context
//   is truncated due to excessive length.
func executeGrep(ctx context.Context, gen CommandGenerator, index *Index, jobNames sets.String, fn GrepFunc) error {
	for _, search := range index.Search {
		if err := executeGrepSingle(ctx, gen, index, search, jobNames, fn); err != nil {
			return err
		}
	}
	return nil
}

func estimateLength(arr []string) int {
	l := 0
	for _, s := range arr {
		l += len(s)
	}
	return l
}

func splitStringSliceByLength(arr []string, maxLength int) ([]string, []string) {
	for i, s := range arr {
		maxLength -= len(s) + 1
		if maxLength <= 0 {
			return arr[:i], arr[i:]
		}
	}
	return arr, nil
}

func executeGrepSingle(ctx context.Context, gen CommandGenerator, index *Index, search string, jobNames sets.String, fn GrepFunc) error {
	commandPath, commandArgs, commandPaths, err := gen.Command(index, search, jobNames)
	if err != nil {
		return err
	}

	// platforms limit the length of arguments - we have to execute in batches
	var maxArgs int
	switch runtime.GOOS {
	case "darwin":
		maxArgs = 200 * 1024
	default:
		maxArgs = 2*1024*1024 - 256*1024
	}
	for _, arg := range commandArgs {
		maxArgs -= len(arg) + 1
	}
	maxBytes := index.MaxBytes
	pathPrefix := gen.PathPrefix()

	for len(commandPaths) > 0 {
		var args []string
		args, commandPaths = splitStringSliceByLength(commandPaths, maxArgs)
		if len(args) == 0 {
			return fmt.Errorf("argument longer than maximum shell length")
		}

		cmd := &exec.Cmd{}
		cmd.Path = commandPath
		cmd.Args = append(commandArgs, args...)
		bytesRead, err := runSingleCommand(ctx, cmd, pathPrefix, index, maxBytes, search, fn)
		if err != nil && err != io.EOF {
			if strings.Contains(err.Error(), "argument list too long") {
				return fmt.Errorf("arguments too long: %d bytes", estimateLength(cmd.Args))
			}
			return err
		}

		maxBytes -= bytesRead
	}
	return nil
}

func runSingleCommand(ctx context.Context, cmd *exec.Cmd, pathPrefix string, index *Index, maxBytes int64, search string, fn GrepFunc) (int64, error) {
	errOut := &bytes.Buffer{}
	cmd.Stderr = errOut
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	maxLines := index.MaxMatches
	if index.Context > 0 {
		maxLines *= (index.Context*2 + 1)
	}

	br := bufio.NewReaderSize(pr, 512*1024)
	filename := bytes.NewBuffer(make([]byte, 1024))
	bytesRead := int64(0)
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
		return fn(filepath.ToSlash(relPath), search, result, hidden)
	}

	defer func() {
		n, err := io.Copy(ioutil.Discard, pr)
		if n > 0 || (err != nil && err != io.EOF) {
			klog.Errorf("Unread input %d: %v", n, err)
		}
		klog.V(6).Infof("Waiting for command to finish after reading %d lines and %d bytes", linesRead, bytesRead)
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && matches == 0 {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.ExitStatus() == 1 {
					return
				}
			}
			klog.Errorf("Failed to wait for command: %v", err)
		}
	}()

	chunk, isPrefix, err := br.ReadLine()
	if err != nil {
		return bytesRead, err
	}
	position := 0
	bytesRead += int64(len(chunk))
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
				return bytesRead, fmt.Errorf("command returned an unexpected empty line at position %d, %d (bytes=%d): %q", position, linesRead, bytesRead, string(chunk))
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
				return bytesRead, err
			}
			if bytesRead > maxBytes {
				return bytesRead, ErrMaxBytes
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
					return bytesRead, err
				}
				return bytesRead, err
			}
			bytesRead += int64(len(chunk))
		}

		// read next line
		position += len(chunk)
		chunk, isPrefix, err = br.ReadLine()
		if err != nil {
			if err := send(); err != nil {
				return bytesRead, err
			}
			return bytesRead, err
		}
		bytesRead += int64(len(chunk))
	}
}
