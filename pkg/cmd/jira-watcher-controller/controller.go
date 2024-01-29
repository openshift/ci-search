package jira_watcher_controller

import (
	"context"
	"fmt"
	jiraBaseClient "github.com/andygrunwald/go-jira"
	"github.com/openshift/ci-search/jira"
	"github.com/openshift/ci-search/pkg/bigquery"
	helpers "github.com/openshift/ci-search/pkg/jira"
	"golang.org/x/time/rate"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"strconv"
	"time"
)

type JiraWatcherController struct {
	jiraClient          *jira.Client
	jiraInformer        cache.SharedIndexInformer
	jiraLister          *jira.IssueLister
	showPrivateMessages bool

	bigqueryClient *bigquery.Client

	dryRun       bool
	maxBatch     int
	rateLimit    *rate.Limiter
	cachesToSync []cache.InformerSynced
	queue        workqueue.RateLimitingInterface
}

func NewJiraWatcherController(jiraClient *jira.Client, jiraInformer cache.SharedIndexInformer, jiraLister *jira.IssueLister, showPrivateMessages bool, bigQueryClient *bigquery.Client, dryRun bool) (*JiraWatcherController, error) {
	c := &JiraWatcherController{
		jiraClient:          jiraClient,
		jiraInformer:        jiraInformer,
		jiraLister:          jiraLister,
		showPrivateMessages: showPrivateMessages,
		bigqueryClient:      bigQueryClient,
		dryRun:              dryRun,
		maxBatch:            250,
		rateLimit:           rate.NewLimiter(rate.Every(15*time.Second), 3),
	}

	c.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "ProwJobStatusController"})
	c.cachesToSync = append(c.cachesToSync, jiraInformer.HasSynced)

	_, err := jiraInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc:    c.Enqueue,
		UpdateFunc: func(old, new interface{}) { c.updateIssue(old, new) },
		DeleteFunc: c.Enqueue,
	})
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *JiraWatcherController) Enqueue(obj interface{}) {
	issue, ok := obj.(*jira.Issue)
	if !ok {
		return
	}
	c.queue.Add(issue.Name)
}

func (c *JiraWatcherController) updateIssue(old, new interface{}) {
	issue, ok := old.(*jira.Issue)
	if !ok {
		return
	}
	update, ok := new.(*jira.Issue)
	if !ok {
		return
	}
	// Only update the issues that ResourceVersion is newer than what's in the Store
	if update.ResourceVersion > issue.ResourceVersion {
		c.queue.Add(update.Name)
	}
}

func (c *JiraWatcherController) RunWorkers(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()

	klog.Infof("Starting Jira Watcher Controller")
	defer func() {
		klog.Infof("Shutting down Jira Watcher Controller")
		c.queue.ShutDown()
		klog.Infof("Jira Watcher Controller shut down")
	}()

	if !cache.WaitForNamedCacheSync("Jira Watcher Controller", ctx.Done(), c.cachesToSync...) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *JiraWatcherController) runWorker(ctx context.Context) {
	for c.processQueue(ctx) {
	}
}

func (c *JiraWatcherController) processQueue(ctx context.Context) bool {
	done := ctx.Done()
	l := c.queue.Len()
	if l == 0 {
		select {
		case <-time.After(5 * time.Second):
			return true
		case <-done:
			return false
		}
	}
	if err := c.rateLimit.Wait(ctx); err != nil {
		return false
	}

	if l > c.maxBatch {
		l = c.maxBatch
	}

	var issueIDs []int
	issueIDs = make([]int, 0, l)
	for l > 0 {
		k, quit := c.queue.Get()
		if quit {
			return false
		}
		c.queue.Done(k)
		id, err := strconv.Atoi(k.(string))
		if err != nil {
			klog.Warningf("comment id %q was not parsable to int: %v", k.(string), err)
			continue
		}
		issueIDs = append(issueIDs, id)
		l--
	}

	klog.V(5).Infof("Fetching %d comments from Jira", len(issueIDs))
	issueComments, err := c.jiraClient.IssueCommentsByID(ctx, issueIDs...)
	if err != nil {
		klog.Warningf("Failed to retrieve comments from Jira: %v", err)
	}
	if !c.showPrivateMessages {
		helpers.FilterIssueComments(&issueComments)
	}

	now := time.Now()
	err = c.sync(ctx, &issueComments, now)
	if err == nil {
		klog.V(5).Infof("Successfully synced %d issues with bigquery", len(issueIDs))
		return true
	}

	utilruntime.HandleError(fmt.Errorf("unable to sync issues with bigquery: %w", err))
	return true
}

func (c *JiraWatcherController) sync(ctx context.Context, issueComments *[]jiraBaseClient.Issue, timestamp time.Time) error {
	var tickets []Ticket
	for _, issue := range *issueComments {
		id, err := strconv.Atoi(issue.ID)
		if err != nil {
			klog.Errorf("Unable to determine issue ID from: %s", issue.ID)
			continue
		}

		existing, err := c.jiraLister.Get(id)
		if err != nil {
			klog.Errorf("JiraIssue %s is not in cache", issue.ID)
			continue
		}

		updated := jira.NewIssueComments(issue.ID, issue.Fields.Comments)
		updated.Info = existing.Info
		updated.RefreshTime = timestamp

		tickets = append(tickets, convertToTicket(updated, timestamp))
	}

	if len(tickets) > 0 {
		if c.dryRun {
			klog.Infof("[Dry Run] Syncing %d issues to bigquery", len(tickets))
		} else {
			klog.V(5).Infof("Syncing %d issues to bigquery", len(tickets))
			err := c.bigqueryClient.WriteRows(ctx, BigqueryDatasetId, BigqueryTableId, tickets)
			if err != nil {
				klog.Errorf("unable to write to bigquery: %v", err)
				return err
			}
		}
	}
	return nil
}
