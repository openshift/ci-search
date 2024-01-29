package jira

import (
	jiraBaseClient "github.com/andygrunwald/go-jira"
	jiraClient "k8s.io/test-infra/prow/jira"
	"strings"
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
