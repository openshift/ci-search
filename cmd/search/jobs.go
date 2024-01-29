package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/openshift/ci-search/pkg/httpwriter"
	"github.com/openshift/ci-search/prow"
)

func (o *options) handleJobs(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	var success bool
	defer func() {
		klog.Infof("Render jobs duration=%s success=%t", time.Since(start).Truncate(time.Millisecond), success)
	}()

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
		iTime, jTime := jobs[i].Status.CompletionTime.Time, jobs[j].Status.CompletionTime.Time
		if iTime.Equal(jTime) {
			return true
		}
		if iTime.IsZero() && !jTime.IsZero() {
			return true
		}
		if !iTime.IsZero() && jTime.IsZero() {
			return false
		}
		return jTime.Before(iTime)
	})
	list := prow.JobList{Items: jobs}
	data, err := json.Marshal(list)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to write jobs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writer := httpwriter.ForRequest(w, req)
	defer writer.Close()
	if _, err := writer.Write(data); err != nil {
		klog.Errorf("Failed to write response: %v", err)
		return
	}

	success = true
}
