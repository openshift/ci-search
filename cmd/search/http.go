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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"github.com/golang/glog"
)

type nopFlusher struct{}

func (_ nopFlusher) Flush() {}

func (o *options) handleConfig(w http.ResponseWriter, req *http.Request) {
	data, err := ioutil.ReadFile(o.ConfigPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to read config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
}

func (o *options) handleIndex(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = nopFlusher{}
	}

	index, err := o.parseRequest(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
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
	for _, searchType := range []string{"junit", "build-log", "all"} {
		var selected string
		if searchType == index.SearchType {
			selected = "selected"
		}
		searchTypeOptions = append(searchTypeOptions, fmt.Sprintf(`<option value="%s" %s>%s</option>`, template.HTMLEscapeString(searchType), selected, template.HTMLEscapeString(searchType)))
	}

	maxAgeOptions := []string{
		fmt.Sprintf(`<option value="6h" %s>6h</option>`, durationSelected(6*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="12h" %s>12h</option>`, durationSelected(12*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="24h" %s>1d</option>`, durationSelected(24*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="48h" %s>2d</option>`, durationSelected(48*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="168h" %s>7d</option>`, durationSelected(168*time.Hour, index.MaxAge)),
		fmt.Sprintf(`<option value="336h" %s>14d</option>`, durationSelected(336*time.Hour, index.MaxAge)),
	}
	switch index.MaxAge {
	case 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour, 168 * time.Hour, 336 * time.Hour:
	case 0:
		maxAgeOptions = append(maxAgeOptions, `<option value="0" selected>No limit</option>`)
	default:
		maxAge := template.HTMLEscapeString(index.MaxAge.String())
		maxAgeOptions = append(maxAgeOptions, fmt.Sprintf(`<option value="%s" selected>%s</option>`, maxAge, maxAge))
	}

	fmt.Fprintf(w, htmlPageStart, "Search OpenShift CI")
	fmt.Fprintf(w, htmlIndexForm, template.HTMLEscapeString(index.Search), strings.Join(maxAgeOptions, ""), strings.Join(contextOptions, ""), strings.Join(searchTypeOptions, ""))

	// display the empty results page
	if len(index.Search) == 0 {
		stats := o.accessor.Stats()
		fmt.Fprintf(w, htmlEmptyPage, units.HumanSize(float64(stats.Size)), stats.Entries)
		fmt.Fprintf(w, htmlPageEnd)
		return
	}

	// perform a search
	flusher.Flush()
	fmt.Fprintf(w, `<div style="margin-top: 3rem; position: relative" class="pl-3">`)

	start := time.Now()

	var count int
	if index.Context >= 0 {
		count, err = renderWithContext(req.Context(), w, index, o.generator, start, o.metadata)
	} else {
		count, err = renderSummary(req.Context(), w, index, o.generator, start, o.metadata)
	}

	duration := time.Now().Sub(start)
	if err != nil && err != io.EOF {
		glog.Errorf("Search %q failed with %d results in %s: command failed: %v", index.Search, count, duration, err)
		fmt.Fprintf(w, `<p class="alert alert-danger>%s</p>"`, template.HTMLEscapeString(err.Error()))
		fmt.Fprintf(w, htmlPageEnd)
		return
	}
	glog.V(2).Infof("Search %q completed with %d results in %s", index.Search, count, duration)

	stats := o.accessor.Stats()
	fmt.Fprintf(w, `<p style="position:absolute; top: -2rem;" class="small"><em>Found %d results in %s (%s in %d entries)</em></p>`, count, duration.Truncate(time.Millisecond), units.HumanSize(float64(stats.Size)), stats.Entries)
	fmt.Fprintf(w, "</div>")

	fmt.Fprintf(w, htmlPageEnd)
}

func (o *options) parseRequest(req *http.Request) (*Index, error) {
	if err := req.ParseForm(); err != nil {
		return nil, err
	}

	index := &Index{
		Context: 2,
		MaxAge:  7 * 24 * time.Hour,
	}

	search := req.FormValue("search")
	if len(search) > 0 {
		index.Search = search
	}

	if context := req.FormValue("context"); len(context) > 0 {
		num, err := strconv.Atoi(context)
		if err != nil || num < -1 || num > 15 {
			return nil, fmt.Errorf("context must be a number between -1 and 15")
		}
		index.Context = num
	}

	switch req.FormValue("type") {
	case "junit":
		index.SearchType = "junit"
	case "build-log":
		index.SearchType = "build-log"
	case "all", "":
		index.SearchType = "all"
	default:
		return nil, fmt.Errorf("search must be 'junit', 'build-log', or 'all'")
	}

	if value := req.FormValue("maxAge"); len(value) > 0 {
		maxAge, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("maxAge is an invalid duration: %v", err)
		} else if maxAge < 0 {
			return nil, fmt.Errorf("maxAge must be non-negative: %v", err)
		}
		index.MaxAge = maxAge
	}
	if o.MaxAge > 0 && o.MaxAge < index.MaxAge {
		index.MaxAge = o.MaxAge
	}

	return index, nil
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

func renderWithContext(ctx context.Context, w http.ResponseWriter, index *Index, generator CommandGenerator, start time.Time, resultMeta ResultMetadata) (int, error) {
	count := 0
	lineCount := 0
	var lastName string

	bw := bufio.NewWriterSize(w, 256*1024)
	err := executeGrep(ctx, generator, index, 30, func(name string, matches []bytes.Buffer, moreLines int) {
		if count == 5 || count%50 == 0 {
			bw.Flush()
		}
		name = strings.Trim(name, "/")
		if lastName == name {
			fmt.Fprintf(bw, "\n&mdash;\n\n")
		} else {
			lastName = name
			if count > 0 {
				fmt.Fprintf(bw, `</pre></div>`)
			}
			count++

			var age string
			result, ok := resultMeta.MetadataFor(name)
			if ok {
				duration := start.Sub(result.FailedAt)
				age = " " + units.HumanDuration(duration)
			}

			fmt.Fprintf(bw, `<div class="mb-4">`)
			parts := bytes.SplitN([]byte(name), []byte{filepath.Separator}, 8)
			last := len(parts) - 1
			switch {
			case last > 2 && (bytes.Equal(parts[last], []byte("junit.failures")) || bytes.Equal(parts[last], []byte("build-log.txt"))):
				var filename string
				if string(parts[last]) == "junit.failures" {
					filename = "junit"
				} else {
					filename = string(parts[last])
				}
				prefix := string(bytes.Join(parts[:last], []byte("/")))
				if last > 3 && bytes.Equal(parts[2], []byte("pull")) {
					name = fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<h5 class="mb-3">%s from PR %s <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a>%s</h5><pre class="small">`, template.HTMLEscapeString(filename), template.HTMLEscapeString(string(parts[3])), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name), template.HTMLEscapeString(age))
				} else {
					name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<h5 class="mb-3">%s from build <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a>%s</h5><pre class="small">`, template.HTMLEscapeString(filename), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name), template.HTMLEscapeString(age))
				}
			default:
				fmt.Fprintf(bw, `<h5 class="mb-3">%s%s</h5><pre class="small">`, template.HTMLEscapeString(name), template.HTMLEscapeString(age))
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

func renderSummary(ctx context.Context, w http.ResponseWriter, index *Index, generator CommandGenerator, start time.Time, resultMeta ResultMetadata) (int, error) {
	count := 0
	currentLines := 0
	var lastName string
	bw := bufio.NewWriterSize(w, 256*1024)
	fmt.Fprintf(bw, `<table class="table table-reponsive"><tbody><tr><th>Type</th><th>Job</th><th>Age</th><th># of hits</th></tr>`)
	err := executeGrep(ctx, generator, index, 30, func(name string, matches []bytes.Buffer, moreLines int) {
		if count == 5 || count%50 == 0 {
			bw.Flush()
		}
		name = strings.Trim(name, "/")
		if lastName == name {
			// continue accumulating matches
		} else {
			lastName = name

			if count > 0 {
				fmt.Fprintf(bw, "<td>%d</td>", currentLines)
				fmt.Fprintf(bw, `</tr>`)
				currentLines = 0
			}
			count++

			var age string
			result, ok := resultMeta.MetadataFor(name)
			if ok {
				duration := start.Sub(result.FailedAt)
				age = units.HumanDuration(duration) + " ago"
			}

			fmt.Fprintf(bw, `<tr>`)
			parts := bytes.SplitN([]byte(name), []byte{filepath.Separator}, 8)
			last := len(parts) - 1
			switch {
			case last > 2 && (bytes.Equal(parts[last], []byte("junit.failures")) || bytes.Equal(parts[last], []byte("build-log.txt"))):
				var filename string
				if string(parts[last]) == "junit.failures" {
					filename = "junit"
				} else {
					filename = string(parts[last])
				}
				prefix := string(bytes.Join(parts[:last], []byte("/")))
				if last > 3 && bytes.Equal(parts[2], []byte("pull")) {
					name = fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<td>%s</td><td><a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></td><td>%s</td>`, template.HTMLEscapeString(filename), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name), template.HTMLEscapeString(age))
				} else {
					name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<td>%s</td><td><a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></td><td>%s</td>`, template.HTMLEscapeString(filename), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name), template.HTMLEscapeString(age))
				}
			default:
				fmt.Fprintf(bw, `<td colspan="2">%s</td><td>%s</td>`, template.HTMLEscapeString(name), template.HTMLEscapeString(age))
			}
		}

		currentLines++
	})

	if count > 0 {
		fmt.Fprintf(bw, "<td>%d</td>", currentLines)
		fmt.Fprintf(bw, `</tr>`)
	}
	if err := bw.Flush(); err != nil {
		glog.Errorf("Unable to flush results buffer: %v", err)
	}
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
<p>Find JUnit test failures from <a href="/config">a subset of CI jobs</a> in <a href="https://deck-ci.svc.ci.openshift.org">OpenShift CI</a>.</p>
<p>The search input will use <a href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a>.</p>
<p>Searches are case-insensitive (using ripgrep "smart casing")</p>
<p>Examples:
<ul>
<li><code>timeout</code> - all JUnit failures with 'timeout' in the result</li>
<li><code>status code \d{3}\s</code> - all failures that contain 'status code' followed by a 3 digit number</li>
</ul>
<p>You can alter the age of results to search with the dropdown next to the search bar. Note that older results are pruned and may not be available after 14 days.</p>
<p>The amount of surrounding text returned with each match can be changed, including none.
<p>Currently indexing %s across %d entries</p>
</div>
`
