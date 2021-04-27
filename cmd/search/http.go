package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	units "github.com/docker/go-units"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"

	"github.com/openshift/ci-search/bugzilla"
	"github.com/openshift/ci-search/metricdb/httpgraph"
	"github.com/openshift/ci-search/pkg/httpwriter"
)

type nopFlusher struct{}

func (_ nopFlusher) Flush() {}

type Match struct {
	Name         string            `json:"name,omitempty"`
	LastModified metav1.Time       `json:"lastModified"`
	FileType     string            `json:"filename"`
	Context      []string          `json:"context,omitempty"`
	MoreLines    int               `json:"moreLines,omitempty"`
	URL          string            `json:"url,omitempty"`
	Bug          *bugzilla.BugInfo `json:"bugInfo,omitempty"`
}

type SearchResponseResult struct {
	Matches []*Match `json:"matches,omitempty"`
}
type SearchResponse struct {
	// SearchResults is a map of searchstring to search results that matched that search string
	Results map[string]SearchResponseResult `json:"results"`
}

func (o *options) handleConfig(w http.ResponseWriter, req *http.Request) {
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
	writer := httpwriter.ForRequest(w, req)
	defer writer.Close()
	if _, err = writer.Write(data); err != nil {
		klog.Errorf("Failed to write response: %v", err)
	}
}

