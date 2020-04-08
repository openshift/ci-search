package prow

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/time/rate"
	"google.golang.org/api/iterator"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

type IndexReader struct {
	client      *storage.Client
	bucket      string
	index       string
	maxAge      time.Duration
	statusURL   url.URL
	rateLimiter *rate.Limiter

	lock  sync.Mutex
	items map[string]*Job
}

func NewIndexReader(client *storage.Client, bucket, index string, maxAge time.Duration, statusURL url.URL) *IndexReader {
	return &IndexReader{
		client:      client,
		bucket:      bucket,
		index:       index,
		maxAge:      maxAge,
		statusURL:   statusURL,
		rateLimiter: rate.NewLimiter(rate.Every(30*time.Second), 0),

		items: make(map[string]*Job),
	}
}

func (r *IndexReader) Get(name string) (*Job, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	job, ok := r.items[name]
	if !ok {
		return nil, errors.NewNotFound(prowGR, name)
	}
	return job, nil
}

func (r *IndexReader) List(labels.Selector) ([]*Job, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	copied := make([]*Job, 0, len(r.items))
	for _, job := range r.items {
		copied = append(copied, job)
	}
	return copied, nil
}

func (r *IndexReader) add(job *Job) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.items[fmt.Sprintf("%s/%s", job.Namespace, job.Name)] = job
}

func (r *IndexReader) Run(ctx context.Context, handler cache.ResourceEventHandler) error {
	var index int
	statusURL := r.statusURL
	end := time.Now()
	for {
		start := end.Add(-24 * time.Hour)
		if err := readJobRange(ctx, r.client.Bucket(r.bucket), r.index, start, end, func(attr *storage.ObjectAttrs) error {
			link, ok := attr.Metadata["link"]
			if !ok {
				return nil
			}
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
			statusURL.Path = "/view/gcs/" + strings.TrimPrefix(link, "gs://")
			deckURL := statusURL.String()

			_, _, jobName, buildID, _, err := jobPathToAttributes(statusURL.Path, deckURL)
			if err != nil {

			}

			job := &Job{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "",
					Name:      fmt.Sprintf("gcs-%d", index),
				},
				Spec: JobSpec{
					Job: jobName,
				},
				Status: JobStatus{
					State:          state,
					URL:            deckURL,
					CompletionTime: metav1.Time{time.Unix(completed, 0)},
					BuildID:        buildID,
				},
			}
			r.add(job)
			handler.OnAdd(job)

			index++
			return nil
		}); err != nil {
			if err == ctx.Err() {
				return err
			}
			klog.Errorf("scan failed, will retry: %v", err)
		} else {

			end = start
			if r.maxAge > 0 && time.Now().Add(-r.maxAge).After(end) {
				break
			}
		}
		r.rateLimiter.Wait(ctx)
	}
	klog.Infof("Completed scan to max age")
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
			klog.Infof("stop: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)
			break
		}
		if done {
			klog.Infof("done: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)
			break
		}
		klog.Infof("read: match=%d skip=%d prefix=%s (%s,%s]", match, skip, searchPrefix, fromTimestamp, nextTimestamp)

		from = nextFrom
		fromTimestamp = nextTimestamp
	}

	klog.Infof("completed in %s with %d scans", time.Now().Sub(start), scans)
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
