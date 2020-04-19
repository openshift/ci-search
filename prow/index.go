package prow

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

type DiskStore struct {
	base   string
	maxAge time.Duration
	queue  workqueue.RateLimitingInterface
	client *storage.Client
}

func NewDiskStore(client *storage.Client, path string, maxAge time.Duration) *DiskStore {
	rate := workqueue.NewItemExponentialFailureRateLimiter(time.Minute, 30*time.Minute)
	queue := workqueue.NewRateLimitingQueue(rate)
	return &DiskStore{
		base:   path,
		maxAge: maxAge,
		queue:  queue,
		client: client,
	}
}

type JobStats struct {
	Jobs     int
	Count    int
	Failures int
}

type PathNotifier interface {
	Notify(paths []string)
}

func (s *DiskStore) Handler() cache.ResourceEventHandler {
	return cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			job, ok := obj.(*Job)
			if !ok {
				return false
			}
			switch job.Status.State {
			case "aborted", "error", "failure", "success":
				if len(job.Status.URL) == 0 || job.Status.CompletionTime.IsZero() {
					return false
				}
				if s.maxAge > 0 && job.Status.CompletionTime.Time.Add(s.maxAge).Before(time.Now()) {
					return false
				}
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				job, ok := obj.(*Job)
				if !ok {
					return
				}
				key, _ := cache.MetaNamespaceKeyFunc(job)
				s.notifyChanged(key)
			},
			UpdateFunc: func(_, obj interface{}) {
				job, ok := obj.(*Job)
				if !ok {
					return
				}
				key, _ := cache.MetaNamespaceKeyFunc(job)
				s.notifyChanged(key)
			},
		},
	}
}

func (s *DiskStore) Run(ctx context.Context, accessor JobAccessor, notifier PathNotifier, disableWrite bool, workers int) {
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer klog.V(2).Infof("Prow disk worker %d exited", i)
			wait.UntilWithContext(ctx, func(ctx context.Context) {
				for {
					obj, done := s.queue.Get()
					if done {
						return
					}
					if disableWrite {
						s.queue.Forget(obj)
						s.queue.Done(obj)
						return
					}
					id, ok := obj.(string)
					if !ok {
						s.queue.Done(id)
						klog.Errorf("unexpected id in queue: %v", obj)
						continue
					}
					job, err := accessor.Get(id)
					if err != nil {
						s.queue.Done(id)
						klog.V(5).Infof("No job for %s: %v", id, err)
						continue
					}
					ctx, cancelFn := context.WithTimeout(ctx, time.Minute)
					func() {
						defer cancelFn()
						paths, err := s.write(ctx, job, notifier)
						if err != nil {
							if s.queue.NumRequeues(obj) > 5 {
								s.queue.Forget(obj)
							} else {
								s.queue.AddRateLimited(obj)
							}
							s.queue.Done(obj)
							klog.Errorf("failed to write job: %v", err)
							return
						}
						notifier.Notify(paths)
						s.queue.Done(id)
					}()
				}
			}, time.Second)
		}(i)
	}
	<-ctx.Done()
}

func (s *DiskStore) write(ctx context.Context, job *Job, notifier PathNotifier) ([]string, error) {
	if job.Status.State == "error" && job.Status.URL == "https://github.com/kubernetes/test-infra/issues" {
		return nil, nil
	}
	u, err := url.Parse(job.Status.URL)
	if err != nil {
		return nil, fmt.Errorf("job %s has no valid status URL: %v", job.Name, err)
	}

	bucket, _, _, _, parts, err := jobPathToAttributes(u.Path, job.Status.URL)
	if err != nil {
		return nil, err
	}
	if len(bucket) == 0 {
		return nil, nil
	}

	build := Build{
		Bucket:     s.client.Bucket(bucket),
		Context:    ctx,
		BucketPath: bucket,
		Prefix:     path.Join(parts...) + "/",
	}
	start := time.Now()
	accumulator, stale := NewAccumulator(s.base, &build, job.Status.CompletionTime.Time)
	if !stale {
		klog.V(7).Infof("Job %s is up to date", job.Status.URL)
		return nil, nil
	}
	if err := ReadBuild(build, accumulator); err != nil {
		klog.Infof("Download %s failed in %s: %v", job.Status.URL, time.Now().Sub(start).Truncate(time.Millisecond), err)
		return nil, err
	}
	if err := accumulator.MarkCompleted(job.Status.CompletionTime.Time); err != nil {
		klog.Errorf("Unable to mark job as completed: %v", err)
	}
	klog.V(2).Infof("Download %s succeeded in %s", job.Status.URL, time.Now().Sub(start).Truncate(time.Millisecond))
	return nil, nil
}

func (s *DiskStore) pathForJob(job *Job) string {
	return filepath.Join(s.base, job.Spec.Job, job.Status.BuildID)
}

func (s *DiskStore) notifyChanged(id string) {
	s.queue.Add(id)
}

func (s *DiskStore) Sync() error {
	start := time.Now()
	mustExpire := s.maxAge != 0
	expiredAt := start.Add(-s.maxAge)

	return filepath.Walk(s.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}

		if mustExpire && expiredAt.After(info.ModTime()) {
			os.Remove(path)
			klog.V(5).Infof("File expired: %s", path)
			return nil
		}
		return nil
	})
}

func jobPathToAttributes(path, full string) (bucket, trigger, job, buildID string, parts []string, err error) {
	parts = strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 5 {
		return
	}
	if parts[0] != "view" || parts[1] != "gcs" {
		return
	}

	bucket = parts[2]
	trigger = parts[3]
	switch trigger {
	case "logs":
		parts = parts[3:]

		switch {
		case len(parts) == 3:
		case len(parts) == 4:
		default:
			err = fmt.Errorf("unrecognized logs path %q in %s", strings.Join(parts, "/"), full)
			return
		}

		job = parts[len(parts)-2]
		buildID = parts[len(parts)-1]
		if _, pErr := strconv.Atoi(buildID); pErr != nil {
			err = fmt.Errorf("unrecognized logs path %q in %s", strings.Join(parts, "/"), full)
			return
		}
		return

	case "pr-logs":
		parts = parts[3:]

		switch {
		case len(parts) == 6 && parts[1] == "pull":
		case len(parts) == 5 && parts[1] == "pull" && parts[2] == "batch":
		case len(parts) == 5 && parts[1] == "pull":
			if _, pErr := strconv.Atoi(parts[2]); pErr != nil {
				err = fmt.Errorf("unrecognized pr-logs path %q in %s", strings.Join(parts, "/"), full)
				return
			}
		default:
			err = fmt.Errorf("unrecognized pr-logs path %q in %s", strings.Join(parts, "/"), full)
			return
		}

		job = parts[len(parts)-2]
		buildID = parts[len(parts)-1]
		if _, pErr := strconv.Atoi(buildID); pErr != nil {
			err = fmt.Errorf("unrecognized logs path %q in %s", strings.Join(parts, "/"), full)
			return
		}
		return

	default:
		err = fmt.Errorf("unrecognized job prefix type %q in %s", parts[3], full)
		return
	}
}
