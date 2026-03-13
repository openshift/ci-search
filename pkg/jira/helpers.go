package jira

import (
	"encoding/json"
	"fmt"
	"strings"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	jiraClient "sigs.k8s.io/prow/pkg/jira"
)

const (
	ReleaseBlockerField = "customfield_10847"
)

func FilterIssueComments(issueComments *[]jiraBaseClient.Issue) {
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

func LineSafe(s string) string {
	return strings.TrimSpace(strings.Replace(s, "\n", " ", -1))
}

func ResolutionFieldName(s *jiraBaseClient.Resolution) string {
	if s != nil {
		resolutionDetails := s.Name
		return LineSafe(resolutionDetails)
	}
	return ""
}

func StatusFieldName(s *jiraBaseClient.Status) string {
	if s != nil {
		statusDetails := s.Name
		return LineSafe(statusDetails)
	}
	return ""
}

func PriorityFieldName(s *jiraBaseClient.Priority) string {
	if s != nil {
		priorityDetails := s.Name
		return LineSafe(priorityDetails)
	}
	return ""
}

func UserFieldDisplayName(s *jiraBaseClient.User) string {
	if s != nil {
		userDetails := s.DisplayName
		return LineSafe(userDetails)
	}
	return ""
}

func ArrayLineSafeString(arr []string, delim string) string {
	inputs := make([]string, 0, len(arr))
	for _, s := range arr {
		inputs = append(inputs, LineSafe(s))
	}
	return strings.Join(inputs, delim)
}

func ArrayLineSafe(arr []string) []string {
	inputs := make([]string, 0, len(arr))
	for _, s := range arr {
		inputs = append(inputs, LineSafe(s))
	}
	return inputs
}

func CommentAuthor(authorDisplayName string) string {
	if authorDisplayName == "" {
		return "ANONYMOUS"
	}
	return strings.TrimSpace(authorDisplayName)
}

func IssueTargetVersions(s jiraBaseClient.Issue) (*[]*jiraBaseClient.Version, error) {
	return jiraClient.GetIssueTargetVersion(&s)
}

type CustomField struct {
	Self     string `json:"self"`
	ID       string `json:"id"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

// GetUnknownField will attempt to get the specified field from the Unknowns struct and unmarshal
// the value into the provided function. If the field is not set, the first return value of this
// function will return false.
func getUnknownField(field string, issue *jiraBaseClient.Issue, fn func() any) (bool, error) {
	obj := fn()
	if issue.Fields == nil || issue.Fields.Unknowns == nil {
		return false, nil
	}
	unknownField, ok := issue.Fields.Unknowns[field]
	if !ok {
		return false, nil
	}
	bytes, err := json.Marshal(unknownField)
	if err != nil {
		return true, fmt.Errorf("failed to process the custom field %s. Error : %v", field, err)
	}
	if err := json.Unmarshal(bytes, obj); err != nil {
		return true, fmt.Errorf("failed to unmarshal the json to struct for %s. Error: %v", field, err)
	}
	return true, nil
}

func GetReleaseBlocker(issue *jiraBaseClient.Issue) (*CustomField, error) {
	var obj *CustomField
	isSet, err := getUnknownField(ReleaseBlockerField, issue, func() any {
		obj = &CustomField{}
		return obj
	})
	if !isSet {
		return nil, err
	}
	return obj, err
}
