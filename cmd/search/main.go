package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/wait"
)

func main() {
	original := flag.CommandLine
	original.Set("alsologtostderr", "true")
	original.Set("v", "2")

	opt := &options{
		ListenAddr: ":8080",
		MaxAge:     14 * 24 * time.Hour,
	}
	cmd := &cobra.Command{
		Run: func(cmd *cobra.Command, arguments []string) {
			if err := opt.Run(); err != nil {
				glog.Exitf("error: %v", err)
			}
		},
	}
	flag := cmd.Flags()

	flag.StringVar(&opt.Path, "path", opt.Path, "The directory to save index results to.")
	flag.StringVar(&opt.ListenAddr, "listen", opt.ListenAddr, "The address to serve release information on")
	flag.AddGoFlag(original.Lookup("v"))

	flag.DurationVar(&opt.MaxAge, "max-age", opt.MaxAge, "The maximum age of entries to keep cached. Set to 0 to keep all. Defaults to 14 days.")
	flag.DurationVar(&opt.Interval, "interval", opt.Interval, "The interval to index jobs. Set to 0 (the default) to disable indexing.")
	flag.StringVar(&opt.ConfigPath, "config", opt.ConfigPath, "Path on disk to a testgrid config for indexing.")
	flag.StringVar(&opt.GCPServiceAccount, "gcp-service-account", opt.GCPServiceAccount, "Path to a GCP service account file.")

	if err := cmd.Execute(); err != nil {
		glog.Exitf("error: %v", err)
	}
}

type options struct {
	ListenAddr string
	Path       string

	// arguments to indexing
	MaxAge            time.Duration
	Interval          time.Duration
	GCPServiceAccount string
	ConfigPath        string

	generator CommandGenerator
	accessor  PathAccessor
}

