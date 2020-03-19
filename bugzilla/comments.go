package bugzilla

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

type CommentStore struct {
	store          cache.Store
	hasSynced      []cache.InformerSynced
	client         *Client
	includePrivate bool

	queue workqueue.Interface

	refreshInterval time.Duration
	maxBatch        int
	rateLimit       *rate.Limiter

	// lock keeps the comment list in sync with the bug list
	lock sync.Mutex
}

type PersistentCommentStore interface {
	Sync(keys []string) ([]*BugComments, error)
	NotifyChanged(id int)
}

func NewCommentStore(client *Client, refreshInterval time.Duration, includePrivate bool) *CommentStore {
	s := &CommentStore{
		store:  cache.NewStore(cache.MetaNamespaceKeyFunc),
		client: client,

		includePrivate: includePrivate,

		queue: workqueue.NewNamed("comment_store"),

		refreshInterval: refreshInterval,
		rateLimit:       rate.NewLimiter(rate.Every(refreshInterval/3), 3),
		maxBatch:        100,
	}
	return s
}

type Stats struct {
	Bugs int
}

func (s *CommentStore) Stats() Stats {
	return Stats{
		Bugs: len(s.store.ListKeys()),
	}
}

func (s *CommentStore) Get(id int) (*BugComments, bool) {
	item, ok, err := s.store.GetByKey(strconv.Itoa(id))
	if err != nil || !ok {
		return nil, false
	}
	return item.(*BugComments), true
}

func (s *CommentStore) Run(ctx context.Context, informer cache.SharedInformer, persisted PersistentCommentStore) error {
	if s.refreshInterval == 0 {
		return nil
	}
	done := ctx.Done()
	if persisted != nil {
		// load the full state into the store
		list, err := persisted.Sync(nil)
		if err != nil {
			klog.Errorf("Unable to load initial comment state: %v", err)
		}
		for _, bug := range list {
			s.store.Add(bug.DeepCopyObject())
		}
		klog.V(4).Infof("Loaded %d bugs from disk", len(list))

		// wait for bug cache to fill, then prune the list
		if !cache.WaitForCacheSync(done, informer.HasSynced) {
			return ctx.Err()
		}
		list, err = persisted.Sync(informer.GetStore().ListKeys())
		if err != nil {
			klog.Errorf("Unable to load initial comment state: %v", err)
		}
		klog.V(4).Infof("Prune disk to %d bugs", len(list))
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    s.bugAdd,
		DeleteFunc: s.bugDelete,
		UpdateFunc: func(_, new interface{}) { s.bugUpdate(new, persisted) },
	})

	klog.V(5).Infof("Running comment store")

	// periodically put all bugs that haven't been refreshed in the last interval
	// into the queue
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		now := time.Now()
		refreshAfter := now.Add(-s.refreshInterval)
		var count int
		for _, obj := range s.store.List() {
			comments := obj.(*BugComments)
			if comments.RefreshTime.Before(refreshAfter) {
				s.queue.Add(comments.Name)
				count++
			}
		}
		klog.V(5).Infof("Refreshed %d comments older than %s", count, s.refreshInterval.String())
	}, s.refreshInterval/4)

	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := s.run(ctx, persisted); err != nil {
			klog.Errorf("Error syncing comments: %v", err)
		}
	}, time.Second)

	return ctx.Err()
}

func (s *CommentStore) run(ctx context.Context, persisted PersistentCommentStore) error {
	done := ctx.Done()
	for {
		l := s.queue.Len()
		if l == 0 {
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-done:
				return nil
			}
		}
		if err := s.rateLimit.Wait(ctx); err != nil {
			return err
		}

		if l > s.maxBatch {
			l = s.maxBatch
		}

		var bugIDs []int
		bugIDs = make([]int, 0, l)
		for l > 0 {
			k, done := s.queue.Get()
			if done {
				return ctx.Err()
			}
			id, err := strconv.Atoi(k.(string))
			if err != nil {
				klog.Warningf("comment id was not parsable to int: %v", err)
				continue
			}
			bugIDs = append(bugIDs, id)
			l--
		}

		now := time.Now()
		klog.V(5).Infof("Fetching %d comments", len(bugIDs))
		bugComments, err := s.client.BugCommentsByID(ctx, bugIDs...)
		if err != nil {
			klog.Warningf("comment store failed to retrieve comments: %v", err)
			continue
		}
		s.filterComments(bugComments)
		s.mergeBugs(bugComments, now, persisted)
	}
}

