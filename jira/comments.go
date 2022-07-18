package jira

import (
	"context"
	"k8s.io/klog"
	"reflect"
	"strconv"
	"sync"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type CommentStore struct {
	store          cache.Store
	persistedStore PersistentCommentStore
	hasSynced      []cache.InformerSynced
	client         *Client
	includePrivate bool

	queue workqueue.Interface

	refreshInterval time.Duration
	maxBatch        int
	rateLimit       *rate.Limiter

	// lock keeps the comment list in sync with the issue list
	lock sync.Mutex
}

type PersistentCommentStore interface {
	Sync(keys []string) ([]*IssueComments, error)
	NotifyChanged(id int)
	DeleteIssue(*Issue) error
	CloseIssue(*IssueComments) error
}

func NewCommentStore(client *Client, refreshInterval time.Duration, persisted PersistentCommentStore) *CommentStore {
	s := &CommentStore{
		store:           cache.NewStore(cache.MetaNamespaceKeyFunc),
		persistedStore:  persisted,
		client:          client,
		queue:           workqueue.NewNamed("comment_store_jira"),
		refreshInterval: refreshInterval,
		rateLimit:       rate.NewLimiter(rate.Every(15*time.Second), 3),
		maxBatch:        250,
	}
	return s
}

type Stats struct {
	Issues int
}

func (s *CommentStore) Stats() Stats {
	return Stats{
		Issues: len(s.store.ListKeys()),
	}
}

func (s *CommentStore) Get(id int) (*IssueComments, bool) {
	item, ok, err := s.store.GetByKey(strconv.Itoa(id))
	if err != nil || !ok {
		return nil, false
	}
	return item.(*IssueComments), true
}

func (s *CommentStore) Run(ctx context.Context, informer cache.SharedInformer) error {
	defer klog.V(2).Infof("Comment worker exited")
	if s.refreshInterval == 0 {
		return nil
	}
	if s.persistedStore != nil {
		// load the full state into the store
		list, err := s.persistedStore.Sync(nil)
		if err != nil {
			klog.Errorf("Unable to load initial comment state: %v", err)
		}
		for _, issue := range list {
			s.store.Add(issue.DeepCopyObject())
		}
		klog.V(4).Infof("Loaded %d issues from disk", len(list))
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    s.issueAdd,
		DeleteFunc: s.issueDelete,
		UpdateFunc: func(_, new interface{}) { s.issueUpdate(new) },
	})

	klog.V(5).Infof("Running comment store")

	// periodically put all bugs that haven't been refreshed in the last interval
	// into the queue
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		now := time.Now()
		refreshAfter := now.Add(-s.refreshInterval)
		var count int
		for _, obj := range s.store.List() {
			comments := obj.(*IssueComments)
			if comments.RefreshTime.Before(refreshAfter) {
				s.queue.Add(comments.Name)
				count++
			}
		}
		klog.V(5).Infof("Refreshed %d comments older than %s", count, s.refreshInterval.String())
	}, s.refreshInterval/4)

	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := s.run(ctx); err != nil {
			klog.Errorf("Error syncing comments: %v", err)
		}
	}, time.Second)

	return ctx.Err()
}

func (s *CommentStore) run(ctx context.Context) error {
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

		var issueIDs []int
		issueIDs = make([]int, 0, l)
		for l > 0 {
			k, done := s.queue.Get()
			if done {
				return ctx.Err()
			}
			s.queue.Done(k)
			id, err := strconv.Atoi(k.(string))
			if err != nil {
				klog.Warningf("comment id %q was not parsable to int: %v", k.(string), err)
				continue
			}
			issueIDs = append(issueIDs, id)
			l--
		}

		now := time.Now()
		klog.V(7).Infof("Fetching %d comments", len(issueIDs))
		issueComments, err := s.client.IssueCommentsByID(ctx, issueIDs...)
		if err != nil {
			klog.Warningf("comment store failed to retrieve comments: %v", err)
		}
		s.filterComments(&issueComments)
		s.mergeIssues(&issueComments, now)
	}
}

