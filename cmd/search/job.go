package main

import (
	"errors"
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

	"github.com/golang/glog"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

var uriNotFoundError = errors.New("URI not found")

type ProwJob struct {
	Type     string `json:"type"`
	State    string `json:"state"`
	URL      string `json:"url"`
	Finished string `json:"finished"`
	Job      string `json:"job"`
	BuildID  string `json:"build_id"`
}

func fetchJob(client *http.Client, job *ProwJob, indexedPaths *pathIndex, toDir string, jobURIPrefix *url.URL, artifactURIPrefix *url.URL, deckURI *url.URL) error {
	date, err := time.Parse(time.RFC3339, job.Finished)
	if err != nil {
		return fmt.Errorf("prow job %s #%s had invalid date: %s", job.Job, job.BuildID, err)
	}
	logPath := job.URL
	if !strings.HasPrefix(logPath, jobURIPrefix.String()) {
		return fmt.Errorf("prow job %s %s had invalid URL: %s", job.Job, job.BuildID, logPath)
	}
	logPath = path.Join(strings.TrimPrefix(logPath, jobURIPrefix.String()), "build-log.txt")
	if _, ok := indexedPaths.MetadataFor(logPath); ok {
		return nil
	}

	uris := make([]*url.URL, 0, 2)
	if artifactURIPrefix != nil {
		uris = append(uris, artifactURIPrefix.ResolveReference(&url.URL{Path: logPath}))
	}

	if deckURI != nil {
		uri := *deckURI
		uri.Path = "/log"
		query := url.Values{"id": []string{job.BuildID}, "job": []string{job.Job}}
		uri.RawQuery = query.Encode()
		uris = append(uris, &uri)
	}

	if len(uris) == 0 {
		return fmt.Errorf("either the artifact-URI prefix or the deck URI must be set")
	}

	pathOnDisk := filepath.Join(toDir, filepath.FromSlash(logPath))
	errs := []error{}
	for _, uri := range uris {
		err = fetchArtifact(client, uri, pathOnDisk, date)
		if err == nil {
			break
		} else if err != uriNotFoundError {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

func fetchArtifact(client *http.Client, uri *url.URL, path string, date time.Time) error {
	defer glog.V(4).Infof("Fetch %s to %s", uri, path)
	resp, err := client.Get(uri.String())
	if err != nil {
		return fmt.Errorf("unable to fetch artifact: %v", err)
	}
	defer func() {
		// ensure we pull the body completely so connections are reused
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 404 {
			return uriNotFoundError
		}
		return fmt.Errorf("unable to fetch artifact %s: %d %s", uri.String(), resp.StatusCode, resp.Status)
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0777); err != nil {
		return fmt.Errorf("unable to create directory for artifact: %v", err)
	}

	f, err := os.OpenFile(path, os.O_EXCL|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("unable to fetch artifact, could not create log file: %v", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("unable to fetch artifact, could not copy log file: %v", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("unable to fetch artifact, could not close log file: %v", err)
	}

	if err := os.Chtimes(path, date, date); err != nil {
		return fmt.Errorf("unable to set file time while indexing to disk: %v", err)
	}

	return nil
}
