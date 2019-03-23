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
	Interval          time.Duration
	GCPServiceAccount string
	ConfigPath        string

	generator CommandGenerator
}

func (o *options) Run() error {
	var err error
	o.generator, err = NewCommandGenerator(o.Path)
	if err != nil {
		return err
	}

	if len(o.ListenAddr) > 0 {
		mux := mux.NewRouter()
		mux.HandleFunc("/", o.index)

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
		if !indexedAt.IsZero() {
			sinceLast := time.Now().Sub(indexedAt)
			if sinceLast < o.Interval {
				sleep := o.Interval - sinceLast
				glog.Infof("Indexer will start in %s", sleep.Truncate(time.Second))
				time.Sleep(sleep)
			}
		}

		glog.Infof("Starting build-indexer every %s", o.Interval)
		wait.Forever(func() {
			args := []string{"--config", o.ConfigPath, "--path", o.Path}
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
		}, o.Interval)
	}

	select {}
}

type nopFlusher struct{}

func (_ nopFlusher) Flush() {}

func (o *options) index(w http.ResponseWriter, req *http.Request) {
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
	switch req.FormValue("type") {
	case "junit":
		index.SearchType = "junit"
	case "all", "":
		index.SearchType = "all"
	default:
		http.Error(w, "?search must be 'junit', 'all'", http.StatusInternalServerError)
		return
	}
	var options []string
	for _, searchType := range []string{"junit", "all"} {
		var selected string
		if searchType == index.SearchType {
			selected = "selected"
		}
		options = append(options, fmt.Sprintf(`<option value="%s" %s>%s</option>`, template.HTMLEscapeString(searchType), selected, template.HTMLEscapeString(searchType)))
	}

	fmt.Fprintf(w, htmlPageStart, "Search results")
	fmt.Fprintf(w, htmlIndexForm, template.HTMLEscapeString(index.Search), strings.Join(options, ""))

	if len(search) > 0 {
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
						fmt.Fprintf(bw, `<div class="mb-4"><h5>junit from PR %s <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(string(parts[3])), template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
					} else {
						name := fmt.Sprintf("%s #%s", parts[last-2], parts[last-1])
						fmt.Fprintf(bw, `<div class="mb-4"><h5>junit from build <a href="https://openshift-gce-devel.appspot.com/build/%s/">%s</a></h5><pre class="small">`, template.HTMLEscapeString(prefix), template.HTMLEscapeString(name))
					}
				default:
					fmt.Fprintf(bw, `<div class="mb-4"><h5>%s</h5><pre class="small">`, template.HTMLEscapeString(name))
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
		fmt.Fprintf(w, `<p class="small"><em>Found %d results in %s</em></p>`, count, duration)
		fmt.Fprintf(w, "</div>")
	}

	fmt.Fprintf(w, htmlPageEnd)
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
<div class="container">
`

const htmlPageEnd = `
</div>
</body>
</html>
`

const htmlIndexForm = `
<form class="form mt-4 mb-4" method="GET">
	<div class="input-group input-group-lg"><input name="search" class="form-control col-auto" value="%s">
	<select name="type" class="form-control col-2" onchange="this.form.submit();">%s</select>
	<input class="btn" type="submit" value="Search">
	</div>
</form>
`
