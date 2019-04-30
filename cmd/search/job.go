package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type ProwJob struct {
	Type     string `json:"type"`
	State    string `json:"state"`
	URL      string `json:"url"`
	Finished string `json:"finished"`
	Job      string `json:"job"`
	BuildID  string `json:"build_id"`
}

func fetchJob(client *http.Client, job *ProwJob, indexedPaths *pathIndex, toDir string, deckURL *url.URL) error {
	date, err := time.Parse(time.RFC3339, job.Finished)
	if err != nil {
		return fmt.Errorf("prow job %s #%s had invalid date: %s", job.Job, job.BuildID, err)
	}
	logPath := job.URL
	if !strings.HasPrefix(logPath, "https://openshift-gce-devel.appspot.com/build/") {
		return fmt.Errorf("prow job %s %s had invalid URL: %s", job.Job, job.BuildID, logPath)
	}
	logPath = path.Join(strings.TrimPrefix(logPath, "https://openshift-gce-devel.appspot.com/build/"), "build-log.txt")
	if _, ok := indexedPaths.MetadataFor(logPath); ok {
		return nil
	}

	logsURL := *deckURL
	logsURL.Path = "/log"
	query := url.Values{"id": []string{job.BuildID}, "job": []string{job.Job}}
	logsURL.RawQuery = query.Encode()
	resp, err := client.Get(logsURL.String())
	if err != nil {
		return fmt.Errorf("unable to index prow jobs from Deck: %v", err)
	}
	defer func() {
		// ensure we pull the body completely so connections are reused
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 404 {
			return nil
		}
		return fmt.Errorf("unable to query prow job logs %s: %d %s", logsURL.String(), resp.StatusCode, resp.Status)
	}
	pathOnDisk := filepath.Join(toDir, filepath.FromSlash(logPath))
	parent := filepath.Dir(pathOnDisk)
	if err := os.MkdirAll(parent, 0777); err != nil {
		return fmt.Errorf("unable to create directory for prow job index: %v", err)
	}
	f, err := os.OpenFile(pathOnDisk, os.O_EXCL|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("unable to index prow jobs from Deck, could not create log file: %v", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("unable to index prow jobs from Deck, could not copy log file: %v", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("unable to index prow jobs from Deck, could not close log file: %v", err)
	}
	if err := os.Chtimes(pathOnDisk, date, date); err != nil {
		return fmt.Errorf("unable to set file time while indexing to disk: %v", err)
	}
	return nil
}
