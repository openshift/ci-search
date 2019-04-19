package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/golang/glog"
)

func (o *options) handleSearch(w http.ResponseWriter, req *http.Request) {
	index, err := o.parseRequest(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if len(index.Search) == 0 {
		http.Error(w, "The 'search' query parameter is required", http.StatusBadRequest)
		return
	}

	result := map[string]map[string][]*Match{}

	err = executeGrep(req.Context(), o.generator, index, 30, func(name string, search string, matches []bytes.Buffer, moreLines int) {
		metadata, _ := o.metadata.MetadataFor(name)

		if metadata.JobURI == nil {
			glog.Errorf("Failed to compute job URI for %q", name)
			return
		}
		uri := metadata.JobURI.String()
		_, ok := result[uri]
		if !ok {
			result[uri] = make(map[string][]*Match, 1)
		}

		_, ok = result[uri][search]
		if !ok {
			result[uri][search] = make([]*Match, 0, 1)
		}

		match := &Match{
			FileType:  metadata.FileType,
			MoreLines: moreLines,
		}

		for _, m := range matches {
			line := bytes.TrimRight(m.Bytes(), " ")
			match.Context = append(match.Context, string(line))
		}
		result[uri][search] = append(result[uri][search], match)
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed search: %v", err), http.StatusBadRequest)
		return
	}

	data, err := json.Marshal(result)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to serialize result: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
