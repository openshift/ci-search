package bugzilla

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

func TestCommentStore(t *testing.T) {
	dir, err := ioutil.TempDir("", "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	c := NewClient(url.URL{Scheme: "https", Host: "bugzilla.redhat.com", Path: "/rest"})
	c.APIKey = os.Getenv("BUGZILLA_API_KEY")
	c.Token = os.Getenv("BUGZILLA_TOKEN")
	if len(c.Token) == 0 && len(c.APIKey) == 0 {
		t.Skip("Must specified BUGZILLA_API_KEY or BUGZILLA_TOKEN")
	}
	rt, err := rest.TransportFor(&rest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c.Client = &http.Client{Transport: rt}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	informer := NewInformer(c, 30*time.Second, 0, 1*time.Minute, func(metav1.ListOptions) SearchBugsArgs {
		return SearchBugsArgs{
			Quicksearch: "cf_internal_whiteboard:buildcop",
		}
	}, func(*BugInfo) bool { return true })
	lister := NewBugLister(informer.GetIndexer())
	store := NewCommentStore(c, 5*time.Minute, false)
	diskStore := NewCommentDiskStore(dir, 10*time.Minute)

	go informer.Run(ctx.Done())
	go store.Run(ctx, informer, diskStore)
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
			comments, ok := store.Get(bug.Info.ID)
			if !ok || comments.RefreshTime.IsZero() {
				klog.Infof("no comments for %d", bug.Info.ID)
				missing = true
				continue
			}
			if len(comments.Comments) == 0 {
				t.Fatalf("bug %d had zero comments: %#v", bug.Info.ID, comments)
			}
			klog.Infof("Bug %d had %d comments", bug.Info.ID, len(comments.Comments))
		}
		if missing {
			time.Sleep(time.Second)
			continue
		}
		break
	}
}
