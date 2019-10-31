package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/golang/glog"
)

func (o *options) handleSearch(w http.ResponseWriter, req *http.Request) {
	index, err := o.parseRequest(req, "text")
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if len(index.Search) == 0 {
		http.Error(w, "The 'search' query parameter is required", http.StatusBadRequest)
		return
	}

	result, err := o.searchResult(req.Context(), index)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed search: %v", err), http.StatusInternalServerError)
	}

	data, err := json.Marshal(result)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to serialize result: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writer := encodedWriter(w, req)
	defer writer.Close()

	if _, err = writer.Write(data); err != nil {
		glog.Errorf("Failed to write response: %v", err)
	}
}

// searchResult returns a result[uri][search][]*Match.
func (o *options) searchResult(ctx context.Context, index *Index) (map[string]map[string][]*Match, error) {
	result := map[string]map[string][]*Match{}

	err := executeGrep(ctx, o.generator, index, 30, func(name string, search string, matches []bytes.Buffer, moreLines int) {
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

	return result, err
}
