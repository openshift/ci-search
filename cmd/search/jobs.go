package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/openshift/ci-search/prow"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
)

var jobLock sync.Mutex
var jobBytes []byte

type Job prow.Job

func (job *Job) StartStop() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
	// var zero time.Time

	// started, err := time.Parse(time.RFC3339, job.Status.StartTime)
	// if err != nil {
	// 	return zero, zero, fmt.Errorf("prow job %s #%s had invalid 'startTime': %s", job.Spec.Job, job.Status.BuildID, err)
	// }

	// var finished time.Time
	// if job.Status.CompletionTime != "" {
	// 	finished, err = time.Parse(time.RFC3339, job.Status.CompletionTime)
	// 	if err != nil {
	// 		return zero, zero, fmt.Errorf("prow job %s #%s had invalid 'completionTime': %s", job.Spec.Job, job.Status.BuildID, err)
	// 	}
	// }

	// return started, finished, nil
}

func (o *options) handleJobs(w http.ResponseWriter, req *http.Request) {
	if o.jobLister == nil {
		http.Error(w, "Unable to serve jobs data because no Deck URI was configured.", http.StatusInternalServerError)
		return
	}

	jobs, err := o.jobLister.List(labels.Everything())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load jobs: %v", err), http.StatusInternalServerError)
		return
	}
	list := prow.JobList{Items: make([]prow.Job, 0, len(jobs))}
	for _, job := range jobs {
		list.Items = append(list.Items, *job)
	}
	data, err := json.Marshal(list)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to write jobs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writer := encodedWriter(w, req)
	defer writer.Close()
	if _, err := writer.Write(data); err != nil {
		klog.Errorf("Failed to write response: %v", err)
	}
}

func getJobs() ([]Job, error) {
	return nil, nil
}
