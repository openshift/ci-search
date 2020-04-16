package prow

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"
)

func Test_readJobRange(t *testing.T) {
	client, err := storage.NewClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	names := sets.NewString()
	now := time.Now()
	if err := readJobRange(context.TODO(), client.Bucket("origin-ci-test"), "job-state", now.Add(-3*time.Hour), now, func(attr *storage.ObjectAttrs) error {
		klog.Infof("%s", attr.Name)
		if names.Has(attr.Name) {
			return fmt.Errorf("duplicate name: %s", attr.Name)
		}
		names.Insert(attr.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func Test_IndexReader(t *testing.T) {
	client, err := storage.NewClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReadFromIndex(context.Background(), client, "origin-ci-test", "job-state", 100*time.Minute, url.URL{Scheme: "https", Host: "prow.svc.ci.openshift.org"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%#v", r)
}
