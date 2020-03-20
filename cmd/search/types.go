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

	// Name is the name of the job, e.g. release-openshift-ocp-installer-e2e-aws-4.1 or pull-ci-openshift-origin-master-e2e-aws.
	Name string

	// Number is the job number, e.g. 309 for origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 or 5466 for origin-ci-test/pr-logs/pull/openshift_installer/1650/pull-ci-openshift-installer-master-e2e-aws/5466.
	Number int

	// IgnoreAge is true if the result should be included regardless of age.
	IgnoreAge bool
}

type Index struct {
	Search []string

	// Filtering the body of material being searched.

	// Search excludes jobs whose Result.FileType does not match.
	SearchType string

	// Job excludes jobs whose Result.Name does not match.
	Job *regexp.Regexp

	// MaxAge excludes jobs which failed longer than MaxAge ago.
	MaxAge time.Duration

	// Output configuration.

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
		// Basic source issues
		//index.Search = append(index.Search, "CONFLICT .*Merge conflict in .*")

		// CI-cluster issues
		index.Search = append(index.Search, "could not create or restart template instance.*")
		index.Search = append(index.Search, "could not (wait for|get) build.*") // https://bugzilla.redhat.com/show_bug.cgi?id=1696483
		/*
			index.Search = append(index.Search, "could not copy .* imagestream.*");  // https://bugzilla.redhat.com/show_bug.cgi?id=1703510
			index.Search = append(index.Search, "error: image .*registry.svc.ci.openshift.org/.* does not exist");
			index.Search = append(index.Search, "unable to find the .* image in the provided release image");
			index.Search = append(index.Search, "error: Process interrupted with signal interrupt.*");
			index.Search = append(index.Search, "pods .* already exists|pod .* was already deleted");
			index.Search = append(index.Search, "could not wait for RPM repo server to deploy.*");
			index.Search = append(index.Search, "could not start the process: fork/exec hack/tests/e2e-scaleupdown-previous.sh: no such file or directory");  // https://prow.svc.ci.openshift.org/view/gcs/origin-ci-test/logs/periodic-ci-azure-e2e-scaleupdown-v4.2/5
		*/

		// Installer and bootstrapping issues issues
		index.Search = append(index.Search, "level=error.*timeout while waiting for state.*") // https://bugzilla.redhat.com/show_bug.cgi?id=1690069 https://bugzilla.redhat.com/show_bug.cgi?id=1691516
		/*
			index.Search = append(index.Search, "checking install permissions: error simulating policy: Throttling: Rate exceeded");  // https://bugzilla.redhat.com/show_bug.cgi?id=1690069 https://bugzilla.redhat.com/show_bug.cgi?id=1691516
			index.Search = append(index.Search, "level=error.*Failed to reach target state.*");
			index.Search = append(index.Search, "waiting for Kubernetes API: context deadline exceeded");
			index.Search = append(index.Search, "failed to wait for bootstrapping to complete.*");
			index.Search = append(index.Search, "failed to initialize the cluster.*");
		*/
		index.Search = append(index.Search, "Container setup exited with code ., reason Error")
		//index.Search = append(index.Search, "Container setup in pod .* completed successfully");

		// Cluster-under-test issues
		index.Search = append(index.Search, "no providers available to validate pod")                          // https://bugzilla.redhat.com/show_bug.cgi?id=1705102
		index.Search = append(index.Search, "Error deleting EBS volume .* since volume is currently attached") // https://bugzilla.redhat.com/show_bug.cgi?id=1704356
		index.Search = append(index.Search, "clusteroperator/.* changed Degraded to True: .*")                 // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1702829 https://bugzilla.redhat.com/show_bug.cgi?id=1702832
		index.Search = append(index.Search, "Cluster operator .* is still updating.*")                         // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1700416
		index.Search = append(index.Search, "Pod .* is not healthy")                                           // e.g. https://bugzilla.redhat.com/show_bug.cgi?id=1700100
		/*
			index.Search = append(index.Search, "failed: .*oc new-app  should succeed with a --name of 58 characters");  // https://bugzilla.redhat.com/show_bug.cgi?id=1535099
			index.Search = append(index.Search, "failed to get logs from .*an error on the server");  // https://bugzilla.redhat.com/show_bug.cgi?id=1690168 closed as a dup of https://bugzilla.redhat.com/show_bug.cgi?id=1691055
			index.Search = append(index.Search, "openshift-apiserver OpenShift API is not responding to GET requests");  // https://bugzilla.redhat.com/show_bug.cgi?id=1701291
			index.Search = append(index.Search, "Cluster did not complete upgrade: timed out waiting for the condition");
			index.Search = append(index.Search, "Cluster did not acknowledge request to upgrade in a reasonable time: timed out waiting for the condition");  // https://bugzilla.redhat.com/show_bug.cgi?id=1703158 , also mentioned in https://bugzilla.redhat.com/show_bug.cgi?id=1701291#c1
			index.Search = append(index.Search, "failed: .*Cluster upgrade should maintain a functioning cluster");
		*/

		// generic patterns so you can hover to see details in the tooltip
		/*
			index.Search = append(index.Search, "error.*");
			index.Search = append(index.Search, "failed.*");
			index.Search = append(index.Search, "fatal.*");
		*/
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

	if value := req.FormValue("maxAge"); len(value) > 0 {
		maxAge, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("maxAge is an invalid duration: %v", err)
		} else if maxAge < 0 {
			return nil, fmt.Errorf("maxAge must be non-negative: %v", err)
		}
		index.MaxAge = maxAge
	}
	if mode == "chart" {
		if index.MaxAge == 0 || index.MaxAge > 24*time.Hour {
			index.MaxAge = 24 * time.Hour
		}
	} else {
		if index.MaxAge == 0 {
			index.MaxAge = 2 * 24 * time.Hour
		}
		if index.MaxAge > maxAge {
			index.MaxAge = maxAge
		}
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
