package jira_watcher_controller

import (
	"cloud.google.com/go/bigquery"
	"encoding/json"
	"fmt"
	jiraBaseClient "github.com/andygrunwald/go-jira"
	"github.com/openshift/ci-search/jira"
	helpers "github.com/openshift/ci-search/pkg/jira"
	"k8s.io/klog/v2"
	"reflect"
	"slices"
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

type Version struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type Comment struct {
	ID      string    `bigquery:"id"`
	Author  string    `bigquery:"author"`
	Created time.Time `bigquery:"created"`
	Message string    `bigquery:"message"`
}

type Component struct {
	ID   string `bigquery:"id"`
	Name string `bigquery:"name"`
}

type CustomField struct {
	FieldName       string  `bigquery:"field_name" json:"field_name,omitempty"`
	ID              string  `bigquery:"id" json:"id,omitempty"`
	Name            string  `bigquery:"name" json:"name,omitempty"`
	Key             string  `bigquery:"key" json:"key,omitempty"`
	DisplayName     string  `bigquery:"display_name" json:"display_name,omitempty"`
	Description     string  `bigquery:"description" json:"description,omitempty"`
	Value           string  `bigquery:"value" json:"value,omitempty"`
	Votes           float64 `bigquery:"votes" json:"votes,omitempty"`
	StructuredValue string  `bigquery:"structured_value" json:"structured_value,omitempty"`
}

type Ticket struct {
	RecordCreated   time.Time     `bigquery:"record_created"`
	Issue           Issue         `bigquery:"issue"`
	Description     string        `bigquery:"description"`
	Creator         string        `bigquery:"creator"`
	Assignee        string        `bigquery:"assignee"`
	Status          Status        `bigquery:"status"`
	Priority        Priority      `bigquery:"priority"`
	Labels          []string      `bigquery:"labels"`
	TargetVersions  []Version     `bigquery:"target_versions"`
	Resolution      Resolution    `bigquery:"resolution"`
	Comments        []Comment     `bigquery:"comments"`
	Summary         string        `bigquery:"summary"`
	Components      []Component   `bigquery:"components"`
	FixVersions     []Version     `bigquery:"fix_versions"`
	AffectsVersions []Version     `bigquery:"affects_versions"`
	LastChangedTime time.Time     `bigquery:"last_changed_time"`
	CustomFields    []CustomField `bigquery:"custom_fields"`
}

func (t *Ticket) Save() (map[string]bigquery.Value, string, error) {
	return map[string]bigquery.Value{
		"record_created":    t.RecordCreated,
		"issue":             t.Issue,
		"description":       t.Description,
		"creator":           t.Creator,
		"assignee":          t.Assignee,
		"status":            t.Status,
		"priority":          t.Priority,
		"labels":            t.Labels,
		"target_versions":   t.TargetVersions,
		"resolution":        t.Resolution,
		"comments":          t.Comments,
		"summary":           t.Summary,
		"components":        t.Components,
		"fix_versions":      t.FixVersions,
		"affects_versions":  t.AffectsVersions,
		"last_changed_time": t.LastChangedTime,
		"custom_fields":     t.CustomFields,
	}, bigquery.NoDedupeID, nil
}

func convertToTicket(issueComments *jira.IssueComments, timestamp time.Time) *Ticket {
	return &Ticket{
		RecordCreated: timestamp,
		Issue: Issue{
			ID:  issueComments.Info.ID,
			Key: issueComments.Info.Key,
		},
		Description:     helpers.LineSafe(issueComments.Info.Fields.Description),
		Creator:         helpers.UserFieldDisplayName(issueComments.Info.Fields.Creator),
		Assignee:        helpers.UserFieldDisplayName(issueComments.Info.Fields.Assignee),
		Status:          getStatus(issueComments.Info.Fields.Status),
		Priority:        getPriority(issueComments.Info.Fields.Priority),
		Labels:          helpers.ArrayLineSafe(issueComments.Info.Fields.Labels),
		TargetVersions:  getTargetVersions(issueComments.Info),
		Resolution:      getResolution(issueComments.Info.Fields.Resolution),
		Comments:        getComments(issueComments.Comments),
		Summary:         helpers.LineSafe(issueComments.Info.Fields.Summary),
		Components:      getComponents(issueComments.Info.Fields.Components),
		FixVersions:     getFixVersions(issueComments.Info.Fields.FixVersions),
		AffectsVersions: getAffectsVersions(issueComments.Info.Fields.AffectsVersions),
		LastChangedTime: getUpdatedTime(issueComments.Info.Fields.Updated),
		CustomFields:    getCustomFields(issueComments.Info),
	}
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

func getPriority(p *jiraBaseClient.Priority) Priority {
	priority := Priority{}
	if p != nil {
		priority = Priority{
			ID:   p.ID,
			Name: p.Name,
		}
	}
	return priority
}

func getResolution(r *jiraBaseClient.Resolution) Resolution {
	resolution := Resolution{}
	if r != nil {
		resolution = Resolution{
			ID:   r.ID,
			Name: r.Name,
		}
	}
	return resolution
}

func getTargetVersions(i jiraBaseClient.Issue) []Version {
	targetVersions, err := helpers.IssueTargetVersions(i)
	if err != nil {
		return nil
	}
	versions := make([]Version, 0, len(*targetVersions))
	for i, version := range *targetVersions {
		versions = slices.Insert(versions, i, Version{
			ID:   helpers.LineSafe(version.ID),
			Name: helpers.LineSafe(version.Name),
		})
	}
	return versions
}

func getComments(issueComments []*jiraBaseClient.Comment) []Comment {
	comments := make([]Comment, 0, len(issueComments))
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

func getComponents(c []*jiraBaseClient.Component) []Component {
	components := make([]Component, 0, len(c))
	for i, component := range c {
		components = slices.Insert(components, i, Component{
			ID:   component.ID,
			Name: component.Name,
		})
	}
	return components
}

func getFixVersions(fixVersions []*jiraBaseClient.FixVersion) []Version {
	versions := make([]Version, 0, len(fixVersions))
	for i, version := range fixVersions {
		versions = slices.Insert(versions, i, Version{
			ID:   version.ID,
			Name: version.Name,
		})
	}
	return versions
}

func getAffectsVersions(affectsVersions []*jiraBaseClient.AffectsVersion) []Version {
	versions := make([]Version, 0, len(affectsVersions))
	for i, version := range affectsVersions {
		versions = slices.Insert(versions, i, Version{
			ID:   version.ID,
			Name: version.Name,
		})
	}
	return versions
}

func getUpdatedTime(updated jiraBaseClient.Time) time.Time {
	b, err := updated.MarshalJSON()
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse("\"2006-01-02T15:04:05.000-0700\"", string(b))
	if err != nil {
		return time.Time{}
	}
	return t
}

func getCustomFields(i jiraBaseClient.Issue) []CustomField {
	var customFields []CustomField
	for k, v := range i.Fields.Unknowns {
		if v == nil {
			continue
		}
		field := processCustomFieldValue(k, v)
		if field != nil {
			customFields = append(customFields, *field)
		}
	}
	return customFields
}

func processCustomFieldValue(name string, value interface{}) *CustomField {
	var field *CustomField
	var fields []CustomField
	var valueStr string

	switch t := value.(type) {
	case int:
		valueStr = fmt.Sprintf("%d", value)
	case float64:
		valueStr = fmt.Sprintf("%f", value)
	case string:
		valueStr = fmt.Sprintf("%s", value)
	case bool:
		valueStr = fmt.Sprintf("%t", value)
	case []interface{}:
		for _, n := range t {
			cf := getCustomField(name, n)
			if cf != nil {
				fields = append(fields, *cf)
			}
		}
	case map[string]interface{}:
		field = getCustomField(name, value)
	default:
		var r = reflect.TypeOf(t)
		klog.Warningf("Unknown CustomField type: %v", r)
		return nil
	}

	switch {
	case field != nil:
		field.FieldName = name
		return field
	case fields != nil && len(fields) > 0:
		return &CustomField{
			FieldName:       name,
			StructuredValue: generateBigQueryJson(fields),
		}
	case len(valueStr) > 0:
		return &CustomField{
			FieldName: name,
			Value:     valueStr,
		}
	default:
		return nil
	}
}

func getCustomField(name string, value interface{}) *CustomField {
	field := &CustomField{}
	switch v := value.(type) {
	case string:
		field.Value = v
	default:
		bytes, err := json.Marshal(value)
		if err != nil {
			klog.Errorf("failed to process the custom field %s. Error : %v", name, err)
			return nil
		}
		if err = json.Unmarshal(bytes, field); err != nil {
			klog.Errorf("failed to unmarshall the json to struct for %s. Error: %v", name, err)
			return nil
		}
	}
	return field
}

func generateBigQueryJson(src interface{}) string {
	data, err := json.Marshal(src)
	if err != nil {
		klog.Errorf("failed to marshall bigquery json. Error: %v", err)
		return ""
	}
	return string(data)
}