func (o *options) handleIndex(w http.ResponseWriter, req *http.Request) {
	var index *Index
	var success bool
	start := time.Now()
	defer func() {
		klog.Infof("Render index %s duration=%s success=%t", index.String(), time.Now().Sub(start).Truncate(time.Millisecond), success)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = nopFlusher{}
	}

	var err error
	index, err = parseRequest(req, "text", o.MaxAge)
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if len(index.Search) == 0 {
		index.Search = []string{""}
	}
	if index.MaxMatches == 0 {
		index.MaxMatches = 5
	}
	if index.Context < 0 {
		index.MaxMatches = 1
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

	var groupByOptions []string
	for _, opt := range []string{"job", "none"} {
		var selected string
		switch {
		case !index.GroupByJob && opt == "none", index.GroupByJob && opt != "none":
			selected = "selected"
		}
		groupByOptions = append(groupByOptions, fmt.Sprintf(`<option value="%s" %s>%s</option>`, template.HTMLEscapeString(opt), selected, template.HTMLEscapeString(opt)))
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer := httpwriter.ForRequest(w, req)
	defer writer.Close()

	var wrapValue string
	nowrapClass := "nowrap"
	if index.WrapLines {
		wrapValue = "checked"
		nowrapClass = ""
	}

	fmt.Fprintf(writer, htmlPageStart, "Search OpenShift CI", nowrapClass)
	fmt.Fprintf(writer, htmlIndexForm,
		template.HTMLEscapeString(index.Search[0]),
		strings.Join(maxAgeOptions, ""),

		strings.Join(contextOptions, ""),
		strings.Join(searchTypeOptions, ""),
		template.HTMLEscapeString(index.IncludeName),
		template.HTMLEscapeString(index.ExcludeName),
		strconv.Itoa(index.MaxMatches),
		strconv.FormatInt(index.MaxBytes, 10),
		groupByOptions,
		wrapValue,
	)

	// display the empty results page
	if len(index.Search[0]) == 0 {
		stats := o.Stats()

		fmt.Fprintf(writer, htmlEmptyPage, o.DeckURI, units.HumanSize(float64(stats.Size)), stats.Entries, stats.FailedJobs, stats.Jobs, stats.Bugs)
		flusher.Flush()

		gw := &httpgraph.GraphDataWriter{}
		writer.Write(gw.
			Var("data").
			Series("", func(buf []byte) []byte {
				for i, bucket := range stats.Buckets {
					if i > 0 {
						buf = append(buf, ',')
					}
					buf = strconv.AppendInt(buf, bucket.T, 10)
				}
				return buf
			}).
			Series("", func(buf []byte) []byte {
				for i, bucket := range stats.Buckets {
					if i > 0 {
						buf = append(buf, ',')
					}
					buf = strconv.AppendInt(buf, int64(bucket.Jobs), 10)
				}
				return buf
			}).
			Series("", func(buf []byte) []byte {
				for i, bucket := range stats.Buckets {
					if i > 0 {
						buf = append(buf, ',')
					}
					buf = strconv.AppendInt(buf, int64(bucket.FailedJobs), 10)
				}
				return buf
			}).
			Done(""),
		)
		fmt.Fprintf(writer, htmlEmptyPageGraph)
		fmt.Fprintf(writer, htmlPageEnd)
		return
	}

	// perform a search
	fmt.Fprintf(writer, `<div style="margin-top: 3rem; position: relative" class="pl-3">`)
	flusher.Flush()

	switch {
	case index.GroupByJob:
		result, err := o.orderedSearchResults(req.Context(), index)
		if err != nil {
			klog.Errorf("Search %q failed with %d results: command failed: %v", index.Search[0], 0, err)
			fmt.Fprintf(writer, `<p class="alert alert-danger">error: %s</p>`, template.HTMLEscapeString(err.Error()))
			fmt.Fprintf(writer, htmlPageEnd)
			return
		}
		bw := bufio.NewWriterSize(writer, 2048)
		var numRuns int
		if result.Matches > 0 {
			fmt.Fprintln(bw, `<div class="table-responsive"><table class="table table-job-compact"><tbody>`)
			for _, bug := range result.Bugs {
				age, _ := formatAge(bug.Matches[0].LastModified.Time, start, index.MaxAge)
				name := bug.Name
				if i := strings.Index(name, ": "); i != -1 {
					name = name[i+2:]
				}
				fmt.Fprintf(bw, "<tr><td><a target=\"_blank\" href=\"%s\">#%d</a></td><td>%s</td><td class=\"text-nowrap\">%s</td><td class=\"col-12\">%s</td></tr>\n", template.HTMLEscapeString(bug.URI.String()), bug.Number, template.HTMLEscapeString(bug.Matches[0].FileType), template.HTMLEscapeString(age), template.HTMLEscapeString(name))
				if index.Context >= 0 {
					fmt.Fprintf(bw, "<tr class=\"row-match\"><td class=\"\" colspan=\"4\"><pre class=\"small\">")
					for _, match := range bug.Matches {
						if err := renderLinesString(bw, match.Context, match.MoreLines); err != nil {
							bw.Flush()
							klog.Errorf("Search %q failed with %d matches: command failed: %v", index.Search[0], numRuns, err)
							fmt.Fprintf(writer, `<p class="alert alert-danger">error: %s</p>`, template.HTMLEscapeString(err.Error()))
							fmt.Fprintf(writer, htmlPageEnd)
							return
						}
					}
					fmt.Fprintln(bw, "</pre></td></tr>")
				}
			}
			for _, job := range result.Jobs {
				stats := o.jobAccessor.JobStats(job.Name, nil, start.Add(-index.MaxAge), start.Add(time.Hour))
				var contents string
				if stats.Count > 0 {
					percentFail := math.Round(float64(stats.Failures) / float64(stats.Count) * 100)
					title := fmt.Sprintf("%d runs, %d failures, %d matching runs", stats.Count, stats.Failures, len(job.Instances))
					if stats.Failures == 0 {
						percentMatch := math.Round(float64(len(job.Instances)) / float64(stats.Count) * 100)
						contents = fmt.Sprintf(" - <em title=\"%s\">%d runs, %d%% failed, %d%% of runs match</em>", template.HTMLEscapeString(title), stats.Count, int(percentFail), int(percentMatch))
					} else {
						percentMatch := math.Round(float64(len(job.Instances)) / float64(stats.Failures) * 100)
						percentImpact := math.Round(float64(len(job.Instances)) / float64(stats.Count) * 100)
						contents = fmt.Sprintf(" - <em title=\"%s\">%d runs, %d%% failed, %d%% of failures match = %d%% impact</em>", template.HTMLEscapeString(title), stats.Count, int(percentFail), int(percentMatch), int(percentImpact))
					}
				}
				numRuns += len(job.Instances)

				uri := *job.Instances[0].URI
				if job.Trigger == "pull" {
					uri.Path = path.Join("job-history", o.IndexBucket, "pr-logs", "directory", job.Name)
				} else {
					uri.Path = path.Join("job-history", o.IndexBucket, "logs", job.Name)
				}
				copied := *index
				copied.MaxAge = o.MaxAge
				copied.ExcludeName = ""
				copied.IncludeName = fmt.Sprintf("^%s$", regexp.QuoteMeta(job.Name))
				uriAll := url.URL{Path: "/", RawQuery: copied.Query().Encode()}
				fmt.Fprintf(bw, "<tr><td colspan=\"4\"><a target=\"_blank\" href=\"%s\">%s</a> <a href=\"%s\">(all)</a>%s</td></tr>\n", template.HTMLEscapeString(uri.String()), template.HTMLEscapeString(job.Name), template.HTMLEscapeString(uriAll.String()), contents)
				for _, instance := range job.Instances {
					for _, match := range instance.Matches {
						age, _ := formatAge(match.LastModified.Time, start, index.MaxAge)
						fmt.Fprintf(bw, "<tr class=\"row-match\"><td><a target=\"_blank\" href=\"%s\">#%d</a></td><td>%s</td><td class=\"text-nowrap\">%s</td><td class=\"col-12\"></td></tr>\n", template.HTMLEscapeString(instance.URI.String()), instance.Number, template.HTMLEscapeString(match.FileType), template.HTMLEscapeString(age))
						if index.Context >= 0 {
							fmt.Fprintf(bw, "<tr class=\"row-match\"><td class=\"\" colspan=\"4\"><pre class=\"small\">")
							if err := renderLinesString(bw, match.Context, match.MoreLines); err != nil {
								bw.Flush()
								klog.Errorf("Search %q failed with %d matches: command failed: %v", index.Search[0], numRuns, err)
								fmt.Fprintf(writer, `<p class="alert alert-danger">error: %s</p>`, template.HTMLEscapeString(err.Error()))
								fmt.Fprintf(writer, htmlPageEnd)
								return
							}
							fmt.Fprintln(bw, "</pre></td></tr>")
						}
					}
				}
			}
			fmt.Fprintln(bw, "</table></div>")
		}
		bw.Flush()

		stats := o.jobAccessor.JobStats("", result.JobNames, start.Add(-index.MaxAge), start)

		title := fmt.Sprintf("%d runs, %d failing runs, %d matched runs, %d jobs, %d matched jobs", stats.Count, stats.Failures, numRuns, stats.Jobs, len(result.Jobs))
		fmt.Fprintf(writer, `<p style="position:absolute; top: -2rem;" class="small"><em title="%s">`, template.HTMLEscapeString(title))
		if stats.Count > 0 {
			percentImpact := float64(numRuns) / float64(stats.Count) * 100
			percentRuns := float64(numRuns) / float64(stats.Failures) * 100
			percentFailures := float64(stats.Failures) / float64(stats.Count) * 100
			fmt.Fprintf(writer, `Found in %.2f%% of runs (%.2f%% of failures) across %d total runs and %d jobs (%.2f%% failed) in %s`, percentImpact, percentRuns, stats.Count, stats.Jobs, percentFailures, time.Now().Sub(start).Truncate(time.Millisecond))
		} else {
			fmt.Fprintf(writer, `%d runs matched in %s`, numRuns, time.Now().Sub(start).Truncate(time.Millisecond))
		}
		fmt.Fprintf(writer, `</em> - <a href="/">clear search</a> | <a href="/chart?%s">chart view</a> - source code located <a target="_blank" href="https://github.com/openshift/ci-search">on github</a></p>`, template.HTMLEscapeString(req.URL.RawQuery))

		if numRuns == 0 && len(result.Bugs) == 0 {
			fmt.Fprintf(writer, `<p style="padding-top: 1em;"><em>No results found.</em></p><p><em>Search uses <a target="_blank" href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a> to find results. Try simplifying your search or using case-insensitive options.</em></p>`)
		}

	default:
		count, err := renderMatches(req.Context(), writer, index, o.generator, start, o)
		if err != nil {
			klog.Errorf("Search %q failed with %d results: command failed: %v", index.Search[0], count, err)
			fmt.Fprintf(writer, `<p class="alert alert-danger">error: %s</p>`, template.HTMLEscapeString(err.Error()))
			fmt.Fprintf(writer, htmlPageEnd)
			return
		}
		klog.V(2).Infof("Search %q over %q for job %s/%s completed with %d results", index.Search[0], index.SearchType, index.IncludeName, index.ExcludeName, count)
		fmt.Fprintf(writer, `<p style="position:absolute; top: -2rem;" class="small"><em>`)
		fmt.Fprintf(writer, `Found %d results in %s`, count, time.Now().Sub(start).Truncate(time.Millisecond))
		fmt.Fprintf(writer, `</em> - <a href="/">clear search</a> | <a href="/chart?%s">chart view</a> - source code located <a target="_blank" href="https://github.com/openshift/ci-search">on github</a></p>`, template.HTMLEscapeString(req.URL.RawQuery))
		if count == 0 {
			fmt.Fprintf(writer, `<p style="padding-top: 1em;"><em>No results found.</em></p><p><em>Search uses <a target="_blank" href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a> to find results. Try simplifying your search or using case-insensitive options.</em></p>`)
		}
	}

	fmt.Fprintf(writer, "</div>")

	fmt.Fprintf(writer, htmlPageEnd)

	success = true
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

func renderMatches(ctx context.Context, w io.Writer, index *Index, generator CommandGenerator, start time.Time, resolver PathResolver) (int, error) {
	count, lineCount, matchCount := 0, 0, 0
	lines := make([][]byte, 0, 64)

	bw := &sortableWriter{sizeLimit: 2 * 1024 * 1024, bw: bufio.NewWriterSize(w, 256*1024)}
	var lastName string
	drop := true
	err := executeGrep(ctx, generator, index, nil, func(name string, search string, matches []bytes.Buffer, moreLines int) error {
		if lastName == name {
			// continue accumulating matches
			if drop {
				return nil
			}
			if index.Context > 0 {
				fmt.Fprintf(bw, "\n&mdash;\n\n")
			}

		} else {
			// finish the last result
			lastName = name
			if !drop {
				if index.Context < 0 {
					fmt.Fprintf(bw, "<td>%d</td></tr>\n", matchCount)
				} else {
					fmt.Fprintf(bw, "</pre></td></tr>\n")
				}
			}
			drop = false
			matchCount = 0

			// decide whether to print the next result
			metadata, err := resolver.MetadataFor(name)
			if err != nil {
				klog.Errorf("unable to resolve metadata for: %s: %v", name, err)
				drop = true
				return nil
			}
			if metadata.URI == nil {
				klog.Errorf("no job URI for %q", name)
				drop = true
				return nil
			}
			if index.JobFilter != nil && !index.JobFilter(metadata.Name) {
				drop = true
				return nil
			}

			age, recent := formatAge(metadata.LastModified, start, index.MaxAge)
			if !metadata.IgnoreAge && !recent {
				klog.V(7).Infof("Filtered %s, older than query limit", name)
				drop = true
				return nil
			}

			count++
			if count == 1 {
				if index.Context >= 0 {
					fmt.Fprintln(bw, `<div class="table-responsive"><table class="table"><tbody><tr><th>Type</th><th>Job</th><th>Age</th></tr>`)
				} else {
					fmt.Fprintln(bw, `<div class="table-responsive"><table class="table"><tbody><tr><th>Type</th><th>Job</th><th>Age</th><th># of hits</th></tr>`)
				}
			}
			bw.SetIndex(-metadata.LastModified.Unix())
			switch metadata.FileType {
			case "bug":
				fmt.Fprintf(bw, `<tr><td>%s</td><td><a target="_blank" href="%s">%s</a></td><td class="text-nowrap">%s</td>`, template.HTMLEscapeString(metadata.FileType), template.HTMLEscapeString(metadata.URI.String()), template.HTMLEscapeString(metadata.Name), template.HTMLEscapeString(age))
			default:
				fmt.Fprintf(bw, `<tr><td>%s</td><td><a target="_blank" href="%s">%s #%d</a></td><td class="text-nowrap">%s</td>`, template.HTMLEscapeString(metadata.FileType), template.HTMLEscapeString(metadata.URI.String()), template.HTMLEscapeString(metadata.Name), metadata.Number, template.HTMLEscapeString(age))
			}

			if index.Context >= 0 {
				fmt.Fprintf(bw, "</tr>\n<tr class=\"row-match\"><td class=\"\" colspan=\"3\"><pre class=\"small\">")
			}
		}

		matchCount++
		if index.Context < 0 {
			return nil
		}

		// remove empty leading and trailing lines, but preserve the line buffer to limit allocations
		lines = trimMatches(matches, lines[:0])
		if err := renderLines(bw, lines, moreLines); err != nil {
			return err
		}
		lineCount += len(lines)
		return nil
	})

	if !drop {
		if index.Context <= 0 {
			fmt.Fprintf(bw, "<td>%d</td></tr>\n", matchCount)
		} else {
			fmt.Fprintf(bw, "</pre></td></tr>\n")
		}
	}
	if err := bw.Flush(); err != nil {
		klog.Errorf("Unable to flush results buffer: %v", err)
	}
	if count > 0 {
		fmt.Fprintf(w, "</table></div>\n")
	}
	return count, err
}

func formatAge(t time.Time, from time.Time, maxAge time.Duration) (string, bool) {
	if t.IsZero() {
		return "", true
	}
	duration := from.Sub(t)
	return units.HumanDuration(duration) + " ago", duration <= maxAge
}

func trimMatches(matches []bytes.Buffer, lines [][]byte) [][]byte {
	for _, m := range matches {
		line := bytes.TrimRightFunc(m.Bytes(), func(r rune) bool { return r == ' ' })
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
	return lines
}

func trimMatchStrings(matches []bytes.Buffer, lines []string) []string {
	for _, m := range matches {
		line := bytes.TrimRightFunc(m.Bytes(), func(r rune) bool { return r == ' ' })
		if len(line) == 0 {
			continue
		}
		lines = append(lines, string(line))
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if len(lines[i]) != 0 {
			break
		}
		lines = lines[:i]
	}
	return lines
}

func renderLines(bw io.Writer, lines [][]byte, moreLines int) error {
	for _, line := range lines {
		template.HTMLEscape(bw, line)
		if _, err := fmt.Fprintln(bw); err != nil {
			return err
		}
	}
	if moreLines > 0 {
		fmt.Fprintf(bw, "\n... %d lines not shown\n\n", moreLines)
	}
	return nil
}

func renderLinesString(bw io.Writer, lines []string, moreLines int) error {
	for _, line := range lines {
		template.HTMLEscape(bw, []byte(line))
		if _, err := fmt.Fprintln(bw); err != nil {
			return err
		}
	}
	if moreLines > 0 {
		fmt.Fprintf(bw, "\n... %d lines not shown\n\n", moreLines)
	}
	return nil
}

const htmlPageStart = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8"><title>%s</title>
<link rel="stylesheet" href="/static/bootstrap-4.4.1.min.css" integrity="sha384-Vkoo8x4CGsO3+Hhxv8T/Q5PaXtkKtu6ug5TOeNV6gBiFeWPGFN9MuhOf23Q9Ifjh" crossorigin="anonymous">
<link rel="stylesheet" href="/static/uPlot.min.css">
<meta name="viewport" content="width=device-width, initial-scale=1, shrink-to-fit=no">
<style>
#results.nowrap PRE { white-space: pre; }
#results PRE { width: calc(95vw - 2.5em); white-space: pre-wrap; padding-bottom: 1em; }
.row-match TD { border-top: 0; }
.table TD { padding-bottom: 0.25rem; }
#results .table-job-compact TD > PRE { margin-bottom: 0; padding-bottom: 0.25rem; }
</style>
</head>
<body>
<div id="results" class="container-fluid %s">
`

const htmlPageEnd = `
</div>
</body>
</html>
`

const htmlIndexForm = `
<form class="form mt-4 mb-4" method="GET">
	<div class="input-group input-group-lg mb-2">
		<input title="A regular expression over the contents of test logs and junit output - uses ripgrep regular expressions" autocomplete="off" autofocus name="search" class="form-control col-auto" value="%s" placeholder="Search OpenShift CI failures by entering a regex search ...">
		<select title="How far back to search for jobs" name="maxAge" class="form-control custom-select col-1" onchange="this.form.submit();">%s</select>
		<select title="Number of lines before and after the match to show" name="context" class="form-control custom-select col-1" onchange="this.form.submit();">%s</select>
		<select title="Type of results to return" name="type" class="form-control custom-select col-1" onchange="this.form.submit();">%s</select>
		<div class="input-group-append"><input class="btn btn-outline-primary" type="submit" value="Search"></div>
	</div>
	<div class="input-group input-group-sm mb-3">
		<div class="input-group-prepend"><span class="input-group-text" for="name">Job:</span></div>
		<input title="A regular expression that matches the name of a job or the title of a bug" class="form-control col-auto" name="name" value="%s" placeholder="Focus job or bug names by regex ...">
		<input title="A regular expression that matches the name of a job or the title of a bug" class="form-control col-auto" name="excludeName" value="%s" placeholder="Skip job or bug names by regex ...">
		<input title="The number of matches per job / file to show" autocomplete="off" class="form-control col-1" name="maxMatches" value="%s" placeholder="Max matches per job or bug">
		<input title="The maximum number of bytes for the response" autocomplete="off" class="form-control col-1" name="maxBytes" value="%s" placeholder="Max bytes to return">
		<select title="Group results by job (with stats) or no grouping" name="groupBy" class="form-control custom-select col-1" onchange="this.form.submit();">%s</select>
		<div class="input-group-append"><span class="input-group-text">
			<input id="wrap" type="checkbox" name="wrap" %s onchange="document.getElementById('results').classList.toggle('nowrap')">
			<label for="wrap" style="margin-bottom: 0; margin-left: 0.4em;">Wrap lines</label>
		</span></div>
	</div>
</form>
`

const htmlEmptyPage = `
<div class="ml-3" style="margin-top: 3rem; color: #666;">
<p>Find bugs and test failures from failed or flaky CI jobs in <a target="_blank" href="%s">OpenShift CI</a>.</p>
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
<p>You may filter by job name using regex controls:
<ul>
<li><code>^release-</code> - all jobs that start with 'release-'</li>
<li><code>UpgradeBlocker</code> - bugs that have 'UpgradeBlocker' in their title</li>
</ul>
<div id="width"></div>
<p id="graph">
<p>Currently indexing %s across %d results, %d failed jobs of %d, and %d bugs</p>
</div>
`
const htmlEmptyPageGraph = `
<style>
#overlay {
	position: absolute;
	background: rgba(0, 0, 0, 0.8);
	padding: 0.5rem;
	margin: 0.75rem;
	color: #fff;
	z-index: 10;
	pointer-events: none;
}
</style>
<script src="/static/uPlot.iife.min.js"></script>
<script src="/static/placement.min.js"></script>
<script>
let longDateHourMin = uPlot.fmtDate('{YYYY}-{MM}-{DD} {h}:{mm}{aa}');

function tooltipPlugin(opts) {
	let over, bound, bLeft, bTop;

	function syncBounds() {
		let bbox = over.getBoundingClientRect();
		bLeft = bbox.left;
		bTop = bbox.top;
	}

	const overlay = document.createElement("div");
	overlay.id = "overlay";
	overlay.style.display = "none";
	overlay.style.position = "absolute";
	document.body.appendChild(overlay);

	return {
		hooks: {
			init: u => {
				over = u.root.querySelector(".u-over");

				bound = over;
			//	bound = document.body;

				over.onmouseenter = () => {
					overlay.style.display = "block";
				};

				over.onmouseleave = () => {
					overlay.style.display = "none";
				};
			},
			setSize: u => {
				syncBounds();
			},
			setCursor: u => {
				const { left, top, idx } = u.cursor;
				const x = u.data[0][idx];
				const y = u.data[1][idx];
				const z = u.data[2][idx];
				const anchor = { left: left + bLeft, top: top + bTop };
				overlay.textContent = ` + "`" + `${z} failed of ${y}` + "`" + `;
				placement(overlay, anchor, "right", "start", { bound });
			}
		}
	};
}
function getSize() { 
	let o = document.getElementById("width")
	let w = o.scrollWidth
	if (w < 320)
		div = 2
	else if (w < 1024)
		div = (w - 320) / (1024-320) * 6 + 2
	else
		div = 8
	return { width: w, height: w / div,	} 
}
const opts = {
	legend: {show: false},
	...getSize(),
	plugins: [
		tooltipPlugin(),
	],
	series: [
		{},
		{
			label: "Jobs",
			values: (u, sidx, idx) => {
				let date = new Date(data[0][idx] * 1e3);
				return {
					Time: longDateHourMin(date),
					Value: data[sidx][idx],
				};
			},
			stroke: "blue",
			fill: "rgba(0,0,255,0.05)",
		},
		{
			label: "Failed Jobs",
			values: (u, sidx, idx) => {
				let date = new Date(data[0][idx] * 1e3);
				return {
					Time: longDateHourMin(date),
					Value: data[sidx][idx],
				};
			},
			value: (u, v) => v == null ? "-" : v.toFixed(1) + "%%",
			stroke: "red",
			fill: "rgba(255,0,0,0.05)",
		},
	],
};

if (data[0].length > 0) {
	let u = new uPlot(opts, data, document.getElementById("graph"));
	window.addEventListener("resize", e => { u.setSize(getSize()); })
}
</script>
`
