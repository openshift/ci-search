package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog"

	"github.com/openshift/ci-search/bugzilla"
)

func main() {
	original := flag.CommandLine
	klog.InitFlags(original)
	original.Set("alsologtostderr", "true")
	original.Set("v", "2")

	opt := &options{
		ListenAddr:        ":8080",
		MaxAge:            14 * 24 * time.Hour,
		JobURIPrefix:      "https://prow.svc.ci.openshift.org/view/gcs/",
		ArtifactURIPrefix: "https://storage.googleapis.com/",
	}
	cmd := &cobra.Command{
		Run: func(cmd *cobra.Command, arguments []string) {
			if err := opt.Run(); err != nil {
				klog.Fatalf("error: %v", err)
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
	flag.StringVar(&opt.JobURIPrefix, "job-uri-prefix", opt.JobURIPrefix, "URI prefix for converting job-detail pages to index names.  For example, https://prow.svc.ci.openshift.org/view/gcs/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 has an index name of origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 with the default job-URI prefix.")
	flag.StringVar(&opt.ArtifactURIPrefix, "artifact-uri-prefix", opt.ArtifactURIPrefix, "URI prefix for artifacts.  For example, origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 has build logs at https://storage.googleapis.com/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309/build-log.txt with the default artifact-URI prefix.")
	flag.StringVar(&opt.DeckURI, "deck-uri", opt.DeckURI, "URL to the Deck server to index prow job failures into search.")

	flag.StringVar(&opt.BugzillaURL, "bugzilla-url", opt.BugzillaURL, "The URL of a bugzilla server to index bugs from.")
	flag.StringVar(&opt.BugzillaTokenPath, "bugzilla-token-file", opt.BugzillaTokenPath, "A file to read a bugzilla token from.")
	flag.StringVar(&opt.BugzillaSearch, "bugzilla-search", opt.BugzillaSearch, "A quicksearch query to search for bugs to index.")

	if err := cmd.Execute(); err != nil {
		klog.Exitf("error: %v", err)
	}
}

type options struct {
	ListenAddr string
	Path       string

	// arguments to indexing
	MaxAge            time.Duration
	Interval          time.Duration
	GCPServiceAccount string
	JobURIPrefix      string
	ArtifactURIPrefix string
	ConfigPath        string
	DeckURI           string

	BugzillaURL       string
	BugzillaSearch    string
	BugzillaTokenPath string

	generator CommandGenerator

	jobs         *pathIndex
	jobsPath     string
	jobURIPrefix *url.URL

	bugs         *bugzilla.CommentStore
	bugsPath     string
	bugURIPrefix *url.URL
}

type IndexStats struct {
	Bugs    int
	Entries int
	Size    int64
}

// Stats returns aggregate statistics for the indexed paths.
func (o *options) Stats() IndexStats {
	j := o.jobs.Stats()
	b := o.bugs.Stats()
	return IndexStats{
		Entries: j.Entries,
		Size:    j.Size,
		Bugs:    b.Bugs,
	}
}

func (o *options) SearchPaths(index *Index, initial []string) ([]string, error) {
	switch index.SearchType {
	case "bug":
		if o.bugURIPrefix == nil {
			return nil, fmt.Errorf("searching on bugs is not enabled")
		}
		return append(initial, "--glob", "!z-bug-*", o.bugsPath), nil
	case "all", "bug+junit":
		if o.bugURIPrefix != nil {
			initial = append(initial, "--glob", "!z-bug-*", o.bugsPath)
		}
		fallthrough
	default:
		if o.jobURIPrefix == nil {
			return nil, fmt.Errorf("searching on jobs is not enabled")
		}
		return o.jobs.SearchPaths(index, initial)
	}
}

func (o *options) MetadataFor(path string) (*Result, error) {
	switch {
	case strings.HasPrefix(path, "bugs/"):
		if o.bugURIPrefix == nil {
			return nil, fmt.Errorf("searching on bugs is not enabled")
		}
		path = strings.TrimPrefix(path, "bugs/")

		var result Result
		result.FileType = "bug"
		name := path
		if !strings.HasPrefix(name, "bug-") {
			return nil, fmt.Errorf("expected path bugs/bug-NUMBER: %s", path)
		}
		name = name[4:]
		id, err := strconv.Atoi(name)
		if err != nil {
			return nil, fmt.Errorf("expected path bugs/bug-NUMBER: %s", path)
		}
		result.Name = fmt.Sprintf("Bug %d", id)
		result.Number = id

		copied := *o.bugURIPrefix
		copied.RawQuery = url.Values{"id": []string{strconv.Itoa(id)}}.Encode()
		result.URI = &copied

		if comments, ok := o.bugs.Get(id); ok {
			// take the time of last bug update or comment, whichever is newer
			if l := len(comments.Comments); l > 0 {
				result.LastModified = comments.Comments[l-1].CreationTime.Time
			}
			if comments.Info.LastChangeTime.After(result.LastModified) {
				result.LastModified = comments.Info.LastChangeTime.Time
			}
			if len(comments.Info.Summary) > 0 {
				if len(comments.Info.Status) > 0 {
					result.Name = fmt.Sprintf("Bug %d: %s %s", id, comments.Info.Summary, comments.Info.Status)
				} else {
					result.Name = fmt.Sprintf("Bug %d: %s", id, comments.Info.Summary)
				}
			}
		}

		result.IgnoreAge = true

		return &result, nil

	case strings.HasPrefix(path, "jobs/"):
		if o.jobURIPrefix == nil {
			return nil, fmt.Errorf("searching on jobs is not enabled")
		}
		path = strings.TrimPrefix(path, "jobs/")

		parts := strings.SplitN(path, "/", 7)
		last := len(parts) - 1

		var result Result
		result.URI = o.jobURIPrefix.ResolveReference(&url.URL{Path: strings.Join(parts[:last], "/")})

		switch parts[last] {
		case "build-log.txt":
			result.FileType = "build-log"
		case "junit.failures":
			result.FileType = "junit"
		default:
			result.FileType = parts[last]
		}

		var err error
		result.Number, err = strconv.Atoi(parts[last-1])
		if err != nil {
			return nil, err
		}

		if last < 3 {
			return nil, fmt.Errorf("not enough parts (%d < 3)", last)
		}
		result.Name = parts[last-2]

		switch parts[1] {
		case "logs":
			result.Trigger = "build"
		case "pr-logs":
			result.Trigger = "pull"
		default:
			result.Trigger = parts[1]
		}

		result.LastModified = o.jobs.LastModified(path)

		return &result, nil
	default:
		return nil, fmt.Errorf("unrecognized result path: %s", path)
	}
}

func (o *options) Run() error {
	jobURIPrefix, err := url.Parse(o.JobURIPrefix)
	if err != nil {
		klog.Exitf("Unable to parse --job-uri-prefix: %v", err)
	}
	o.jobURIPrefix = jobURIPrefix

	artifactURIPrefix, err := url.Parse(o.ArtifactURIPrefix)
	if err != nil {
		klog.Exitf("Unable to parse --artifact-uri-prefix: %v", err)
	}

	o.jobsPath = filepath.Join(o.Path, "jobs")
	o.bugsPath = filepath.Join(o.Path, "bugs")

	indexedPaths := &pathIndex{
		base:    o.jobsPath,
		baseURI: jobURIPrefix,
		maxAge:  o.MaxAge,
	}
	o.jobs = indexedPaths

	if len(o.BugzillaURL) > 0 {
		url, err := url.Parse(o.BugzillaURL)
		if err != nil {
			klog.Exitf("Unable to parse --bugzilla-url: %v", err)
		}

		u := *url
		u.Path = "show_bug.cgi"
		o.bugURIPrefix = &u

		if len(o.BugzillaSearch) == 0 {
			klog.Exitf("--bugzilla-search is required")
		}
		tokenData, err := ioutil.ReadFile(o.BugzillaTokenPath)
		if err != nil {
			klog.Exitf("Failed to load --bugzilla-token-file: %v", err)
		}
		token := string(bytes.TrimSpace(tokenData))
		c := bugzilla.NewClient(*url)
		c.APIKey = token
		rt, err := rest.TransportFor(&rest.Config{})
		if err != nil {
			klog.Exitf("Unable to build bugzilla client: %v", err)
		}
		c.Client = &http.Client{Transport: rt}
		informer := bugzilla.NewInformer(
			c, 10*time.Minute, 30*time.Minute,
			func(metav1.ListOptions) bugzilla.SearchBugsArgs {
				return bugzilla.SearchBugsArgs{
					Quicksearch: o.BugzillaSearch,
				}
			},
			func(info *bugzilla.BugInfo) bool {
				return !contains(info.Keywords, "Security")
			},
		)
		lister := bugzilla.NewBugLister(informer.GetIndexer())
		store := bugzilla.NewCommentStore(c, 15*time.Minute, false)
		if err := os.MkdirAll(o.bugsPath, 0777); err != nil {
			return fmt.Errorf("unable to create directory for artifact: %v", err)
		}
		diskStore := bugzilla.NewCommentDiskStore(o.bugsPath, o.MaxAge)

		o.bugs = store

		ctx := context.Background()
		go informer.Run(ctx.Done())
		go store.Run(ctx, informer, diskStore)
		go diskStore.Run(ctx, lister, store)
		klog.Infof("Started bugzilla querier against %s with query %q", o.BugzillaURL, o.BugzillaSearch)
	} else {
		o.bugs = bugzilla.NewCommentStore(nil, 0, false)
	}

	if err := indexedPaths.Load(); err != nil {
		return err
	}

	o.generator, err = NewCommandGenerator(o.Path, o)
	if err != nil {
		return err
	}

	if len(o.ListenAddr) > 0 {
		mux := mux.NewRouter()
		mux.HandleFunc("/chart", o.handleChart)
		mux.HandleFunc("/chart.png", o.handleChartPNG)
		mux.HandleFunc("/config", o.handleConfig)
		mux.HandleFunc("/jobs", o.handleJobs)
		mux.HandleFunc("/search", o.handleSearch)
		mux.HandleFunc("/", o.handleIndex)

		go func() {
			klog.Infof("Listening on %s", o.ListenAddr)
			if err := http.ListenAndServe(o.ListenAddr, mux); err != nil {
				klog.Exitf("Server exited: %v", err)
			}
		}()
	}

	// index what is on disk now
	for i := 0; i < 3; i++ {
		err := indexedPaths.Load()
		if err == nil {
			break
		}
		klog.Errorf("Failed to update indexed paths, retrying: %v", err)
		time.Sleep(time.Second)
	}

	if o.Interval > 0 {
		// read the index timestamp
		var indexedAt time.Time
		indexedAtPath := filepath.Join(o.jobsPath, ".indexed-at")
		if data, err := ioutil.ReadFile(indexedAtPath); err == nil {
			if value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
				indexedAt = time.Unix(value, 0)
				klog.Infof("Last indexed at %s", indexedAt)
			}
		}

		now := time.Now()

		if o.MaxAge > 0 {
			klog.Infof("Results expire after %s", o.MaxAge)
			expiredAt := now.Add(-o.MaxAge)
			if expiredAt.After(indexedAt) {
				klog.Infof("Last index time is older than the allowed max age, setting to %s", expiredAt)
				indexedAt = expiredAt
			}
		}

		if !indexedAt.IsZero() {
			sinceLast := now.Sub(indexedAt)
			if sinceLast < o.Interval {
				sleep := o.Interval - sinceLast
				klog.Infof("Indexer will start in %s", sleep.Truncate(time.Second))
				time.Sleep(sleep)
			}
		}

		client := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout: 10 * time.Second,
				MaxConnsPerHost:     10,
				Dial: (&net.Dialer{
					Timeout: 30 * time.Second,
				}).Dial,
			},
		}

		var deckURI *url.URL
		if len(o.DeckURI) > 0 {
			u, err := url.Parse(o.DeckURI)
			if err != nil {
				klog.Exitf("Unable to parse --deck-uri: %v", err)
			}
			deckURI = u
		}

		klog.Infof("Starting build indexing (every %s)", o.Interval)
		wait.Forever(func() {
			var wg sync.WaitGroup
			if deckURI != nil {
				workCh := make(chan *ProwJob, 5)
				for i := 0; i < cap(workCh); i++ {
					wg.Add(1)
					go func() {
						defer klog.V(4).Infof("Indexer completed")
						defer wg.Done()
						for job := range workCh {
							if err := fetchJob(client, job, o, o.jobsPath, jobURIPrefix, artifactURIPrefix, deckURI); err != nil {
								klog.Warningf("Job index failed: %v", err)
								continue
							}
						}
					}()
				}
				go func() {
					defer klog.V(4).Infof("Lister completed")
					defer close(workCh)
					dataURI := *deckURI
					dataURI.Path = "/prowjobs.js"
					resp, err := client.Get(dataURI.String())
					if err != nil {
						klog.Errorf("Unable to index prow jobs from Deck: %v", err)
						return
					}
					defer resp.Body.Close()
					if resp.StatusCode < 200 || resp.StatusCode >= 300 {
						klog.Errorf("Unable to query prow jobs: %d %s", resp.StatusCode, resp.Status)
						return
					}

					newBytes, err := ioutil.ReadAll(resp.Body)
					if err != nil {
						klog.Errorf("Unable to read prow jobs from Deck: %v", err)
						return
					}

					var jobs ProwJobs
					if err := json.Unmarshal(newBytes, &jobs); err != nil {
						klog.Errorf("Unable to decode prow jobs from Deck: %v", err)
						return
					}

					jobLock.Lock()
					jobBytes = newBytes
					jobLock.Unlock()

					klog.Infof("Indexing failed build-log.txt files from prow (%d jobs)", len(jobs.Items))
					for i := range jobs.Items {
						job := &jobs.Items[i]
						if job.Status.State != "failure" {
							continue
						}
						// jobs without a URL are unfetchable
						if len(job.Status.URL) == 0 {
							continue
						}
						workCh <- job
					}
				}()
			}

			if o.ConfigPath != "" {
				wg.Add(1)
				go func() {
					defer klog.V(4).Infof("build-indexer completed")
					defer wg.Done()
					args := []string{"--config", o.ConfigPath, "--path", o.jobsPath, "--max-results", "500"}
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
						klog.Errorf("Failed to index: %v", err)
						return
					}
					indexDuration := time.Now().Sub(indexedAt)

					// keep the index time stored on successful updates
					klog.Infof("Index successful at %s, took %s", indexedAt, indexDuration.Truncate(time.Second))
					if err := ioutil.WriteFile(indexedAtPath, []byte(fmt.Sprintf("%d", indexedAt.Unix())), 0644); err != nil {
						klog.Errorf("Failed to write index marker: %v", err)
					}
				}()
			}

			wg.Wait()

			for i := 0; i < 3; i++ {
				err := indexedPaths.Load()
				if err == nil {
					break
				}
				klog.Errorf("Failed to update indexed paths, retrying: %v", err)
				time.Sleep(time.Second)
			}
		}, o.Interval)
	}

	select {}
}

func contains(arr []string, s string) bool {
	for _, item := range arr {
		if s == item {
			return true
		}
	}
	return false
}
