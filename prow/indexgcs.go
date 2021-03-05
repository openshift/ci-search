package prow

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
)

type ListerFunc func(ctx context.Context) ([]*Job, error)

func (l ListerFunc) ListJobs(ctx context.Context) ([]*Job, error) { return l(ctx) }

type CachingLister struct {
	Lister JobLister
	jobs   []*Job
}

func (l *CachingLister) ListJobs(ctx context.Context) ([]*Job, error) {
	if l.jobs == nil {
		var err error
		jobs, err := l.Lister.ListJobs(ctx)
		if err != nil {
			return nil, err
		}
		l.jobs = jobs
		return jobs, nil
	}
	return l.jobs, nil
}

// ReadFromIndex reads jobs from the named GCS bucket index (managed by a cloud function that watches for finished jobs) and returns
// them. Jobs older than maxAge are not loaded. statusURL will form the status URL for a given job if the job's link attribute in the
// index can be parsed.
func ReadFromIndex(ctx context.Context, client *storage.Client, bucket, indexName string, maxAge time.Duration, statusURL url.URL) ([]*Job, error) {
	index := &Index{
		Bucket:    bucket,
		IndexName: indexName,
	}

	start := time.Now()
	index.FromTime(start.Add(-maxAge))
	index.ToTime(start)

	jobs := make([]*Job, 0, 2048)
	i := 0
	if err := index.EachJob(ctx, client, 0, statusURL, func(job Job, attr *storage.ObjectAttrs) error {
		state, ok := attr.Metadata["state"]
		if !ok {
			return nil
		}
		completedString, ok := attr.Metadata["completed"]
		if !ok {
			return nil
		}
		completed, err := strconv.ParseInt(completedString, 10, 64)
		if err != nil {
			return nil
		}
		switch state {
		case "success":
		case "failed":
			state = "failure"
		case "error":
		default:
			return nil
		}
		i++
		job.Name = fmt.Sprintf("gcs-%d", i)
		job.Status.State = state
		job.Status.CompletionTime = metav1.Time{Time: time.Unix(completed, 0)}
		jobs = append(jobs, &job)
		return nil
	}); err != nil {
		if err == ctx.Err() {
			return nil, err
		}
		klog.Errorf("scan failed, will retry: %v", err)
	}
	klog.V(5).Infof("Found %d jobs in %s", len(jobs), time.Now().Sub(start))
	return jobs, nil
}

var (
	ErrSkip = fmt.Errorf("skip index data")
	ErrStop = fmt.Errorf("stop index scan")
)

type Index struct {
	Bucket    string
	IndexName string

	FromKey string
	ToKey   string
}

func (i *Index) FromTime(t time.Time) {
	i.FromKey = path.Join("index", i.IndexName, t.Format(time.RFC3339))
}

func (i *Index) ToTime(t time.Time) {
	i.ToKey = path.Join("index", i.IndexName, t.Format(time.RFC3339))
}

func (i *Index) Scan(ctx context.Context, client *storage.Client, limit int64, fn func(attr *storage.ObjectAttrs) error) error {
	bucket := client.Bucket(i.Bucket)
	prefix := path.Join("index", i.IndexName) + "/"

	query := &storage.Query{
		Prefix:      prefix,
		StartOffset: i.FromKey,
		EndOffset:   i.ToKey,
	}
	query.SetAttrSelection([]string{"Name", "Size", "Metadata"})
	it := bucket.Objects(ctx, query)

	for {
		attr, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if err := fn(attr); err != nil {
			return err
		}
		limit--
		if limit == 0 {
			break
		}
	}

	return nil
}

