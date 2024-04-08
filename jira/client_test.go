package jira

import (
	"bytes"
	"context"
	"flag"
	"os"
	"testing"
	"time"

	"k8s.io/klog/v2"
	jiraClient "sigs.k8s.io/prow/prow/jira"
)

func init() {
	klog.InitFlags(flag.CommandLine)
}

func TestClient_IssuesByID(t *testing.T) {
	tokenData := os.Getenv("JIRA_API_KEY")
	if len(tokenData) == 0 {
		t.Skip("Must specified JIRA_API_KEY")
	}
	options := func(options *jiraClient.Options) {
		options.BearerAuth = func() (token string) {
			return string(bytes.TrimSpace([]byte(tokenData)))
		}
	}
	jc, _ := jiraClient.NewClient("https://issues.redhat.com", options)
	c := &Client{
		Client: jc,
	}
	got, err := c.IssuesByID(context.TODO(), 14585626)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("%#v", got)
	}
	t.Logf("%#v", got[0])
}

func TestClient_IssuesCommentsByID(t *testing.T) {
	tokenData := os.Getenv("JIRA_API_KEY")
	if len(tokenData) == 0 {
		t.Skip("Must specified JIRA_API_KEY")
	}
	options := func(options *jiraClient.Options) {
		options.BearerAuth = func() (token string) {
			return string(bytes.TrimSpace([]byte(tokenData)))
		}
	}
	jc, _ := jiraClient.NewClient("https://issues.redhat.com", options)
	c := &Client{
		Client: jc,
	}

	got, err := c.IssueCommentsByID(context.TODO(), 14585626, 14585611)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%#v", got)
	}
	for _, comment := range got {
		if comment.Fields.Comments.Comments == nil {
			t.Fatalf("missing expected comments on issue %s", comment.ID)
		}
		t.Logf("%#v", comment.Fields.Comments.Comments)
	}
}

func TestClient_Search(t *testing.T) {
	tokenData := os.Getenv("JIRA_API_KEY")
	if len(tokenData) == 0 {
		t.Skip("Must specified JIRA_API_KEY")
	}
	options := func(options *jiraClient.Options) {
		options.BearerAuth = func() (token string) {
			return string(bytes.TrimSpace([]byte(tokenData)))
		}
	}
	jc, _ := jiraClient.NewClient("https://issues.redhat.com", options)
	c := &Client{
		Client: jc,
	}

	closeToNowTime := time.Unix(1647007769, 0)
	got, err := c.SearchIssues(context.TODO(), SearchIssuesArgs{
		LastChangeTime: closeToNowTime,
		Jql:            "project='OpenShift Continuous Release'&issueType=bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range got {
		if !time.Time(issue.Fields.Updated).After(closeToNowTime) {
			t.Fatal("Found issue with updated time after the LastChangeTime")
		}
	}
	t.Logf("%d issues after %s", len(got), closeToNowTime)
}
