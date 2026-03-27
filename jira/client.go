package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	jiraClient "sigs.k8s.io/prow/pkg/jira"
)

const (
	IssueQaContactField      = "customfield_10470"
	IssueTargetVersionField  = "customfield_10855"
	IssueReleaseBlockerField = "customfield_10847"
)

// GetUnknownField will attempt to get the specified field from the Unknowns struct and unmarshal
// the value into the provided function. If the field is not set, the first return value of this
// function will return false.
func GetUnknownField(field string, issue *jiraBaseClient.Issue, fn func() any) (bool, error) {
	obj := fn()
	if issue.Fields == nil || issue.Fields.Unknowns == nil {
		return false, nil
	}
	unknownField, ok := issue.Fields.Unknowns[field]
	if !ok {
		return false, nil
	}
	if unknownField == nil {
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

type SecurityLevel struct {
	Self        string `json:"self"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// GetIssueSecurityLevel returns the security level of an issue. If no security level
// is set for the issue, the returned SecurityLevel and error will both be nil and
// the issue will follow the default project security level.
func GetIssueSecurityLevel(issue *jiraBaseClient.Issue) (*SecurityLevel, error) {
	// TODO: Add field to the upstream go-jira package; if a security level exists, it is returned
	// as part of the issue fields
	// See https://github.com/andygrunwald/go-jira/issues/456
	var obj *SecurityLevel
	isSet, err := GetUnknownField("security", issue, func() any {
		obj = &SecurityLevel{}
		return obj
	})
	if !isSet {
		return nil, err
	}
	return obj, err
}

func GetIssueQaContact(issue *jiraBaseClient.Issue) (*jiraBaseClient.User, error) {
	var obj *jiraBaseClient.User
	isSet, err := GetUnknownField(IssueQaContactField, issue, func() any {
		obj = &jiraBaseClient.User{}
		return obj
	})
	if !isSet {
		return nil, err
	}
	return obj, err
}

func GetIssueTargetVersions(issue *jiraBaseClient.Issue) ([]*jiraBaseClient.Version, error) {
	var obj *[]*jiraBaseClient.Version
	isSet, err := GetUnknownField(IssueTargetVersionField, issue, func() any {
		obj = &[]*jiraBaseClient.Version{{}}
		return obj
	})
	if !isSet {
		return nil, err
	}
	return *obj, err
}

func IssueTargetVersionIDs(s jiraBaseClient.Issue) []string {
	var listOfTargetVersions []string
	targetVersions, err := GetIssueTargetVersions(&s)
	if err != nil {
		return nil
	}
	for _, element := range targetVersions {
		listOfTargetVersions = append(listOfTargetVersions, element.ID)
	}
	return listOfTargetVersions
}

type CustomField struct {
	Self     string `json:"self"`
	ID       string `json:"id"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

func GetReleaseBlocker(issue *jiraBaseClient.Issue) (*CustomField, error) {
	var obj *CustomField
	isSet, err := GetUnknownField(IssueReleaseBlockerField, issue, func() any {
		obj = &CustomField{}
		return obj
	})
	if !isSet {
		return nil, err
	}
	return obj, err
}

func FilterPrivateIssues(issue *jiraBaseClient.Issue) bool {
	securityField, err := GetIssueSecurityLevel(issue)
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
	jqlQuery := fmt.Sprintf("id IN (%s)", jqlParseIds(issues))
	options := &jiraBaseClient.SearchOptionsV2{
		MaxResults: len(issues),
		Fields:     []string{"comment"},
	}
	return c.SearchIssuesWithPagination(ctx, jqlQuery, options)
}

func (c *Client) SearchIssues(ctx context.Context, args SearchIssuesArgs) ([]jiraBaseClient.Issue, error) {
	searchOptions := &jiraBaseClient.SearchOptionsV2{}
	if args.MaxResults == 0 {
		searchOptions.MaxResults = 500
	} else {
		searchOptions.MaxResults = args.MaxResults
	}
	if len(args.IncludeFields) > 0 {
		searchOptions.Fields = issueInfoFields
	}
	return c.SearchIssuesWithPagination(ctx, addTimeToJQL(args.LastChangeTime, args.Jql), searchOptions)
}

func (c *Client) IssuesByID(ctx context.Context, issues ...int) ([]jiraBaseClient.Issue, error) {
	jql := fmt.Sprintf("id IN (%s)", jqlParseIds(issues))
	return c.SearchIssues(ctx, SearchIssuesArgs{IncludeFields: []string{"*all"}, Jql: jql})
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

// SearchIssuesWithPagination performs a paginated search using Jira API v3
// It properly handles pagination by following nextPageToken until all results are retrieved
func (c *Client) SearchIssuesWithPagination(ctx context.Context, jql string, options *jiraBaseClient.SearchOptionsV2) ([]jiraBaseClient.Issue, error) {
	var allIssues []jiraBaseClient.Issue
	var nextPageToken string
	pageNum := 1

	for {
		// Create search options for this page
		options.NextPageToken = nextPageToken

		// Debug: Log pagination details
		klog.V(6).Infof("Fetching page %d with pageSize=%d, nextPageToken='%s'", pageNum, options.MaxResults, nextPageToken)

		// Perform the search for this page
		issues, response, err := c.Client.SearchV2JqlWithContext(ctx, jql, options)
		if err != nil {
			return nil, fmt.Errorf("failed to search issues on page %d: %w", pageNum, err)
		}

		// Debug: Log results from this page
		klog.V(6).Infof("Page %d returned %d issues (total so far: %d)", pageNum, len(issues), len(allIssues)+len(issues))

		// Add issues from this page to our collection
		allIssues = append(allIssues, issues...)

		// Check if this is the last page using the isLast property from the response
		if response != nil && response.IsLast {
			klog.V(6).Infof("Reached last page - isLast=true, got %d issues on final page", len(issues))
			break
		}

		// Get the nextPageToken from the response for the next iteration
		if response != nil && response.NextPageToken != "" {
			nextPageToken = response.NextPageToken
			klog.V(6).Infof("Found nextPageToken: '%s'", nextPageToken)
		} else {
			// No nextPageToken means we've reached the last page
			klog.V(6).Infof("No nextPageToken found - reached last page")
			break
		}

		pageNum++
	}

	klog.V(6).Infof("Pagination complete - retrieved %d total issues across %d pages", len(allIssues), pageNum-1)
	return allIssues, nil
}
