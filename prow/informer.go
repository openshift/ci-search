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
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

var prowGR = schema.GroupResource{Group: "search.openshift.io", Resource: "prow"}

// NewLister lists jobs out of a cache.
func NewLister(indexer cache.Indexer) *Lister {
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

func NewInformer(client *Client, interval, resyncInterval time.Duration) cache.SharedIndexInformer {
	lw := &ListWatcher{
		client:   client,
		interval: interval,
	}
	lwPager := &cache.ListWatch{ListFunc: lw.List, WatchFunc: lw.Watch}
	return cache.NewSharedIndexInformer(lwPager, &Job{}, resyncInterval, nil)
}

type ListWatcher struct {
	client   *Client
	interval time.Duration
}

func (lw *ListWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	list, err := lw.client.ListJobs(context.Background())
	if err != nil {
		return nil, err
	}
	return list, nil
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
	defer klog.V(4).Infof("Watcher exited")
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
	klog.V(4).Infof("Stopped watch")
}

func (w *periodicWatcher) stop() {
	klog.V(4).Infof("Stopping watch")
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