func (i *Index) EachJob(ctx context.Context, client *storage.Client, limit int64, statusURL url.URL, fn func(partialJob Job, attr *storage.ObjectAttrs) error) error {
	if err := i.Scan(ctx, client, limit, func(attr *storage.ObjectAttrs) error {
		link, ok := attr.Metadata["link"]
		if !ok {
			return nil
		}

		statusURL.Path = "/view/gcs/" + strings.TrimPrefix(link, "gs://")
		deckURL := statusURL.String()

		_, _, jobName, buildID, _, err := jobPathToAttributes(statusURL.Path, deckURL)
		if err != nil {
			klog.V(7).Infof("Unable to parse indexed link to a valid job: %s", link)
			return nil
		}

		err = fn(Job{
			Spec: JobSpec{
				Job: jobName,
			},
			Status: JobStatus{
				URL:     deckURL,
				BuildID: buildID,
			},
		}, attr)

		switch {
		case err == ErrSkip:
			return nil
		case err == ErrStop:
			return err
		case err != nil:
			return err
		}
		return nil
	}); err != nil && err != ErrStop {
		if err == ctx.Err() {
			return err
		}
	}
	return nil

}

const (
	rfc3339          = len("0000-00-00T00:00:00Z")
	rfc3339MinuteTen = len("0000-00-00T00:0")
	rfc3339Hour      = len("0000-00-00T00:")
	rfc3339Day       = len("0000-00-00T")
)

func readJobRange(ctx context.Context, bucket *storage.BucketHandle, index string, from, to time.Time, fn func(*storage.ObjectAttrs) error) error {
	start := time.Now()

	basePrefix := path.Join("index", index)
	from = from.UTC()
	to = to.UTC()

	var scans int
	fromTimestamp := from.Format(time.RFC3339)
	for {
		var searchPrefix string
		var nextFrom time.Time
		switch {
		case from.Second() != 0, from.Minute() != 0:
			if (60 - from.Minute()) < 30 {
				searchPrefix = path.Join(basePrefix, fromTimestamp[:rfc3339MinuteTen])
				nextFrom = from.Add(10 * time.Minute).Truncate(10 * time.Minute)
			}
			fallthrough
		case from.Hour() != 0:
			if (24 - from.Hour()) < 16 {
				searchPrefix = path.Join(basePrefix, fromTimestamp[:rfc3339Hour])
				nextFrom = from.Add(time.Hour).Truncate(time.Hour)
			}
			fallthrough
		default:
			searchPrefix = path.Join(basePrefix, fromTimestamp[:rfc3339Day])
			nextFrom = from.Add(24 * time.Hour).Truncate(24 * time.Hour)
		}

		var done bool
		if nextFrom.After(to) {
			nextFrom = to
			done = true
		}
		nextTimestamp := nextFrom.Format(time.RFC3339)

		scans++
		query := &storage.Query{Prefix: searchPrefix}
		query.SetAttrSelection([]string{"Name", "Size", "Metadata"})
		match, skip, stop, err := scanJobRangeIndex(bucket.Objects(ctx, query), basePrefix, fromTimestamp, nextTimestamp, fn)
		if err != nil {
			return err
		}
		if stop {
			klog.V(6).Infof("stop: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)
			break
		}
		if done {
			klog.V(6).Infof("done: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)
			break
		}
		klog.V(6).Infof("read: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)

		from = nextFrom
		fromTimestamp = nextTimestamp
	}

	klog.V(4).Infof("Index completed in %s with %d scans", time.Now().Sub(start), scans)
	return nil
}

func scanJobRangeIndex(it *storage.ObjectIterator, base, from, to string, fn func(attr *storage.ObjectAttrs) error) (count int, skip int, past bool, err error) {
	for {
		attr, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return count, skip, false, err
		}
		if len(attr.Name) < (len(base) + len(from) + 2) {
			klog.Warningf("invalid key in index range: %s", attr.Name)
			skip++
			continue
		}
		keyTimestamp := attr.Name[len(base)+1 : len(base)+1+len(from)]
		if keyTimestamp <= from {
			skip++
			continue
		}
		if keyTimestamp > to {
			return count, skip, true, nil
		}
		count++
		if err := fn(attr); err != nil {
			return count, skip, false, err
		}
	}
	return count, skip, false, nil
}
