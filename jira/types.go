package jira

import (
	"bytes"
	"encoding/json"
	"k8s.io/klog/v2"
	"strconv"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type IssueList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Issue
}

type Issue struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Info jiraBaseClient.Issue
}

type IssueComments struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Info        jiraBaseClient.Issue
	RefreshTime time.Time
	Comments    []*jiraBaseClient.Comment
}

type Error struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type IssueInfoList struct {
	Issues []jiraBaseClient.Issue `json:"issues"`
	Faults []interface{}          `json:"faults"`
}

type IDString int

func (s *IDString) UnmarshalJSON(data []byte) error {
	if len(data) == 4 && bytes.Equal(data, []byte("null")) {
		*s = 0
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return err
	}
	*s = IDString(i)
	return nil
}

type CommentVisibility struct {
	Type  string `json:"type,omitempty" structs:"type,omitempty"`
	Value string `json:"value,omitempty" structs:"value,omitempty"`
}

// TODO-check what filed is of interest, the rest can be removed
var issueInfoFields = []string{"created", "priority", "labels", "versions", "assignee", "updated", "status", "components", "summary", "creator", "subtasks", "reporter", "progress", "resolution", "fixVersions", IssueTargetVersionField}

type SearchIssuesArgs struct {
	LastChangeTime time.Time
	Jql            string
	IncludeFields  []string
	MaxResults     int
	StartAt        int
}

func NewIssueComments(id string, info *jiraBaseClient.Comments) *IssueComments {
	return setFieldsFromIssueComments(&IssueComments{
		ObjectMeta: metav1.ObjectMeta{
			Name: id,
			UID:  types.UID(id),
		},
		Comments: info.Comments,
	})
}

func setFieldsFromIssueComments(issue *IssueComments) *IssueComments {
	var oldest, newest time.Time
	for _, comment := range issue.Comments {
		if oldest.IsZero() || StringToTime(comment.Created).Before(oldest) {
			oldest = StringToTime(comment.Created)
		}
		if StringToTime(comment.Updated).After(newest) {
			newest = StringToTime(comment.Created)
		}
	}
	issue.CreationTimestamp.Time = oldest
	issue.ResourceVersion = timeToRV(metav1.Time{Time: newest})
	return issue
}

func timeToRV(t metav1.Time) string {
	s, _ := t.MarshalQueryParameter()
	return s
}

func StringToMetaV1Time(timeString string) metav1.Time {
	created, err := time.Parse("2006-01-02T15:04:05.999-0700", timeString)
	if err != nil {
		klog.Error("failed to parse string to time.Time")
	}
	createdByte, _ := created.MarshalJSON()
	var createdMetaV1 metav1.Time
	createdMetaV1.UnmarshalJSON(createdByte)
	return createdMetaV1
}

func Metav1ToJiraTimeString(t metav1.Time) string {
	a := t.Format("2006-01-02T15:04:05.000-0700")
	return a
}

func StringToTime(timeString string) time.Time {
	created, err := time.Parse("2006-01-02T15:04:05.999-0700", timeString)
	if err != nil {
		klog.Errorf("failed to format the Jira timestamp : %s", timeString)
	}
	return created
}

func NewIssueList(issues []jiraBaseClient.Issue, includeFn func(issue *jiraBaseClient.Issue) bool) *IssueList {
	var change time.Time
	items := make([]Issue, 0, len(issues))
	for _, info := range issues {
		if includeFn != nil && !includeFn(&info) {
			continue
		}
		if t := time.Time(info.Fields.Updated); change.Before(t) {
			change = t
		}
		items = append(items, Issue{
			ObjectMeta: metav1.ObjectMeta{
				Name:              info.ID,
				UID:               types.UID(info.ID),
				CreationTimestamp: StringToMetaV1Time(time.Time(info.Fields.Created).Format("2006-01-02T15:04:05.000-0700")),
				ResourceVersion:   timeToRV(StringToMetaV1Time(time.Time(info.Fields.Updated).Format("2006-01-02T15:04:05.000-0700"))),
			},
			Info: info,
		})
	}
	list := &IssueList{Items: items}
	if !change.IsZero() {
		list.ResourceVersion = timeToRV(metav1.Time{Time: change})
	}
	return list
}

func (b Issue) DeepCopyObject() runtime.Object {
	copied := b
	copied.ObjectMeta = *b.ObjectMeta.DeepCopy()
	return &copied
}

func (b IssueComments) DeepCopyObject() runtime.Object {
	copied := b
	copied.ObjectMeta = *b.ObjectMeta.DeepCopy()
	if b.Comments != nil {
		copied.Comments = make([]*jiraBaseClient.Comment, len(b.Comments))
		copy(copied.Comments, b.Comments)
	}
	return &copied
}

func (b *IssueList) DeepCopyObject() runtime.Object {
	copied := *b
	if b.Items != nil {
		copied.Items = make([]Issue, len(b.Items))
		for i := range b.Items {
			copied.Items[i] = *b.Items[i].DeepCopyObject().(*Issue)
		}
	}
	return &copied
}

type CommentAccessor interface {
	Get(id int) (*IssueComments, bool)
}
