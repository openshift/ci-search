package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/golang/glog"
)

func renderWithContext(ctx context.Context, w http.ResponseWriter, index *Index, generator CommandGenerator, start time.Time) (int, error) {
	count := 0
	lineCount := 0
	var lastName string

	bw := bufio.NewWriterSize(w, 256*1024)
	err := executeGrep(ctx, generator, index, 30, func(name string, matches []bytes.Buffer, moreLines int) {
		if count == 5 || count%50 == 0 {
			bw.Flush()
		}
		if lastName == name {
			fmt.Fprintf(bw, "\n&mdash;\n\n")
		} else {
			lastName = name
			if count > 0 {
				fmt.Fprintf(bw, `</pre></div>`)
			}
			count++

			fmt.Fprintf(bw, `<div class="mb-4">`)
			parts := bytes.SplitN([]byte(name), []byte{filepath.Separator}, 8)
			last := len(parts) - 1
			switch {
			case last > 2 && bytes.Equal(parts[last], []byte("junit.failures")):
				prefix := string(bytes.Join(parts[:last], []byte("/")))
				if last > 3 && bytes.Equal(parts[2], []byte("pull")) {
					name = fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<h5 class="mb-3">junit from PR %s <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(string(parts[3])), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				} else {
					name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<h5 class="mb-3">junit from build <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				}
			default:
				fmt.Fprintf(bw, `<h5 class="mb-3">%s</h5><pre class="small">`, template.HTMLEscapeString(name))
			}
		}

		// remove empty leading and trailing lines
		var lines [][]byte
		for _, m := range matches {
			line := bytes.TrimRight(m.Bytes(), " ")
			if len(line) == 0 {
				continue
			}
			lines = append(lines, line)
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if len(lines[i]) != 0 {
				break
			}
			lines = lines[:i]
		}
		lineCount += len(lines)

		for _, line := range lines {
			template.HTMLEscape(bw, line)
			fmt.Fprintln(bw)
		}
		if moreLines > 0 {
			fmt.Fprintf(bw, "\n... %d lines not shown\n\n", moreLines)
		}
	})
	if count > 0 {
		fmt.Fprintf(bw, `</pre></div>`)
	}
	if err := bw.Flush(); err != nil {
		glog.Errorf("Unable to flush results buffer: %v", err)
	}
	return count, err
}

func renderSummary(ctx context.Context, w http.ResponseWriter, index *Index, generator CommandGenerator, start time.Time) (int, error) {
	count := 0
	currentLines := 0
	var lastName string
	bw := bufio.NewWriterSize(w, 256*1024)
	err := executeGrep(ctx, generator, index, 30, func(name string, matches []bytes.Buffer, moreLines int) {
		if count == 5 || count%50 == 0 {
			bw.Flush()
		}
		if lastName == name {
			// continue accumulating matches
		} else {
			lastName = name

			if count > 0 {
				fmt.Fprintf(bw, " - <span>%d</span>", currentLines)
				fmt.Fprintf(bw, `</div>`)
				currentLines = 0
			}
			count++

			fmt.Fprintf(bw, `<div class="mb-2">`)
			parts := bytes.SplitN([]byte(name), []byte{filepath.Separator}, 8)
			last := len(parts) - 1
			switch {
			case last > 2 && bytes.Equal(parts[last], []byte("junit.failures")):
				prefix := string(bytes.Join(parts[:last], []byte("/")))
				if last > 3 && bytes.Equal(parts[2], []byte("pull")) {
					name = fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<span class="mb-3">junit from PR %s <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></span>`, template.HTMLEscapeString(string(parts[3])), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				} else {
					name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<span class="mb-3">junit from build <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></span>`, template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				}
			default:
				fmt.Fprintf(bw, `<span class="mb-3">%s</span>`, template.HTMLEscapeString(name))
			}
		}

		currentLines++
	})

	if count > 0 {
		fmt.Fprintf(bw, " - <span>%d</span>", currentLines)
		fmt.Fprintf(bw, `</div>`)
	}
	if err := bw.Flush(); err != nil {
		glog.Errorf("Unable to flush results buffer: %v", err)
	}
	return count, err
}
