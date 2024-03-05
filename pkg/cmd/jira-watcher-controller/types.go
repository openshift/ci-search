package jira_watcher_controller

import (
	"cloud.google.com/go/bigquery"
	jiraBaseClient "github.com/andygrunwald/go-jira"
	"github.com/openshift/ci-search/jira"
	helpers "github.com/openshift/ci-search/pkg/jira"
	"k8s.io/klog/v2"
	"strings"
	"time"
)

const (
	BigqueryDatasetId = "jira_data"
	BigqueryTableId   = "tickets"
)

type Issue struct {
	ID  string `bigquery:"id"`
	Key string `bigquery:"key"`
}

type Status struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type Priority struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type Resolution struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type TargetVersion struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type Comment struct {
	ID      string    `bigquery:"id"`
	Author  string    `bigquery:"author"`
	Created time.Time `bigquery:"created"`
	Message string    `bigquery:"message"`
}

type Ticket struct {
	RecordCreated  time.Time       `bigquery:"record_created"`
	Issue          Issue           `bigquery:"issue"`
	Description    string          `bigquery:"description"`
	Creator        string          `bigquery:"creator"`
	Assignee       string          `bigquery:"assignee"`
	Status         Status          `bigquery:"status"`
	Priority       Priority        `bigquery:"priority"`
	Labels         []string        `bigquery:"labels"`
	TargetVersions []TargetVersion `bigquery:"target_versions"`
	Resolution     Resolution      `bigquery:"resolution"`
	Comments       []Comment       `bigquery:"comments"`
	Summary        string          `bigquery:"summary"`
}

func (t *Ticket) Save() (map[string]bigquery.Value, string, error) {
	return map[string]bigquery.Value{
		"record_created": t.RecordCreated,
		"issue":          t.Issue,
		"description":    t.Description,
		"creator":        t.Creator,
		"assignee":       t.Assignee,
		"status":         t.Status,
		"priority":       t.Priority,
		"labels":         t.Labels,
		"target_version": t.TargetVersions,
		"resolution":     t.Resolution,
		"comments":       t.Comments,
		"summary":        t.Summary,
	}, bigquery.NoDedupeID, nil
}

func convertToTicket(issueComments *jira.IssueComments, timestamp time.Time) Ticket {
	ticket := Ticket{
		RecordCreated: timestamp,
		Issue: Issue{
			ID:  issueComments.Info.ID,
			Key: issueComments.Info.Key,
		},
		Description:    helpers.LineSafe(issueComments.Info.Fields.Description),
		Creator:        helpers.UserFieldDisplayName(issueComments.Info.Fields.Creator),
		Assignee:       helpers.UserFieldDisplayName(issueComments.Info.Fields.Assignee),
		Status:         getStatus(issueComments.Info.Fields.Status),
		Priority:       getPriority(issueComments.Info.Fields.Priority),
		Labels:         helpers.ArrayLineSafe(issueComments.Info.Fields.Labels),
		TargetVersions: getTargetVersions(issueComments.Info),
		Resolution:     getResolution(issueComments.Info.Fields.Resolution),
		Comments:       getComments(issueComments.Comments),
		Summary:        helpers.LineSafe(issueComments.Info.Fields.Summary),
	}

	return ticket
}

func getStatus(s *jiraBaseClient.Status) Status {
	status := Status{}
	if s != nil {
		status = Status{
			ID:   s.ID,
			Name: s.Name,
		}
	}
	return status
}

func getPriority(s *jiraBaseClient.Priority) Priority {
	priority := Priority{}
	if s != nil {
		priority = Priority{
			ID:   s.ID,
			Name: s.Name,
		}
	}
	return priority
}

func getResolution(s *jiraBaseClient.Resolution) Resolution {
	resolution := Resolution{}
	if s != nil {
		resolution = Resolution{
			ID:   s.ID,
			Name: s.Name,
		}
	}
	return resolution
}

func getTargetVersions(i jiraBaseClient.Issue) []TargetVersion {
	var versions []TargetVersion
	targetVersions, err := helpers.IssueTargetVersions(i)
	if err != nil {
		return nil
	}
	for _, version := range *targetVersions {
		versions = append(versions, TargetVersion{
			ID:   helpers.LineSafe(version.ID),
			Name: helpers.LineSafe(version.Name),
		})
	}
	return versions
}

func getComments(issueComments []*jiraBaseClient.Comment) []Comment {
	var comments []Comment

	for _, comment := range issueComments {
		escapedText := strings.ReplaceAll(strings.ReplaceAll(comment.Body, "\x00", " "), "\x1e", " ")
		comments = append(comments, Comment{
			ID:      comment.ID,
			Author:  helpers.CommentAuthor(comment.Author.DisplayName),
			Created: getCreatedTime(comment.Created),
			Message: escapedText,
		})
	}

	return comments
}

func getCreatedTime(created string) time.Time {
	out, err := time.Parse("2006-01-02T15:04:05.999-0700", created)
	if err != nil {
		klog.Errorf("unable to parse jira comment created time: %v", err)
		return time.Time{}
	}
	return out
}
