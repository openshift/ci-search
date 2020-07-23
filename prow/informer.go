package prow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

var (
	metricJobStats = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "prow_stats",
		Help: "The current counts of completed runs, failed runs, and jobs.",
	}, []string{"type"})
)

func init() {
	prometheus.MustRegister(
		metricJobStats,
	)
}

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
	return &Lister{indexer: indexer}
}

type Lister struct {
	indexer cache.Indexer
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
		return nil, errors.NewNotFound(prowGR, name)
	}
	return obj.(*Job), nil
}

func (s *Lister) JobStats(name string, names sets.String, from, to time.Time) JobStats {
	var stats JobStats
	hasNameScope := names.Len() > 0

	if len(name) == 0 {
		jobs := make(map[string]struct{}, 500)
		for _, obj := range s.indexer.List() {
			job, ok := obj.(*Job)
			if !ok {
				continue
			}
			if hasNameScope && !names.Has(job.Spec.Job) {
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

		if !hasNameScope {
			metricJobStats.WithLabelValues("completed_runs").Set(float64(stats.Count))
			metricJobStats.WithLabelValues("failed_runs").Set(float64(stats.Failures))
			metricJobStats.WithLabelValues("jobs").Set(float64(stats.Jobs))
		}
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

var metricJobSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "informer_list_jobs",
	Help: "Prints the current number of jobs known to a jobs informer.",
}, []string{"name", "type"})

func init() {
	prometheus.MustRegister(metricJobSize)
}

func NewInformer(interval, resyncInterval, maxAge time.Duration, initialLister JobLister, listers ...JobLister) cache.SharedIndexInformer {
	lw := &ListWatcher{
		interval:      interval,
		maxAge:        maxAge,
		listers:       listers,
		initialLister: initialLister,
	}
	lw.name = fmt.Sprintf("%p", lw)
	lwPager := &cache.ListWatch{ListFunc: lw.List, WatchFunc: lw.Watch}
	return cache.NewSharedIndexInformer(lwPager, &Job{}, resyncInterval, cache.Indexers{})
}

type ListWatcher struct {
	client        *Client
	interval      time.Duration
	maxAge        time.Duration
	listers       []JobLister
	initialLister JobLister
	name          string

	lock     sync.Mutex
	lastList []*Job
}

func jobExpired(job *Job, expires time.Time) bool {
	if job.Status.CompletionTime.IsZero() {
		if job.CreationTimestamp.Time.Before(expires) {
			return true
		}
		return false
	}
	return job.Status.CompletionTime.Time.Before(expires)
}

func mergeJobs(lists [][]*Job, expires time.Time) (list *JobList, expiredCount int, emptyCount int) {
	size := 0
	for _, list := range lists {
		size += len(list)
	}
	keys := make(map[types.NamespacedName]struct{}, size)
	var jobList JobList
	jobList.Items = make([]*Job, 0, size)
	for _, list := range lists {
		for _, job := range list {
			if jobExpired(job, expires) {
				expiredCount++
				continue
			}
			if len(job.Spec.Job) == 0 || len(job.Status.BuildID) == 0 {
				emptyCount++
				continue
			}
			key := types.NamespacedName{Namespace: job.Spec.Job, Name: job.Status.BuildID}
			if _, ok := keys[key]; ok {
				continue
			}
			keys[key] = struct{}{}
			jobList.Items = append(jobList.Items, job)
		}
	}
	return &jobList, expiredCount, emptyCount
}

func (lw *ListWatcher) mostRecentJobs() ([]*Job, error) {
	lw.lock.Lock()
	list := lw.lastList
	lw.lock.Unlock()
	if list != nil || lw.initialLister == nil {
		return list, nil
	}

	ctx := context.Background()
	list, err := lw.initialLister.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	metricJobSize.WithLabelValues(lw.name, "initial").Set(float64(len(list)))

	lw.lock.Lock()
	defer lw.lock.Unlock()
	lw.lastList = list
	return list, nil
}

func (lw *ListWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	// keep items until they are expired by using the previous result as the seed for
	// the next result
	initial, err := lw.mostRecentJobs()
	if err != nil {
		return nil, err
	}

	// calculate the merged list from all current sources, including our last state at
	// the end (so we get the newer copy from the listers, and backfill any jobs that
	// are no longer live with their last visible state)
	ctx := context.Background()
	lists := make([][]*Job, 0, len(lw.listers)+1)
	for _, lister := range lw.listers {
		list, err := lister.ListJobs(ctx)
		if err != nil {
			return nil, err
		}
		lists = append(lists, list)
	}
	lists = append(lists, initial)

	expires := time.Now().Add(-lw.maxAge)
	merged, expired, empty := mergeJobs(lists, expires)

	metricJobSize.WithLabelValues(lw.name, "expired").Set(float64(expired))
	metricJobSize.WithLabelValues(lw.name, "empty").Set(float64(empty))
	metricJobSize.WithLabelValues(lw.name, "current").Set(float64(len(merged.Items)))

	// remember our most recent list so that jobs that "age out" of the live view
	// of prow jobs are still viewed by the system
	lw.lock.Lock()
	defer lw.lock.Unlock()
	lw.lastList = append(lw.lastList[:0], merged.Items...)
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
