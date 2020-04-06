package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"k8s.io/klog"
)

func (o *options) handleSearch(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	defer func() { klog.Infof("Render search result in %s", time.Now().Sub(start).Truncate(time.Millisecond)) }()

	index, err := parseRequest(req, "text", o.MaxAge)
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
		klog.Errorf("Failed to write response: %v", err)
	}
}

// searchResult returns a result[uri][search][]*Match.
func (o *options) searchResult(ctx context.Context, index *Index) (map[string]map[string][]*Match, error) {
	result := map[string]map[string][]*Match{}

	var reJob *regexp.Regexp
	if index.Job != nil {
		re, err := regexp.Compile(fmt.Sprintf(`%[1]s[^%[1]s]*%[2]s[^%[1]s]*%[1]s`, string(filepath.Separator), index.Job.String()))
		if err != nil {
			return nil, fmt.Errorf("unable to build search path regexp: %v", err)
		}
		reJob = re
	}

	maxMatches := index.MaxMatches
	if maxMatches == 0 {
		maxMatches = 25
	}

	err := executeGrep(ctx, o.generator, index, maxMatches, func(name string, search string, matches []bytes.Buffer, moreLines int) {
		metadata, err := o.MetadataFor(name)
		if err != nil {
			klog.Errorf("unable to resolve metadata for: %s: %v", name, err)
			return
		}
		if metadata.URI == nil {
			klog.Errorf("Failed to compute job URI for %q", name)
			return
		}
		if reJob != nil && !reJob.MatchString(name) {
			return
		}
		uri := metadata.URI.String()
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
			line := bytes.TrimRightFunc(m.Bytes(), func(r rune) bool { return r == ' ' })
			match.Context = append(match.Context, string(line))
		}
		result[uri][search] = append(result[uri][search], match)
	})

	return result, err
}
