package jira

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	jiraClient "k8s.io/test-infra/prow/jira"
)

func TestListWatcher(t *testing.T) {
	if len(os.Getenv("JIRA_API_KEY")) == 0 {
		t.Skip("Must specified JIRA_API_KEY")
	}
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
	lw := &ListWatcher{
		client:   c,
		interval: 30 * time.Second,
		argsFn: func(metav1.ListOptions) SearchIssuesArgs {
			return SearchIssuesArgs{
				Jql: "project='OpenShift Continuous Release'&issueType=bug",
			}
		},
	}
	obj, err := lw.List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list := obj.(*IssueList)
	if len(list.Items) == 0 {
		t.Fatalf("%#v", list.Items)
	}
	t.Logf("%#v", list.Items)

	w, err := lw.Watch(metav1.ListOptions{ResourceVersion: list.ResourceVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	count := 0
	for event := range w.ResultChan() {
		t.Logf("%#v", event)
		count++
		if count > 2 {
			break
		}
	}
}

func TestInformer(t *testing.T) {
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

	informer := NewInformer(
		c,
		30*time.Second,
		0,
		1*time.Minute,
		func(metav1.ListOptions) SearchIssuesArgs {
			return SearchIssuesArgs{
				Jql: "project='OCPBUGSM'&status='In Progress'",
			}
		},
		func(*jiraBaseClient.Issue) bool {
			return true
		})
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if bug, ok := obj.(*Issue); ok {
				klog.Infof("ADD %s", bug.Name)
			} else {
				klog.Infof("ADD %#v", obj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if bug, ok := obj.(*Issue); ok {
				klog.Infof("DELETE %s", bug.Name)
			} else {
				klog.Infof("DELETE %#v", obj)
			}
		},
	})
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go informer.Run(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		t.Fatalf("unable to sync")
	}

	time.Sleep(2 * time.Minute)
}
