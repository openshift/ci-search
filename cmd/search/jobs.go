package main

import (
	"fmt"
	"net/http"
	"sync"
)

var jobLock sync.Mutex
var jobBytes []byte

func (o *options) handleJobs(w http.ResponseWriter, req *http.Request) {
	if o.DeckURI == "" {
		http.Error(w, "Unable to serve jobs data because no Deck URI was configured.", http.StatusInternalServerError)
		return
	}

	if o.Interval.Seconds() == 0 {
		http.Error(w, "Unable to serve jobs data because no indexing interval was configured.", http.StatusInternalServerError)
		return
	}

	if jobBytes == nil {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(o.Interval.Seconds()))) // https://tools.ietf.org/html/rfc7231#section-7.1.3
		http.Error(w, "Unable to serve jobs data because we have not fetched it yet.", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	jobLock.Lock()
	defer jobLock.Unlock()
	w.Write(jobBytes)
}