func (s *CommentStore) filterComments(bugComments *BugCommentsList) {
	if s.includePrivate {
		return
	}
	for id, comments := range bugComments.Bugs {
		if len(comments.Comments) == 0 {
			continue
		}
		copied := make([]BugComment, 0, len(comments.Comments))
		for _, comment := range comments.Comments {
			if comment.IsPrivate {
				continue
			}
			copied = append(copied, comment)
		}
		if len(copied) == len(comments.Comments) {
			continue
		}
		if len(copied) == 0 {
			comment := comments.Comments[0]
			comment.Text = "<private comment>"
			comment.Creator = "Unknown"
			comment.IsPrivate = false
			copied = append(copied, comment)
		}
		klog.V(7).Infof("Filtered %d/%d private comments from bug %d", len(comments.Comments)-len(copied), len(comments.Comments), id)
		comments.Comments = copied
		bugComments.Bugs[id] = comments
	}
}

func (s *CommentStore) mergeBugs(bugComments *BugCommentsList, now time.Time, persisted PersistentCommentStore) {
	var total int
	defer func() { klog.V(5).Infof("Updated %d comment records", total) }()
	s.lock.Lock()
	defer s.lock.Unlock()

	for id, comments := range bugComments.Bugs {
		obj, ok, err := s.store.GetByKey(strconv.Itoa(int(id)))
		if !ok || err != nil {
			klog.V(5).Infof("Bug %d is not in cache", id)
			continue
		}
		existing := obj.(*BugComments)
		if existing.RefreshTime.After(now) {
			klog.V(5).Infof("Bug refresh time is in the future: %v >= %v", existing, now)
			continue
		}

		updated := NewBugComments(int(id), &comments)
		updated.Info = existing.Info
		updated.RefreshTime = now
		s.store.Update(updated)
		if persisted != nil {
			persisted.NotifyChanged(int(id))
		}
		total++
	}
}

func (s *CommentStore) bugAdd(obj interface{}) {
	bug, ok := obj.(*Bug)
	if !ok {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	obj, ok, err := s.store.GetByKey(bug.Name)
	if err != nil {
		klog.Errorf("Unexpected error retrieving %q from store: %v", bug.Name, err)
	}
	if ok {
		existing := obj.(*BugComments).DeepCopyObject().(*BugComments)
		existing.Info = bug.Info
		if err := s.store.Update(existing); err != nil {
			klog.Errorf("Unable to merge added bug from informer: %v", err)
			return
		}
	} else {
		if err := s.store.Add(&BugComments{ObjectMeta: metav1.ObjectMeta{Name: bug.Name}}); err != nil {
			klog.Errorf("Unable to add bug from informer: %v", err)
			return
		}
	}
	s.queue.Add(bug.Name)
}

func (s *CommentStore) bugUpdate(obj interface{}, persisted PersistentCommentStore) {
	bug, ok := obj.(*Bug)
	if !ok {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()

	existing, ok := s.Get(bug.Info.ID)
	if !ok {
		return
	}
	if reflect.DeepEqual(bug.Info, existing.Info) {
		return
	}
	existing = existing.DeepCopyObject().(*BugComments)
	existing.Info = bug.Info
	if err := s.store.Update(existing); err != nil {
		klog.Errorf("Unable to update bug from informer: %v", err)
		return
	}
	if persisted != nil {
		persisted.NotifyChanged(bug.Info.ID)
	}
}

func (s *CommentStore) bugDelete(obj interface{}) {
	var name string
	switch t := obj.(type) {
	case cache.DeletedFinalStateUnknown:
		name = t.Key
	case *Bug:
		name = t.Name
	default:
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.store.Delete(&Bug{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		klog.Errorf("Unable to delete bug from informer: %v", err)
		return
	}
	s.queue.Add(name)
}
