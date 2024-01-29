package bugzilla

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

func TestListWatcher(t *testing.T) {
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

	lw := &ListWatcher{
		client:   c,
		interval: 30 * time.Second,
		argsFn: func(metav1.ListOptions) SearchBugsArgs {
			return SearchBugsArgs{
				Quicksearch: "cf_internal_whiteboard:buildcop",
			}
		},
	}
	obj, err := lw.List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list := obj.(*BugList)
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

	informer := NewInformer(c, 30*time.Second, 0, 1*time.Minute, func(metav1.ListOptions) SearchBugsArgs {
		return SearchBugsArgs{
			Quicksearch: "cf_internal_whiteboard:buildcop",
		}
	}, func(*BugInfo) bool { return true })
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if bug, ok := obj.(*Bug); ok {
				klog.Infof("ADD %s", bug.Name)
			} else {
				klog.Infof("ADD %#v", obj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if bug, ok := obj.(*Bug); ok {
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
