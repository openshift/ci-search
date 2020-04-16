package prow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

var prowGR = schema.GroupResource{Group: "search.openshift.io", Resource: "prow"}

// NewLister lists jobs out of a cache.
func NewLister(indexer cache.Indexer) *Lister {
	if err := indexer.AddIndexers(cache.Indexers{
		"by-job": func(obj interface{}) ([]string, error) {
			job, ok := obj.(*Job)
			if !ok {
				return nil, nil
			}
			return []string{job.Spec.Job}, nil
		},
	}); err != nil {
		panic(err)
	}
	return &Lister{indexer: indexer, resource: prowGR}
}

type Lister struct {
	indexer  cache.Indexer
	resource schema.GroupResource
}

func (s *Lister) List(selector labels.Selector) (ret []*Job, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*Job))
	})
	return ret, err
}

func (s *Lister) Get(name string) (*Job, error) {
	obj, exists, err := s.indexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(s.resource, name)
	}
	return obj.(*Job), nil
}

func (s *Lister) JobStats(name string, names sets.String, from, to time.Time) JobStats {
	var stats JobStats

	if len(name) == 0 {
		jobs := make(map[string]struct{}, 500)
		for _, obj := range s.indexer.List() {
			job, ok := obj.(*Job)
			if !ok {
				continue
			}
			if names != nil && !names.Has(job.Spec.Job) {
				continue
			}
			if job.Status.CompletionTime.Time.Before(from) || job.Status.CompletionTime.Time.After(to) {
				continue
			}
			jobs[job.Spec.Job] = struct{}{}
			stats.Count++
			if job.Status.State == "success" || job.Status.State == "aborted" {
				continue
			}
			stats.Failures++
		}
		stats.Jobs = len(jobs)
		return stats
	}

	arr, err := s.indexer.ByIndex("by-job", name)
	if err != nil {
		panic(err)
	}
	for _, obj := range arr {
		job, ok := obj.(*Job)
		if !ok {
			continue
		}
		if job.Status.CompletionTime.Time.Before(from) || job.Status.CompletionTime.Time.After(to) {
			continue
		}
		stats.Count++
		if job.Status.State == "success" || job.Status.State == "aborted" {
			continue
		}
		stats.Failures++
	}
	if stats.Count > 0 {
		stats.Jobs = 1
	}
	return stats
}

type JobLister interface {
	ListJobs(ctx context.Context) ([]*Job, error)
}

func NewInformer(interval, resyncInterval time.Duration, listers ...JobLister) cache.SharedIndexInformer {
	lw := &ListWatcher{
		interval: interval,
		listers:  listers,
	}
	lwPager := &cache.ListWatch{ListFunc: lw.List, WatchFunc: lw.Watch}
	return cache.NewSharedIndexInformer(lwPager, &Job{}, resyncInterval, cache.Indexers{})
}

type ListWatcher struct {
	client   *Client
	interval time.Duration
	listers  []JobLister
}

func mergeJobs(lists [][]*Job) *JobList {
	if len(lists) == 1 {
		list := make([]*Job, len(lists[0]))
		copy(list, lists[0])
		return &JobList{Items: list}
	}

	size := 0
	for _, list := range lists {
		size += len(list)
	}
	keys := make(map[types.NamespacedName]struct{}, size)
	var jobList JobList
	jobList.Items = make([]*Job, 0, size)
	for _, list := range lists {
		for _, job := range list {
			key := types.NamespacedName{Namespace: job.Namespace, Name: job.Name}
			if _, ok := keys[key]; ok {
				continue
			}
			keys[key] = struct{}{}
			jobList.Items = append(jobList.Items, job)
		}
	}
	return &jobList
}

func (lw *ListWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	ctx := context.Background()
	lists := make([][]*Job, 0, len(lw.listers))
	for _, lister := range lw.listers {
		list, err := lister.ListJobs(ctx)
		if err != nil {
			return nil, err
		}
		lists = append(lists, list)
	}
	merged := mergeJobs(lists)
	klog.V(4).Infof("Merged into %d results", len(merged.Items))
	return merged, nil
}

func (lw *ListWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	var rv metav1.Time
	if err := rv.UnmarshalQueryParameter(options.ResourceVersion); err != nil {
		return nil, err
	}
	return newPeriodicWatcher(lw, lw.interval, rv), nil
}

type periodicWatcher struct {
	lw       *ListWatcher
	ch       chan watch.Event
	interval time.Duration
	rv       metav1.Time

	lock   sync.Mutex
	done   chan struct{}
	closed bool
}

func newPeriodicWatcher(lw *ListWatcher, interval time.Duration, rv metav1.Time) *periodicWatcher {
	pw := &periodicWatcher{
		lw:       lw,
		interval: interval,
		rv:       rv,
		ch:       make(chan watch.Event, 100),
		done:     make(chan struct{}),
	}
	go pw.run()
	return pw
}

func (w *periodicWatcher) run() {
	defer klog.V(7).Infof("Watcher exited")
	defer close(w.ch)

	// never watch longer than maxInterval
	stop := time.After(w.interval)
	select {
	case <-stop:
		klog.V(4).Infof("Maximum duration reached %s", w.interval)
		w.ch <- watch.Event{Type: watch.Error, Object: &errors.NewResourceExpired(fmt.Sprintf("watch closed after %s, resync required", w.interval)).ErrStatus}
		w.stop()
	case <-w.done:
	}
}

func (w *periodicWatcher) Stop() {
	defer func() {
		// drain the channel if stop was invoked until the channel is closed
		for range w.ch {
		}
	}()
	w.stop()
	klog.V(7).Infof("Stopped watch")
}

func (w *periodicWatcher) stop() {
	klog.V(7).Infof("Stopping watch")
	w.lock.Lock()
	defer w.lock.Unlock()
	if !w.closed {
		close(w.done)
		w.closed = true
	}
}

func (w *periodicWatcher) ResultChan() <-chan watch.Event {
	return w.ch
}
