package jira

import (
	"context"
	"fmt"
	"k8s.io/klog/v2"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	jiraClient "sigs.k8s.io/prow/prow/jira"
)

// TODO move to the Jira Client lib
const (
	IssueQaContactField     = "customfield_12316243"
	IssueTargetVersionField = "customfield_12319940"
)

// IssueTargetVersionIDs TODO - move to the Jira Client/Plugin
func IssueTargetVersionIDs(s jiraBaseClient.Issue) []string {
	var listOfTargetVersions []string
	targetVersion, err := jiraClient.GetIssueTargetVersion(&s)
	if err != nil {
		return nil
	}
	for _, element := range *targetVersion {
		listOfTargetVersions = append(listOfTargetVersions, element.ID)
	}
	return listOfTargetVersions
}

func FilterPrivateIssues(issue *jiraBaseClient.Issue) bool {
	securityField, err := jiraClient.GetIssueSecurityLevel(issue)
	if err != nil {
		klog.Errorf("failed to get the security level for issue: %s", issue.ID)
		return false
	}
	// if the security field is not defined, the issue is not private
	if securityField == nil {
		return true
	}

	// the GetIssueSecurityLevel can return an empty struct, when the security field is not defined in the custom fields
	// TODO - we need to improve the GetIssueSecurityLevel method, if the field is not defined, we should return nil, not an empty struct
	if securityField.ID == "" {
		return true
	}

	// in the current state, we will filter out every issue with the security field defined, regardless of the
	// configuration
	return false
}

// TODO - currently unused jira field. check what is necessary, remove the rest. Move to the Jira client/plugin
// This might be moved to the Jira client
//func fixVersionJira(s jiraClient.Issue) []string {
//	var listOfFixedVersions []string
//	for _, element := range s.Fields.FixVersions {
//		listOfFixedVersions = append(listOfFixedVersions, element.Name)
//	}
//	return listOfFixedVersions
//}
//
//func versionsJira(s jiraClient.Issue) []string {
//	var listOfFixedVersions []string
//	for _, element := range s.Fields.AffectsVersions {
//		listOfFixedVersions = append(listOfFixedVersions, element.Name)
//	}
//	return listOfFixedVersions
//}
//
//func componentsJira(s jiraClient.Issue) []string {
//	var listOfFixedVersions []string
//	for _, element := range s.Fields.Components {
//		listOfFixedVersions = append(listOfFixedVersions, element.Name)
//	}
//	return listOfFixedVersions
//}

type Client struct {
	Client jiraClient.Client
}

func addTimeToJQL(t time.Time, jql string) string {
	if !t.IsZero() {
		// JQL "updated" accepts this format: "yyyy/MM/dd HH:mm"
		roundedTime := fmt.Sprintf("%d/%d/%d %d:%d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute())
		return fmt.Sprintf("(%s)&updated>='%s'", jql, roundedTime)
	}
	return jql
}
func (c *Client) IssueCommentsByID(ctx context.Context, issues ...int) ([]jiraBaseClient.Issue, error) {
	var searchOptions jiraBaseClient.SearchOptions
	jqlQuery := fmt.Sprintf("id IN (%s)", jqlParseIds(issues))
	searchOptions.MaxResults = len(issues)
	searchOptions.Fields = []string{"comment"}
	search, _, err := c.Client.SearchWithContext(ctx, jqlQuery, &searchOptions)
	return search, err
}

func (c *Client) SearchIssues(ctx context.Context, args SearchIssuesArgs) ([]jiraBaseClient.Issue, error) {
	var searchOptions jiraBaseClient.SearchOptions
	if args.MaxResults >= 0 {
		if args.MaxResults == 0 {
			searchOptions.MaxResults = 500
		} else {
			searchOptions.MaxResults = args.MaxResults
		}
	}
	if args.StartAt > 0 {
		searchOptions.StartAt = args.StartAt
	}
	if len(args.IncludeFields) > 0 {
		searchOptions.Fields = issueInfoFields
	}
	search, _, err := c.Client.SearchWithContext(ctx, addTimeToJQL(args.LastChangeTime, args.Jql), &searchOptions)
	return search, err
}

func (c *Client) IssuesByID(ctx context.Context, issues ...int) ([]jiraBaseClient.Issue, error) {
	jql := fmt.Sprintf("id IN (%s)", jqlParseIds(issues))
	return c.SearchIssues(ctx, SearchIssuesArgs{Jql: jql})
}

type ClientError struct {
	Err Error
}

func (e *ClientError) Error() string {
	return e.Err.Message
}

func jqlParseIds(issues []int) string {
	var ids string
	for _, issue := range issues {
		if ids == "" {
			ids = fmt.Sprintf("%d", issue)
		} else {
			ids = fmt.Sprintf("%s,%d", ids, issue)
		}
	}
	return ids
}
