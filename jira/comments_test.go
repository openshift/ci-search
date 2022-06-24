package jira

import (
	"bytes"
	"context"
	"io/ioutil"
	"os"
	"strconv"
	"testing"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
	jiraClient "k8s.io/test-infra/prow/jira"
)

func TestCommentStore(t *testing.T) {
	dir, err := ioutil.TempDir("", "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

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

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	informer := NewInformer(c, 30*time.Second, 0, 1*time.Minute, func(metav1.ListOptions) SearchIssuesArgs {
		return SearchIssuesArgs{
			Jql: "id IN (13432112, 14603375, 12963126, 12963159)",
		}
	}, func(issue *jiraBaseClient.Issue) bool { return true })
	lister := NewIssueLister(informer.GetIndexer())
	diskStore := NewCommentDiskStore(dir, 10*time.Minute)
	store := NewCommentStore(c, 5*time.Minute, diskStore)

	go informer.Run(ctx.Done())
	go store.Run(ctx, informer)
	go diskStore.Run(ctx, lister, store, false)

	klog.Infof("waiting for caches to sync")
	cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)

	for {
		bugs, err := lister.List(labels.Everything())
		if err != nil {
			t.Fatal(err)
		}
		if len(bugs) == 0 {
			klog.Infof("no bugs")
			time.Sleep(time.Second)
			continue
		}

		var missing bool
		for _, bug := range bugs {
			a, _ := strconv.Atoi(bug.Info.ID)
			comments, ok := store.Get(a)
			if !ok || comments.RefreshTime.IsZero() {
				klog.Infof("no comments for %s", bug.Info.ID)
				missing = true
				continue
			}
			if len(comments.Comments) == 0 {
				t.Fatalf("bug %s had zero comments: %#v", bug.Info.ID, comments)
			}
			klog.Infof("JiraIssue %s had %d comments", bug.Info.ID, len(comments.Comments))
		}
		if missing {
			time.Sleep(time.Second)
			continue
		}
		break
	}
}
