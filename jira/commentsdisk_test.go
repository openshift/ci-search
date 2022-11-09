package jira

import (
	"io/ioutil"
	"k8s.io/utils/diff"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCommentDiskStore_write(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	s := &CommentDiskStore{
		base: dir,
	}
	issue := &Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "181",
		},
		Info: jiraBaseClient.Issue{
			ID:  strconv.Itoa(181),
			Key: "OCP-123",
			Fields: &jiraBaseClient.IssueFields{
				Assignee: &jiraBaseClient.User{
					DisplayName: "John Doe",
				},
				Description: "This is the description",
				Status:      &jiraBaseClient.Status{Name: "In Progress"},
				Resolution:  &jiraBaseClient.Resolution{Name: "Resolution"},
				Priority:    &jiraBaseClient.Priority{Name: "Urgent"},

				Summary: "This is a test issue description",
				Creator: &jiraBaseClient.User{DisplayName: "John Smith"},
				Labels:  []string{"Openshift"},
			},
		},
	}
	comments := &IssueComments{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "181",
			ResourceVersion:   "1970-01-01T00:05:00Z",
			CreationTimestamp: metav1.Time{Time: time.Unix(100, 0).Local()},
		},
		Info: jiraBaseClient.Issue{
			ID:  strconv.Itoa(181),
			Key: "OCP-123",
			Fields: &jiraBaseClient.IssueFields{
				Assignee: &jiraBaseClient.User{
					DisplayName: "John Doe",
				},
				Description: "This is the description",
				Status:      &jiraBaseClient.Status{Name: "In Progress"},
				Resolution:  &jiraBaseClient.Resolution{Name: "Resolution"},
				Priority:    &jiraBaseClient.Priority{Name: "Urgent"},

				Summary: "This is a test issue description",
				Creator: &jiraBaseClient.User{DisplayName: "John Smith"},
				Labels:  []string{"Openshift"},
			},
		},
		Comments: []*jiraBaseClient.Comment{
			{
				ID:      "0",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(100, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(100, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Alice"},
				Body:    "",
			},
			{
				ID:      "1",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(150, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(200, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Monty Python"},
				Body:    "Text with newlines\n\nNewline\n",
			},
			{
				ID:      "2",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(200, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(200, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Tony"},
				Body:    "\"Fake comment\\n---\\nWith divider\"",
			},
			{
				ID:      "3",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(250, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(250, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Joe"},
				Body:    "Fake\x1e comment\n---",
			},
			{
				ID:      "4",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Erin"},
				Body:    "---",
			},
			{
				ID:      "5",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Author:  jiraBaseClient.User{DisplayName: "Frank"},
				Body:    "Test",
			},
			{
				ID:      "6",
				Created: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Updated: Metav1ToJiraTimeString(metav1.Time{Time: time.Unix(300, 0).Local()}),
				Author:  jiraBaseClient.User{},
				Body:    "Test",
			},
		},
	}

	if err := s.write(issue, comments); err != nil {
		t.Fatal(err)
	}
	tempPath, path := s.pathForBug(issue)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", string(data))
	actualComments, err := ReadBugComments(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tempPath); err == nil || !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// expect the time to be lost (creation time only is sent)
	comments.Comments[1].Updated = comments.Comments[1].Created
	actualComments.Comments[3].Body = strings.Replace(actualComments.Comments[3].Body, "Fake  ", "Fake\x1e ", 1)
	if !reflect.DeepEqual(comments, actualComments) {
		t.Fatalf("\n%s", diff.ObjectReflectDiff(comments, actualComments))
	}

	list, err := s.Sync(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Comments) != len(comments.Comments) {
		t.Fatalf("%#v", list)
	}
}