func (s *CommentStore) filterComments(issueComments *[]jiraBaseClient.Issue) {
	if s.includePrivate {
		return
	}
	for _, issue := range *issueComments {
		var filteredCommentList []*jiraBaseClient.Comment
		for _, comment := range issue.Fields.Comments.Comments {
			if comment.Visibility.Value == "" {
				filteredCommentList = append(filteredCommentList, comment)
			} else {
				filteredCommentList = append(filteredCommentList, &jiraBaseClient.Comment{Body: "<private comment>",
					Author:  jiraBaseClient.User{DisplayName: "UNKNOWN"},
					Created: comment.Created,
					Updated: comment.Updated,
					ID:      comment.ID,
				})
			}
		}
		issue.Fields.Comments.Comments = filteredCommentList
	}
}

func (s *CommentStore) mergeIssues(issueComments *[]jiraBaseClient.Issue, now time.Time) {
	var total int
	defer func() { klog.V(7).Infof("Updated %d comment records", total) }()
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, issue := range *issueComments {
		obj, ok, err := s.store.GetByKey(issue.ID)
		if !ok || err != nil {
			klog.V(5).Infof("JiraIssue %s is not in cache", issue.ID)
			continue
		}
		existing := obj.(*IssueComments)
		if existing.RefreshTime.After(now) {
			klog.V(5).Infof("JiraIssue refresh time is in the future: %v >= %v", existing, now)
			continue
		}

		updated := NewIssueComments(issue.ID, issue.Fields.Comments)
		updated.Info = existing.Info
		updated.RefreshTime = now
		s.store.Update(updated)
		if s.persistedStore != nil {
			a, _ := strconv.Atoi(issue.ID)
			s.persistedStore.NotifyChanged(a)
		}
		total++
	}
}

func (s *CommentStore) issueAdd(obj interface{}) {
	issue, ok := obj.(*Issue)
	if !ok {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	obj, ok, err := s.store.GetByKey(issue.Name)
	if err != nil {
		klog.Errorf("Unexpected error retrieving %q from store: %v", issue.Name, err)
	}
	if ok {
		existing := obj.(*IssueComments).DeepCopyObject().(*IssueComments)
		existing.Info = issue.Info
		if err := s.store.Update(existing); err != nil {
			klog.Errorf("Unable to merge added issue from informer: %v", err)
			return
		}
	} else {
		if err := s.store.Add(&IssueComments{
			ObjectMeta: metav1.ObjectMeta{Name: issue.Name},
			Info:       issue.Info,
		}); err != nil {
			klog.Errorf("Unable to add issue from informer: %v", err)
			return
		}
	}
	s.queue.Add(issue.Name)
}

func (s *CommentStore) issueUpdate(obj interface{}) {
	issue, ok := obj.(*Issue)
	if !ok {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	a, _ := strconv.Atoi(issue.Info.ID)
	existing, ok := s.Get(a)
	if !ok {
		return
	}
	if reflect.DeepEqual(issue.Info, existing.Info) {
		return
	}
	existing = existing.DeepCopyObject().(*IssueComments)
	existing.Info = issue.Info
	if err := s.store.Update(existing); err != nil {
		klog.Errorf("Unable to update issue from informer: %v", err)
		return
	}
	if s.persistedStore != nil {
		a, _ := strconv.Atoi(issue.Info.ID)
		s.persistedStore.NotifyChanged(a)
	}
}

func (s *CommentStore) issueDelete(obj interface{}) {
	var name string
	var err error
	switch t := obj.(type) {
	case cache.DeletedFinalStateUnknown:
		name = t.Key
	case *Issue:
		name = t.Name
	default:
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()

	obj, ok, err := s.store.GetByKey(name)
	if err != nil {
		klog.Errorf("Unexpected error retrieving %q from store: %v", name, err)
		return
	}
	if !ok {
		klog.Errorf("JiraIssue %q not found in store", name)
		return
	}

	issue, ok := obj.(*IssueComments)
	if !ok {
		klog.Errorf("Key %q did not reference object of type IssueComments: %#v", name, obj)
		return
	}
	if err := s.store.Delete(issue); err != nil {
		klog.Errorf("Unable to delete issue from informer: %v", err)
		return
	}
	if err := s.persistedStore.CloseIssue(issue); err != nil {
		klog.Errorf("Unable to close issue in disk store: %v", err)
		return
	}
	s.queue.Add(name)
}
