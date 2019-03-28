package main

import (
	"flag"
	"fmt"
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
	metadata  ResultMetadata
}

func (o *options) Run() error {
	var err error

	indexedPaths := &pathIndex{base: o.Path, maxAge: o.MaxAge}
	if err := indexedPaths.Load(); err != nil {
		return err
	}
	o.accessor = indexedPaths
	o.metadata = indexedPaths

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

	// index what is on disk now
	for i := 0; i < 3; i++ {
		err := indexedPaths.Load()
		if err == nil {
			break
		}
		glog.Errorf("Failed to update indexed paths, retrying: %v", err)
		time.Sleep(time.Second)
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
