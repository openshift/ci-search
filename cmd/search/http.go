package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"k8s.io/klog"
)

type nopFlusher struct{}

func (_ nopFlusher) Flush() {}

type Match struct {
	FileType  string   `json:"filename"`
	Context   []string `json:"context,omitempty"`
	MoreLines int      `json:"moreLines,omitempty"`
}

func (o *options) handleConfig(w http.ResponseWriter, req *http.Request) {
	o.ConfigPath = "README.md"
	if o.ConfigPath == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	data, err := ioutil.ReadFile(o.ConfigPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to read config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer := encodedWriter(w, req)
	defer writer.Close()
	if _, err = writer.Write(data); err != nil {
		klog.Errorf("Failed to write response: %v", err)
	}
}

func (o *options) handleIndex(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = nopFlusher{}
	}

	index, err := parseRequest(req, "text", o.MaxAge)
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if len(index.Search) == 0 {
		index.Search = []string{""}
	}

	contextOptions := []string{
		fmt.Sprintf(`<option value="-1" %s>Links</option>`, intSelected(1, index.Context)),
		fmt.Sprintf(`<option value="0" %s>No context</option>`, intSelected(0, index.Context)),
		fmt.Sprintf(`<option value="1" %s>1 lines</option>`, intSelected(1, index.Context)),
		fmt.Sprintf(`<option value="2" %s>2 lines</option>`, intSelected(2, index.Context)),
		fmt.Sprintf(`<option value="3" %s>3 lines</option>`, intSelected(3, index.Context)),
		fmt.Sprintf(`<option value="5" %s>5 lines</option>`, intSelected(5, index.Context)),
		fmt.Sprintf(`<option value="7" %s>7 lines</option>`, intSelected(7, index.Context)),
		fmt.Sprintf(`<option value="10" %s>10 lines</option>`, intSelected(10, index.Context)),
		fmt.Sprintf(`<option value="15" %s>15 lines</option>`, intSelected(15, index.Context)),
	}
	switch index.Context {
	case -1, 0, 1, 2, 3, 5, 7, 10, 15:
	default:
		context := template.HTMLEscapeString(strconv.Itoa(index.Context))
		contextOptions = append(contextOptions, fmt.Sprintf(`<option value="%s" selected>%s</option>`, context, context))
	}

	var searchTypeOptions []string
	for _, searchType := range []string{"bug+junit", "bug", "junit", "build-log", "all"} {
		var selected string
		if searchType == index.SearchType {
			selected = "selected"
		}
		searchTypeOptions = append(searchTypeOptions, fmt.Sprintf(`<option value="%s" %s>%s</option>`, template.HTMLEscapeString(searchType), selected, template.HTMLEscapeString(searchType)))
	}

	maxAgeOptions := []string{
		fmt.Sprintf(`<option value="%dh" %s>6h</option>`, 6, durationSelected(6*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="%dh" %s>12h</option>`, 12, durationSelected(12*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="%dh" %s>1d</option>`, 24, durationSelected(24*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="%dh" %s>2d</option>`, 2*24, durationSelected(2*24*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="%dh" %s>7d</option>`, 7*24, durationSelected(7*24*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="%dh" %s>14d</option>`, 14*24, durationSelected(14*24*time.Hour, index.MaxAge)),
	}
	switch index.MaxAge {
	case 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 2 * 24 * time.Hour, 7 * 24 * time.Hour, 14 * 24 * time.Hour:
	case 0:
		maxAgeOptions = append(maxAgeOptions, `<option value="0" selected>No limit</option>`)
	default:
		maxAge := template.HTMLEscapeString(index.MaxAge.String())
		maxAgeOptions = append(maxAgeOptions, fmt.Sprintf(`<option value="%s" selected>%s</option>`, maxAge, maxAge))
	}

	writer := encodedWriter(w, req)
	defer writer.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	fmt.Fprintf(writer, htmlPageStart, "Search OpenShift CI")
	fmt.Fprintf(writer, htmlIndexForm, template.HTMLEscapeString(index.Search[0]), strings.Join(maxAgeOptions, ""), strings.Join(contextOptions, ""), strings.Join(searchTypeOptions, ""))

	// display the empty results page
	if len(index.Search[0]) == 0 {
		stats := o.Stats()
		fmt.Fprintf(writer, htmlEmptyPage, units.HumanSize(float64(stats.Size)), stats.Entries, stats.Bugs)
		fmt.Fprintf(writer, htmlPageEnd)
		return
	}

	// perform a search
	flusher.Flush()
	fmt.Fprintf(writer, `<div style="margin-top: 3rem; position: relative" class="pl-3">`)

	start := time.Now()

	var count int
	if index.Context >= 0 {
		count, err = renderWithContext(req.Context(), writer, index, o.generator, start, o)
	} else {
		count, err = renderSummary(req.Context(), writer, index, o.generator, start, o)
	}

	duration := time.Now().Sub(start)
	if err != nil {
		klog.Errorf("Search %q failed with %d results in %s: command failed: %v", index.Search[0], count, duration, err)
		fmt.Fprintf(writer, `<p class="alert alert-danger">error: %s</p>`, template.HTMLEscapeString(err.Error()))
		fmt.Fprintf(writer, htmlPageEnd)
		return
	}
	klog.V(2).Infof("Search %q completed with %d results in %s", index.Search[0], count, duration)

	stats := o.Stats()
	fmt.Fprintf(writer, `<p style="position:absolute; top: -2rem;" class="small"><em>Found %d results in %s (%s in %d files and %d bugs)</em> - <a href="/">home</a> | <a href="/chart?%s">chart view</a> - source code located <a target="_blank" href="https://github.com/openshift/ci-search">on github</a></p>`, count, duration.Truncate(time.Millisecond), units.HumanSize(float64(stats.Size)), stats.Entries, stats.Bugs, template.HTMLEscapeString(req.URL.RawQuery))
	if count == 0 {
		fmt.Fprintf(writer, `<p style="padding-top: 1em;"><em>No results found.</em></p><p><em>Search uses <a target="_blank" href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a> to find results. Try simplifying your search or using case-insensitive options.</em></p>`)
	}
	fmt.Fprintf(writer, "</div>")

	fmt.Fprintf(writer, htmlPageEnd)
}

func intSelected(current, expected int) string {
	if current == expected {
		return "selected"
	}
	return ""
}

func durationSelected(current, expected time.Duration) string {
	if current == expected {
		return "selected"
	}
	return ""
}

type sortableEntry struct {
	index int64
	data  []byte
}

type sortableEntries []sortableEntry

func (e sortableEntries) Less(i, j int) bool { return e[i].index <= e[j].index }
func (e sortableEntries) Swap(i, j int)      { e[i], e[j] = e[j], e[i] }
func (e sortableEntries) Len() int           { return len(e) }

type sortableWriter struct {
	bw *bufio.Writer

	sizeLimit int
	entries   sortableEntries

	buf   *bytes.Buffer
	index int64
}

func (w *sortableWriter) push() {
	data := w.buf.Bytes()
	copied := make([]byte, len(data))
	copy(copied, data)
	w.sizeLimit -= len(copied)
	w.entries = append(w.entries, sortableEntry{index: w.index, data: copied})
	w.buf.Reset()
}

func (w *sortableWriter) SetIndex(index int64) {
	if w.sizeLimit <= 0 {
		return
	}
	if w.buf == nil {
		w.index = index
		w.buf = bytes.NewBuffer(make([]byte, 0, 2048))
		return
	}

	// copy the current buffer to the array
	w.push()
	w.index = index

	if w.sizeLimit <= 0 {
		klog.Infof("DEBUG: results larger than window, flushing from now on")
		w.buf = nil
		w.Flush()
	}
}

func (w *sortableWriter) Flush() error {
	if w.buf != nil {
		w.push()
		w.buf = nil
	}
	if len(w.entries) > 0 {
		sort.Sort(w.entries)
		for _, entry := range w.entries {
			if _, err := w.bw.Write(entry.data); err != nil {
				return err
			}
		}
		w.entries = w.entries[:]
	}
	return w.bw.Flush()
}

func (w *sortableWriter) Write(buf []byte) (int, error) {
	if w.buf != nil {
		return w.buf.Write(buf)
	}
	return w.bw.Write(buf)
}

func renderWithContext(ctx context.Context, w io.Writer, index *Index, generator CommandGenerator, start time.Time, resolver PathResolver) (int, error) {
	count := 0
	lineCount := 0

	bw := &sortableWriter{sizeLimit: 2 * 1024 * 1024, bw: bufio.NewWriterSize(w, 256*1024)}
	var lastName string
	drop := true
	err := executeGrep(ctx, generator, index, 30, func(name string, search string, matches []bytes.Buffer, moreLines int) {
		if lastName == name {
			if drop {
				return
			}
			fmt.Fprintf(bw, "\n&mdash;\n\n")
		} else {
			// finish the last result
			lastName = name
			if !drop {
				fmt.Fprintf(bw, `</pre></div>`)
			}
			drop = false

			// decide whether to print the next result
			result, err := resolver.MetadataFor(name)
			if err != nil {
				klog.Errorf("unable to resolve metadata for: %s: %v", name, err)
				drop = true
				return
			}
			var age string
			if !result.LastModified.IsZero() {
				duration := start.Sub(result.LastModified)
				if !result.IgnoreAge && duration > index.MaxAge {
					klog.V(5).Infof("Filtered %s, older than query limit", name)
					drop = true
					return
				}
				age = " " + units.HumanDuration(duration)
			}

			// output the result
			count++
			bw.SetIndex(-result.LastModified.Unix())
			fmt.Fprintf(bw, `<div class="mb-4">`)
			switch result.FileType {
			case "bug":
				fmt.Fprintf(bw, `<h5 class="mb-3"><a target="_blank" href="%s">%s</a>%s</h5><pre class="small">`, template.HTMLEscapeString(result.URI.String()), result.Name, template.HTMLEscapeString(age))
			default:
				fmt.Fprintf(bw, `<h5 class="mb-3">%s from %s <a target="_blank" href="%s">%s #%d</a>%s</h5><pre class="small">`, template.HTMLEscapeString(result.FileType), template.HTMLEscapeString(result.Trigger), template.HTMLEscapeString(result.URI.String()), template.HTMLEscapeString(result.Name), result.Number, template.HTMLEscapeString(age))
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
	if !drop {
		fmt.Fprintf(bw, `</pre></div>`)
	}
	if err := bw.Flush(); err != nil {
		klog.Errorf("Unable to flush results buffer: %v", err)
	}
	return count, err
}

func renderSummary(ctx context.Context, w io.Writer, index *Index, generator CommandGenerator, start time.Time, resolver PathResolver) (int, error) {
	count := 0
	currentLines := 0

	bw := &sortableWriter{sizeLimit: 2 * 1024 * 1024, bw: bufio.NewWriterSize(w, 256*1024)}
	var lastName string
	drop := true
	err := executeGrep(ctx, generator, index, 30, func(name string, search string, matches []bytes.Buffer, moreLines int) {
		if lastName == name {
			// continue accumulating matches
		} else {
			// finish the last result
			lastName = name
			if !drop {
				fmt.Fprintf(bw, "<td>%d</td></tr>\n", currentLines)
			}
			drop = false
			currentLines = 0

			// decide whether to print the next result
			result, err := resolver.MetadataFor(name)
			if err != nil {
				klog.Errorf("unable to resolve metadata for: %s: %v", name, err)
				drop = true
				return
			}
			if result.URI == nil {
				klog.Errorf("no job URI for %q", name)
				drop = true
				return
			}
			var age string
			if !result.LastModified.IsZero() {
				duration := start.Sub(result.LastModified)
				if !result.IgnoreAge && duration > index.MaxAge {
					klog.V(5).Infof("Filtered %s, older than query limit", name)
					drop = true
					return
				}
				age = units.HumanDuration(duration) + " ago"
			}

			count++
			if count == 1 {
				fmt.Fprintf(bw, `<table class="table table-reponsive"><tbody><tr><th>Type</th><th>Job</th><th>Age</th><th># of hits</th></tr>`+"\n")
			}
			bw.SetIndex(-result.LastModified.Unix())
			switch result.FileType {
			case "bug":
				fmt.Fprintf(bw, `<tr><td>%s</td><td><a target="_blank" href="%s">%s</a></td><td>%s</td>`, template.HTMLEscapeString(result.FileType), template.HTMLEscapeString(result.URI.String()), template.HTMLEscapeString(result.Name), template.HTMLEscapeString(age))
			default:
				fmt.Fprintf(bw, `<tr><td>%s</td><td><a target="_blank" href="%s">%s #%d</a></td><td>%s</td>`, template.HTMLEscapeString(result.FileType), template.HTMLEscapeString(result.URI.String()), template.HTMLEscapeString(result.Name), result.Number, template.HTMLEscapeString(age))
			}
		}

		currentLines++
	})

	if !drop {
		fmt.Fprintf(bw, "<td>%d</td></tr>\n", currentLines)
	}
	if err := bw.Flush(); err != nil {
		klog.Errorf("Unable to flush results buffer: %v", err)
	}
	fmt.Fprintf(w, "</table>\n")
	return count, err
}

const htmlPageStart = `
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8"><title>%s</title>
<link rel="stylesheet" href="https://stackpath.bootstrapcdn.com/bootstrap/4.1.3/css/bootstrap.min.css" integrity="sha384-MCw98/SFnGE8fJT3GXwEOngsV7Zt27NXFoaoApmYm81iuXoPkFOJwJ8ERdknLPMO" crossorigin="anonymous">
<meta name="viewport" content="width=device-width, initial-scale=1, shrink-to-fit=no">
<style>
</style>
</head>
<body>
<div class="container-fluid">
`

const htmlPageEnd = `
</div>
</body>
</html>
`

const htmlIndexForm = `
<form class="form mt-4 mb-4" method="GET">
	<div class="input-group input-group-lg"><input autocomplete="off" autofocus name="search" class="form-control col-auto" value="%s" placeholder="Search OpenShift CI failures by entering a regex search ...">
	<select name="maxAge" class="form-control col-1" onchange="this.form.submit();">%s</select>
	<select name="context" class="form-control col-1" onchange="this.form.submit();">%s</select>
	<select name="type" class="form-control col-1" onchange="this.form.submit();">%s</select>
	<input class="btn" type="submit" value="Search">
	</div>
</form>
`

const htmlEmptyPage = `
<div class="ml-3" style="margin-top: 3rem; color: #666;">
<p>Find bugs and test failures from <a href="/config">CI jobs</a> in <a target="_blank" href="https://deck-ci.svc.ci.openshift.org">OpenShift CI</a>.</p>
<p>The search input will use <a target="_blank" href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a>.</p>
<p>Searches are case-insensitive (using ripgrep "smart casing")</p>
<p>Examples:
<ul>
<li><code>timeout</code> - all JUnit failures with 'timeout' in the result</li>
<li><code>status code \d{3}\s</code> - all failures that contain 'status code' followed by a 3 digit number</li>
<li><code>(?m)text on one line .* and text on another line</code> - search for text across multiple lines</li>
</ul>
<p>You can alter the age of results to search with the dropdown next to the search bar. Note that older results are pruned and may not be available after 14 days.</p>
<p>The amount of surrounding text returned with each match can be changed, including none.
<p>Currently indexing %s across %d results and %d bugs</p>
</div>
`