func (o *options) Run() error {
	var err error

	indexedPaths := &pathIndex{base: o.Path, maxAge: o.MaxAge}
	if err := indexedPaths.Load(); err != nil {
		return err
	}
	o.accessor = indexedPaths

	o.generator, err = NewCommandGenerator(o.Path, o.accessor)
	if err != nil {
		return err
	}

	if len(o.ListenAddr) > 0 {
		mux := mux.NewRouter()
		mux.HandleFunc("/config", o.handleConfig)
		mux.HandleFunc("/", o.handleIndex)

		go func() {
			glog.Infof("Listening on %s", o.ListenAddr)
			if err := http.ListenAndServe(o.ListenAddr, mux); err != nil {
				glog.Exitf("Server exited: %v", err)
			}
		}()
	}

	if o.Interval > 0 {
		// read the index timestamp
		var indexedAt time.Time
		indexedAtPath := filepath.Join(o.Path, ".indexed-at")
		if data, err := ioutil.ReadFile(indexedAtPath); err == nil {
			if value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
				indexedAt = time.Unix(value, 0)
				glog.Infof("Last indexed at %s", indexedAt)
			}
		}

		now := time.Now()

		if o.MaxAge > 0 {
			glog.Infof("Results expire after %s", o.MaxAge)
			expiredAt := now.Add(-o.MaxAge)
			if expiredAt.After(indexedAt) {
				glog.Infof("Last index time is older than the allowed max age, setting to %s", expiredAt)
				indexedAt = expiredAt
			}
		}

		if !indexedAt.IsZero() {
			sinceLast := now.Sub(indexedAt)
			if sinceLast < o.Interval {
				sleep := o.Interval - sinceLast
				glog.Infof("Indexer will start in %s", sleep.Truncate(time.Second))
				time.Sleep(sleep)
			}
		}

		glog.Infof("Starting build-indexer every %s", o.Interval)
		wait.Forever(func() {
			args := []string{"--config", o.ConfigPath, "--path", o.Path, "--max-results", "500"}
			if len(o.GCPServiceAccount) > 0 {
				args = append(args, "--gcp-service-account", o.GCPServiceAccount)
			}
			if !indexedAt.IsZero() {
				args = append(args, "--finished-after", strconv.FormatInt(indexedAt.Unix(), 10))
			}
			cmd := exec.Command("build-indexer", args...)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr

			indexedAt = time.Now()
			if err := cmd.Run(); err != nil {
				glog.Errorf("Failed to index: %v", err)
				return
			}
			indexDuration := time.Now().Sub(indexedAt)

			// keep the index time stored on successful updates
			glog.Infof("Index successful at %s, took %s", indexedAt, indexDuration.Truncate(time.Second))
			if err := ioutil.WriteFile(indexedAtPath, []byte(fmt.Sprintf("%d", indexedAt.Unix())), 0644); err != nil {
				glog.Errorf("Failed to write index marker: %v", err)
			}

			for i := 0; i < 3; i++ {
				err := indexedPaths.Load()
				if err == nil {
					break
				}
				glog.Errorf("Failed to update indexed paths, retrying: %v", err)
				time.Sleep(time.Second)
			}
		}, o.Interval)
	}

	select {}
}

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

	if err := req.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusInternalServerError)
		return
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
		if err != nil || num < 0 || num > 15 {
			http.Error(w, "?context must be a number between 0 and 15", http.StatusInternalServerError)
			return
		}
		index.Context = num
	}
	contextOptions := []string{
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
	case 0, 1, 2, 3, 5, 7, 10, 15:
	default:
		context := template.HTMLEscapeString(strconv.Itoa(index.Context))
		contextOptions = append(contextOptions, fmt.Sprintf(`<option value="%s" selected>%s</option>`, context, context))
	}

	switch req.FormValue("type") {
	case "junit":
		index.SearchType = "junit"
	case "all", "":
		index.SearchType = "all"
	default:
		http.Error(w, "?search must be 'junit', 'all'", http.StatusInternalServerError)
		return
	}
	var searchTypeOptions []string
	for _, searchType := range []string{"junit", "all"} {
		var selected string
		if searchType == index.SearchType {
			selected = "selected"
		}
		searchTypeOptions = append(searchTypeOptions, fmt.Sprintf(`<option value="%s" %s>%s</option>`, template.HTMLEscapeString(searchType), selected, template.HTMLEscapeString(searchType)))
	}

	if value := req.FormValue("maxAge"); len(value) > 0 {
		maxAge, err := time.ParseDuration(value)
		if err != nil || maxAge < 0 {
			http.Error(w, "?maxAge must be a non-negative duration", http.StatusInternalServerError)
			return
		}
		index.MaxAge = maxAge
	}
	if o.MaxAge > 0 && o.MaxAge < index.MaxAge {
		index.MaxAge = o.MaxAge
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
	if len(search) == 0 {
		stats := o.accessor.Stats()
		fmt.Fprintf(w, htmlEmptyPage, units.HumanSize(float64(stats.Size)), stats.Entries)
		fmt.Fprintf(w, htmlPageEnd)
		return
	}

	// perform a search
	flusher.Flush()

	count := 0
	start := time.Now()
	var lastName string
	fmt.Fprintf(w, `<div class="pl-3">`)

	bw := bufio.NewWriterSize(w, 256*1024)
	err := executeGrep(req.Context(), o.generator, index, 30, func(name string, matches []bytes.Buffer, moreLines int) {
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
			parts := bytes.SplitN([]byte(name), []byte{filepath.Separator}, 8)
			last := len(parts) - 1
			switch {
			case last > 2 && bytes.Equal(parts[last], []byte("junit.failures")):
				prefix := string(bytes.Join(parts[:last], []byte("/")))
				if last > 3 && bytes.Equal(parts[2], []byte("pull")) {
					name = fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<div class="mb-4"><h5 class="mb-3">junit from PR %s <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(string(parts[3])), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				} else {
					name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
					fmt.Fprintf(bw, `<div class="mb-4"><h5 class="mb-3">junit from build <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
				}
			default:
				fmt.Fprintf(bw, `<div class="mb-4"><h5 class="mb-3">%s</h5><pre class="small">`, template.HTMLEscapeString(name))
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

		for _, line := range lines {
			template.HTMLEscape(bw, line)
			fmt.Fprintln(bw)
		}
		if moreLines > 0 {
			fmt.Fprintf(bw, "\n... %d lines not shown\n\n", moreLines)
		}
		count++
	})
	if count > 0 {
		fmt.Fprintf(bw, `</pre></div>`)
	}
	if err := bw.Flush(); err != nil {
		glog.Errorf("Unable to flush results buffer: %v", err)
	}
	duration := time.Now().Sub(start)
	glog.V(2).Infof("Search completed in %s", duration)
	if err != nil && err != io.EOF {
		glog.Errorf("Command exited with error: %v", err)
		fmt.Fprintf(w, `<p class="alert alert-danger>%s</p>"`, template.HTMLEscapeString(err.Error()))
		fmt.Fprintf(w, htmlPageEnd)
		return
	}
	stats := o.accessor.Stats()
	fmt.Fprintf(w, `<p class="small"><em>Found %d results in %s (%s in %d entries)</em></p>`, count, duration.Truncate(time.Millisecond), units.HumanSize(float64(stats.Size)), stats.Entries)
	fmt.Fprintf(w, "</div>")

	fmt.Fprintf(w, htmlPageEnd)
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
	<div class="input-group input-group-lg"><input autofocus name="search" class="form-control col-auto" value="%s" placeholder="Search OpenShift CI failures by entering a regex search ...">
	<select name="maxAge" class="form-control col-1" onchange="this.form.submit();">%s</select>
	<select name="context" class="form-control col-1" onchange="this.form.submit();">%s</select>
	<!--<select name="type" class="form-control col-1" onchange="this.form.submit();">%s</select>-->
	<input class="btn" type="submit" value="Search">
	</div>
</form>
`

const htmlEmptyPage = `
<div class="ml-3" style="margin-top: 3rem; color: #666;">
<p>Find test failures from <a href="/config">a subset of CI jobs</a> in <a href="https://deck-ci.svc.ci.openshift.org">OpenShift CI</a>.</p>
<p>The search input will use <a href="https://docs.rs/regex/0.2.5/regex/#syntax">ripgrep regular-expression patterns</a>.</p>
<p>Searches are case-insensitive (using ripgrep "smart casing")</p>
<p>Examples:
<ul>
<li><code>timeout</code> - all JUnit failures with 'timeout' in the result</li>
<li><code>status code \d{3}\s</code> - all failures that contain 'status code' followed by a 3 digit number</li>
</ul>
<p>You can alter the age of results to search with the dropdown next to the search bar. Note that older results are pruned and may not be available after 14 days.</p>
<p>Currently indexing %s across %d entries</p>
</div>
`
