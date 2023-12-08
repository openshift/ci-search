package bugzilla

import (
	"context"
	"flag"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(flag.CommandLine)
}

func TestClient_BugsByID(t *testing.T) {
	c := NewClient(url.URL{Scheme: "https", Host: "bugzilla.redhat.com", Path: "/rest"})
	got, err := c.BugsByID(context.TODO(), 1812261)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Faults) > 0 {
		t.Fatalf("%#v", got.Faults)
	}
	if len(got.Bugs) != 1 {
		t.Fatalf("%#v", got.Bugs)
	}
	t.Logf("%#v", got.Bugs[0])
}
func TestClient_BugCommentsByID(t *testing.T) {
	c := NewClient(url.URL{Scheme: "https", Host: "bugzilla.redhat.com", Path: "/rest"})
	got, err := c.BugCommentsByID(context.TODO(), 1812261, 1811648)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Bugs) != 2 {
		t.Fatalf("%#v", got.Bugs)
	}
	comments, ok := got.Bugs[1812261]
	if !ok {
		t.Fatal("missing expected comments")
	}
	if len(comments.Comments) == 0 {
		t.Fatal("missing expected comments")
	}
	t.Logf("%#v", comments.Comments)
}

func TestClient_Search(t *testing.T) {
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
	got, err := c.SearchBugs(context.TODO(), SearchBugsArgs{
		LastChangeTime: time.Unix(1584221752, 0).Add(-72 * time.Hour),
		Quicksearch:    "cf_internal_whiteboard:buildcop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Faults) > 0 {
		t.Fatalf("%#v", got.Faults)
	}
	if len(got.Bugs) != 1 {
		t.Fatalf("%#v", got.Bugs)
	}
	t.Logf("%#v", got.Bugs[0])
}
