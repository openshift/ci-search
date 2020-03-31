package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/openshift/ci-search/prow"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
)

func (o *options) handleJobs(w http.ResponseWriter, req *http.Request) {
	if o.jobAccessor == nil {
		http.Error(w, "Unable to serve jobs data because no prow data source was configured.", http.StatusInternalServerError)
		return
	}

	jobs, err := o.jobAccessor.List(labels.Everything())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load jobs: %v", err), http.StatusInternalServerError)
		return
	}
	// sort uncompleted -> newest completed -> oldest completed
	sort.Slice(jobs, func(i, j int) bool {
		iTime, jTime := jobs[i].Status.CompletionTime, jobs[j].Status.CompletionTime
		if iTime == nil {
			return true
		}
		if jTime == nil {
			return false
		}
		if iTime.Time.Equal(jTime.Time) {
			return true
		}
		return jTime.Time.Before(iTime.Time)
	})
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
