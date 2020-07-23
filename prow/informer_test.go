package prow

import (
	"context"
	"flag"
	"net/http"
	"net/url"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

func init() {
	klog.InitFlags(flag.CommandLine)
}

func TestInformer(t *testing.T) {
	c := NewClient(url.URL{Scheme: "https", Host: "prow.svc.ci.openshift.org", Path: "/prowjobs.js"})
	rt, err := rest.TransportFor(&rest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c.Client = &http.Client{Transport: rt}

	informer := NewInformer(30*time.Second, 10*time.Minute, 30*time.Minute, nil, c)
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if job, ok := obj.(*Job); ok {
				klog.Infof("ADD %s %s", job.Name, job.ResourceVersion)
			} else {
				klog.Infof("ADD %#v", obj)
			}
		},
		UpdateFunc: func(old, obj interface{}) {
			if job, ok := obj.(*Job); ok {
				klog.Infof("UPDATE %s %s", job.Name, job.ResourceVersion)
			} else {
				klog.Infof("UPDATE %#v", obj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if job, ok := obj.(*Job); ok {
				klog.Infof("DELETE %s %s", job.Name, job.ResourceVersion)
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
