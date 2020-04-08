package bugzilla

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

// NewBugLister lists bugs out of a cache.
func NewBugLister(indexer cache.Indexer) *BugLister {
	return &BugLister{indexer: indexer, resource: schema.GroupResource{Group: "search.openshift.io", Resource: "bugs"}}
}

type BugLister struct {
	indexer  cache.Indexer
	resource schema.GroupResource
}

func (s *BugLister) List(selector labels.Selector) (ret []*Bug, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*Bug))
	})
	return ret, err
}

func (s *BugLister) Get(id int) (*Bug, error) {
	idString := strconv.Itoa(id)
	obj, exists, err := s.indexer.GetByKey(idString)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(s.resource, idString)
	}
	return obj.(*Bug), nil
}

func NewInformer(client *Client, interval, maxInterval, resyncInterval time.Duration, argsFn func(metav1.ListOptions) SearchBugsArgs, includeFn func(*BugInfo) bool) cache.SharedIndexInformer {
	lw := &ListWatcher{
		client:      client,
		argsFn:      argsFn,
		includeFn:   includeFn,
		interval:    interval,
		maxInterval: maxInterval,
	}
	lwPager := &cache.ListWatch{ListFunc: lw.List, WatchFunc: lw.Watch}
	return cache.NewSharedIndexInformer(lwPager, &Bug{}, resyncInterval, nil)
}

type ListWatcher struct {
	client      *Client
	argsFn      func(metav1.ListOptions) SearchBugsArgs
	includeFn   func(*BugInfo) bool
	interval    time.Duration
	maxInterval time.Duration
}

func (lw *ListWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	args := lw.argsFn(options)
	if options.Limit > 0 {
		args.Limit = int(options.Limit) + 1
	}
	if len(options.Continue) > 0 {
		if offset, err := strconv.Atoi(options.Continue); err == nil && offset > 0 {
			args.Offset = offset
		}
	}
	bugs, err := lw.client.SearchBugs(context.Background(), args)
	if err != nil {
		return nil, err
	}
	list := NewBugList(bugs, lw.includeFn)
	if options.Limit > 0 {
		returned := len(bugs.Bugs)
		hasMore := returned > int(options.Limit)
		if hasMore {
			if int(options.Limit) > len(list.Items) {
				list.Items = list.Items[:int(options.Limit)]
			}
			list.Continue = strconv.Itoa(args.Offset + int(options.Limit))
		}
		klog.V(2).Infof("Listed bugs offset=%d limit=%d total=%d items=%d hasMore=%t nextOffset=%s", args.Offset, options.Limit, returned, len(list.Items), hasMore, list.Continue)
	} else {
		klog.V(2).Infof("Listed bugs offset=%d limit=%d total=%d items=%d", args.Offset, options.Limit, len(bugs.Bugs), len(list.Items))
	}
	return list, nil
}

func (lw *ListWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	var rv metav1.Time
	if err := rv.UnmarshalQueryParameter(options.ResourceVersion); err != nil {
		return nil, err
	}
	return newPeriodicWatcher(lw, lw.interval, lw.maxInterval, rv, lw.argsFn(options), lw.includeFn), nil
}

type periodicWatcher struct {
	lw          *ListWatcher
	ch          chan watch.Event
	interval    time.Duration
	maxInterval time.Duration
	rv          metav1.Time
	args        SearchBugsArgs
	includeFn   func(*BugInfo) bool

	lock   sync.Mutex
	done   chan struct{}
	closed bool
}

func newPeriodicWatcher(lw *ListWatcher, interval, maxInterval time.Duration, rv metav1.Time, args SearchBugsArgs, includeFn func(*BugInfo) bool) *periodicWatcher {
	pw := &periodicWatcher{
		lw:          lw,
		interval:    interval,
		maxInterval: maxInterval,
		rv:          rv,
		args:        args,
		ch:          make(chan watch.Event, 100),
		done:        make(chan struct{}),
	}
	go pw.run()
	return pw
}

func (w *periodicWatcher) run() {
	defer klog.V(7).Infof("Watcher exited")
	defer close(w.ch)

	// never watch longer than maxInterval
	if w.maxInterval > 0 {
		stop := time.After(w.maxInterval)
		go func() {
			select {
			case <-stop:
				klog.V(5).Infof("maximum duration reached %s", w.maxInterval)
				w.ch <- watch.Event{Type: watch.Error, Object: &errors.NewResourceExpired(fmt.Sprintf("watch closed after %s, resync required", w.maxInterval)).ErrStatus}
				w.stop()
			case <-w.done:
			}
		}()
	}

	// a watch starts on the next visible change (which is a single second of precision for these queries)
	rv := metav1.Time{Time: w.rv.Truncate(time.Second).Add(time.Second)}

	var delay time.Duration
	now := time.Now()
	if d := rv.Time.Add(w.interval).Sub(now); d > 0 {
		delay = d
	} else {
		delay = w.interval
	}
	klog.V(5).Infof("Waiting for minimum interval %s", delay)
	select {
	case <-time.After(delay):
	case <-w.done:
		return
	}

	wait.Until(func() {
		args := w.args
		args.LastChangeTime = rv.Time
		bugs, err := w.lw.client.SearchBugs(context.Background(), args)
		if err != nil {
			klog.V(5).Infof("Search query error: %v", err)
			w.ch <- watch.Event{Type: watch.Error, Object: &errors.NewInternalError(err).ErrStatus}
			w.stop()
			return
		}
		if len(bugs.Bugs) == 0 {
			return
		}

		list := NewBugList(bugs, w.includeFn)
		var nextRV metav1.Time
		if err := nextRV.UnmarshalQueryParameter(list.ResourceVersion); err != nil {
			klog.Errorf("Unable to parse resource version for informer: %s: %v", list.ResourceVersion, err)
			return
		}
		if !nextRV.Time.After(rv.Time) {
			klog.Errorf("The resource version for the current query %q is not after %q", nextRV.String(), rv.String())
			return
		}

		klog.V(5).Infof("Watch observed %d bugs with a change time since %s", len(list.Items), timeToRV(rv))

		// sort the list from oldest change to newest change
		sort.Slice(list.Items, func(i, j int) bool {
			a, b := list.Items[i].Info.LastChangeTime.Time, list.Items[j].Info.LastChangeTime.Time
			if a.After(b) {
				return false
			}
			return true
		})
		for i := range list.Items {
			eventType := watch.Modified
			if !list.Items[i].CreationTimestamp.Time.Before(rv.Time) {
				eventType = watch.Added
			}
			if list.Items[i].Info.LastChangeTime.Time.Before(rv.Time) {
				continue
			}
			w.ch <- watch.Event{Type: eventType, Object: &list.Items[i]}
		}
		rv = nextRV
	}, w.interval, w.done)
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
