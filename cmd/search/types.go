package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

type Result struct {
	// LastModified is the time when the item was last updated (job failure or bug update)
	LastModified time.Time

	// URI is the job detail page, e.g. https://prow.svc.ci.openshift.org/view/gcs/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309
	URI *url.URL

	// FileType is the type of file where the match was found, e.g. "build-log" or "junit".
	FileType string

	// Trigger is "pull" or "build".
	Trigger string

	// Name is a string to be printed to the user, which might be the job name or bug title
	Name string

	// Number is the job number, e.g. 309 for origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 or 5466 for origin-ci-test/pr-logs/pull/openshift_installer/1650/pull-ci-openshift-installer-master-e2e-aws/5466.
	Number int

	// IgnoreAge is true if the result should be included regardless of age.
	IgnoreAge bool
}

type Index struct {
	// One or more search strings. Some pages support only a single search
	Search []string

	// SearchType excludes jobs whose Result.FileType does not match.
	SearchType string

	// Job excludes jobs whose Result.Name does not match.
	Job *regexp.Regexp

	// MaxAge excludes jobs which failed longer than MaxAge ago.
	MaxAge time.Duration

	// MaxMatches caps the number of individual results within a file
	// that can be returned.
	MaxMatches int

	// Context includes this many lines of context around each match.
	Context int
}

func parseRequest(req *http.Request, mode string, maxAge time.Duration) (*Index, error) {
	if err := req.ParseForm(); err != nil {
		return nil, err
	}

	index := &Index{}

	index.Search, _ = req.Form["search"]
	if len(index.Search) == 0 && mode == "chart" {

		// CI-cluster issues
		index.Search = append(index.Search, "could not create or restart template instance.*")
		index.Search = append(index.Search, "could not (wait for|get) build.*") // https://bugzilla.redhat.com/show_bug.cgi?id=1696483

		// Installer and bootstrapping issues issues
		index.Search = append(index.Search, "level=error.*timeout while waiting for state.*") // https://bugzilla.redhat.com/show_bug.cgi?id=1690069 https://bugzilla.redhat.com/show_bug.cgi?id=1691516
		index.Search = append(index.Search, "Container setup exited with code ., reason Error")

		// Cluster-under-test issues
		index.Search = append(index.Search, "no providers available to validate pod")                          // https://bugzilla.redhat.com/show_bug.cgi?id=1705102
		index.Search = append(index.Search, "Error deleting EBS volume .* since volume is currently attached") // https://bugzilla.redhat.com/show_bug.cgi?id=1704356
		index.Search = append(index.Search, "clusteroperator/.* changed Degraded to True: .*")                 // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1702829 https://bugzilla.redhat.com/show_bug.cgi?id=1702832
		index.Search = append(index.Search, "Cluster operator .* is still updating.*")                         // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1700416
		index.Search = append(index.Search, "Pod .* is not healthy")                                           // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1700100

		index.Search = append(index.Search, "failed: \\(.*")
	}

	switch req.FormValue("type") {
	case "":
		if mode == "chart" {
			index.SearchType = "all"
		} else {
			index.SearchType = "bug+junit"
		}
	case "bug+junit":
		index.SearchType = "bug+junit"
	case "bug":
		index.SearchType = "bug"
	case "junit":
		index.SearchType = "junit"
	case "build-log":
		index.SearchType = "build-log"
	case "all":
		index.SearchType = "all"
	default:
		return nil, fmt.Errorf("search type must be 'bug', 'junit', 'build-log', or 'all'")
	}

	if value := req.FormValue("name"); len(value) > 0 || mode == "chart" {
		if len(value) == 0 {
			value = "-e2e-"
		}
		var err error
		index.Job, err = regexp.Compile(value)
		if err != nil {
			return nil, fmt.Errorf("name is an invalid regular expression: %v", err)
		}
	}

	if value := req.FormValue("maxMatches"); len(value) > 0 {
		maxMatches, err := strconv.Atoi(value)
		if err != nil || maxMatches < 0 || maxMatches > 500 {
			return nil, fmt.Errorf("maxMatches must be a number between 0 and 500")
		}
		index.MaxMatches = maxMatches
	}

	if value := req.FormValue("maxAge"); len(value) > 0 {
		maxAge, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("maxAge is an invalid duration: %v", err)
		} else if maxAge < 0 {
			return nil, fmt.Errorf("maxAge must be non-negative: %v", err)
		}
		index.MaxAge = maxAge
	}
	if index.MaxAge == 0 {
		index.MaxAge = 2 * 24 * time.Hour
	}
	if index.MaxAge > maxAge {
		index.MaxAge = maxAge
	}

	if context := req.FormValue("context"); len(context) > 0 {
		num, err := strconv.Atoi(context)
		if err != nil || num < -1 || num > 15 {
			return nil, fmt.Errorf("context must be a number between -1 and 15")
		}
		index.Context = num
	} else if mode == "text" {
		index.Context = 2
	}

	return index, nil
}
