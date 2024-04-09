package jira

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	jiraClient "sigs.k8s.io/prow/prow/jira"

	helpers "github.com/openshift/ci-search/pkg/jira"
	"github.com/openshift/ci-search/walk"
)

type CommentDiskStore struct {
	base   string
	maxAge time.Duration

	queue workqueue.Interface
}

func NewCommentDiskStore(path string, maxAge time.Duration) *CommentDiskStore {
	return &CommentDiskStore{
		base:   path,
		maxAge: maxAge,
		queue:  workqueue.NewNamed("comment_disk"),
	}
}

func (s *CommentDiskStore) Run(ctx context.Context, lister *IssueLister, store CommentAccessor, disableWrite bool) {
	defer klog.V(2).Infof("Comment disk worker exited")
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		for {
			obj, done := s.queue.Get()
			if done {
				return
			}
			if disableWrite {
				s.queue.Done(obj)
				return
			}
			id, err := strconv.Atoi(obj.(string))
			if err != nil {
				s.queue.Done(obj)
				klog.Errorf("unexpected id in queue: %v", err)
				continue
			}
			comments, ok := store.Get(id)
			if !ok {
				s.queue.Done(obj)
				klog.V(5).Infof("No comments for %d", id)
				continue
			}
			s.queue.Done(obj)
			bug, err := lister.Get(id)
			if err != nil {
				bug = &Issue{ObjectMeta: comments.ObjectMeta, Info: comments.Info}
			}
			if err := s.write(bug, comments); err != nil {
				klog.Errorf("write bug: %v", err)
			}
		}
	}, time.Second)
}

func (s *CommentDiskStore) NotifyChanged(id int) {
	s.queue.Add(strconv.Itoa(id))
}

func (s *CommentDiskStore) Sync(keys []string) ([]*IssueComments, error) {
	var known sets.String
	if keys != nil {
		known = sets.NewString(keys...)
	}
	start := time.Now()
	mustExpire := s.maxAge != 0
	expiredAt := start.Add(-s.maxAge)
	tempExpiredAfter := start.Add(-15 * time.Minute)

	bugs := make([]*IssueComments, 0, 2048)

	err := walk.Walk(s.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}

		if mustExpire && expiredAt.After(info.ModTime()) {
			os.Remove(path)
			klog.V(5).Infof("File expired: %s", path)
			return nil
		}
		relPath, err := filepath.Rel(s.base, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if strings.HasPrefix(info.Name(), "z-issue__") {
			if tempExpiredAfter.After(info.ModTime()) {
				os.Remove(path)
				klog.V(5).Infof("Temporary file expired: %s", path)
				return nil
			}
			return nil
		}
		if !strings.HasPrefix(info.Name(), "issue__") {
			return nil
		}
		nameParts := strings.Split(info.Name(), "__")
		idString := nameParts[2]
		id, err := strconv.ParseInt(idString, 10, 64)
		if err != nil {
			os.Remove(path)
			klog.V(5).Infof("File has invalid name: %s", path)
			return nil
		}

		if known != nil && !known.Has(idString) {
			os.Remove(path)
			klog.V(5).Infof("JiraIssue is not in the known list: %s", path)
			return nil
		}

		comments, err := ReadBugComments(path)
		if err != nil {
			return fmt.Errorf("unable to read %q: %v", path, err)
		}
		if len(comments.Comments) == 0 {
			os.Remove(path)
			klog.V(5).Infof("Issue has no comments: %s", path)
			return nil
		}
		if comments.Name != idString {
			return fmt.Errorf("file has path %q but ID is %d", path, id)
		}
		comments.CreationTimestamp.Time = StringToTime(comments.Comments[0].Created)
		comments.RefreshTime = info.ModTime()
		bugs = append(bugs, comments)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bugs, nil
}

func (s *CommentDiskStore) DeleteIssue(bug *Issue) error {
	_, path := s.pathForBug(bug)
	return os.Remove(path)
}

func (s *CommentDiskStore) CloseIssue(bug *IssueComments) error {
	clone := bug.DeepCopyObject().(*IssueComments)
	clone.Info.Fields.Status.Name = jiraClient.StatusClosed
	if err := s.write(&Issue{ObjectMeta: clone.ObjectMeta, Info: clone.Info}, clone); err != nil {
		return fmt.Errorf("could not mark bug %s closed due to write error: %v", clone.Info.ID, err)
	}
	return nil
}
func (s *CommentDiskStore) pathForBug(bug *Issue) (string, string) {
	return filepath.Join(s.base, fmt.Sprintf("z-issue__%s__%s", bug.Info.Key, bug.Info.ID)),
		filepath.Join(s.base, fmt.Sprintf("issue__%s__%s", bug.Info.Key, bug.Info.ID))
}

func (s *CommentDiskStore) write(issue *Issue, comments *IssueComments) error {
	path, finalPath := s.pathForBug(issue)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(f)

	if _, err := fmt.Fprintf(
		w,
		"Issue %s: %s\nDescription: %s \nStatus: %s\nResolution: %s\nPriority: %s\nCreator: %s\nAssigned To: %s\nLabels: %s\nTarget Version: %s\n---\n",
		issue.Info.ID,
		helpers.LineSafe(issue.Info.Fields.Summary),
		helpers.LineSafe(issue.Info.Fields.Description),
		helpers.StatusFieldName(issue.Info.Fields.Status),
		helpers.ResolutionFieldName(issue.Info.Fields.Resolution),
		helpers.PriorityFieldName(issue.Info.Fields.Priority),
		helpers.UserFieldDisplayName(issue.Info.Fields.Creator),
		helpers.UserFieldDisplayName(issue.Info.Fields.Assignee),
		helpers.ArrayLineSafeString(issue.Info.Fields.Labels, ", "),
		helpers.ArrayLineSafeString(IssueTargetVersionIDs(issue.Info), ", "),
		//TODO these fields might or might not contain usefully information. Check what makes sense to keep, and what the requirements are
		//arrayLineSafe(fixVersionJira(issue.Info), ", "),
		//arrayLineSafe(versionsJira(issue.Info), ", "),
		//arrayLineSafe(componentsJira(issue.Info), ", "),
		//lineSafe(strings.ReplaceAll(issue.Info.Environment, "\x0D", "")),
	); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}

	for _, comment := range comments.Comments {
		escapedText := strings.ReplaceAll(strings.ReplaceAll(comment.Body, "\x00", " "), "\x1e", " ")
		if _, err := fmt.Fprintf(
			w,
			"Comment %s by %s at %s\n%s\n\x1e",
			comment.ID,
			helpers.CommentAuthor(comment.Author.DisplayName),
			comment.Created,
			escapedText,
		); err != nil {
			f.Close()
			os.Remove(path)
			return err
		}
	}

	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	if err := os.Chtimes(path, comments.RefreshTime, comments.RefreshTime); err != nil {
		os.Remove(path)
		return err
	}
	return os.Rename(path, finalPath)
}

var (
	reDiskCommentsLineHeader        = regexp.MustCompile(`^Issue (\d+): (.*)$`)
	reDiskCommentsLineCommentHeader = regexp.MustCompile(`^Comment (\d+) by (.+) at (\d\d\d\d-\d\d-\d\dT\d\d:\d\d:\d\d\.\d\d\d[+-]\d\d\d\d)$`)
)

const (
	issueCommentDelimiter = "\x1e"
)

func ReadBugComments(path string) (*IssueComments, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var bug IssueComments
	var fields jiraBaseClient.IssueFields
	var status jiraBaseClient.Status
	var priority jiraBaseClient.Priority
	var creator jiraBaseClient.User
	var assignee jiraBaseClient.User
	var resolution jiraBaseClient.Resolution
	comments := make([]*jiraBaseClient.Comment, 0, 4)

	// allow lines of up to 4MB
	delim := []byte(issueCommentDelimiter)
	br := bufio.NewReader(f)
	sr := bufio.NewScanner(br)
	sr.Buffer(make([]byte, 4*1024), 4*1024*1024)
	phase := 0
	sr.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		switch phase {
		case 0, 1:
			return bufio.ScanLines(data, atEOF)
		case 2:
			if atEOF && len(data) == 0 {
				return 0, nil, nil
			}
			if i := bytes.Index(data, delim); i >= 0 {
				// We have a full comment
				return i + len(delim), data[0:i], nil
			}
			// If we're at EOF, we have a final, non-terminated line. Return it.
			if atEOF {
				return len(data), data, nil
			}
			// Request more data.
			return 0, nil, nil
		default:
			return 0, nil, fmt.Errorf("unrecognized phase %d", phase)
		}
	})

	// PHASE 0: Header
	if !sr.Scan() {
		return nil, fmt.Errorf("%s: first line missing or malformed: %v", path, sr.Err())
	}
	m := reDiskCommentsLineHeader.FindStringSubmatch(dropSpaces(sr.Text()))
	if m == nil {
		return nil, fmt.Errorf("%s: first line must be of the form 'JiraIssue NUMBER: DESCRIPTION'", path)
	}

	bug.Name = m[1]
	bug.Info.ID = m[1]
	bug.Info.Key = strings.Split(path, "__")[1]
	fields.Summary = m[2]

	// strip the rest of the header
	var foundSeparator bool
ScanHeader:
	for sr.Scan() {
		text := sr.Text()
		switch {
		case strings.HasPrefix(text, "Description: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			fields.Description = strings.TrimSpace(parts[1])
		case strings.HasPrefix(text, "Status: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			status.Name = strings.TrimSpace(parts[1])
			fields.Status = &status
		case strings.HasPrefix(text, "Priority: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			priority.Name = parts[1]
			fields.Priority = &priority
		case strings.HasPrefix(text, "Creator: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			creator.DisplayName = parts[1]
			fields.Creator = &creator
		case strings.HasPrefix(text, "Assigned To: "):
			parts := strings.SplitN(text, " ", 3)
			if len(parts) < 3 || len(parts[2]) == 0 {
				continue
			}
			assignee.DisplayName = parts[2]
			fields.Assignee = &assignee
		case strings.HasPrefix(text, "Labels: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			fields.Labels = strings.Split(parts[1], ", ")
		case strings.HasPrefix(text, "Resolution: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			resolution.Name = parts[1]
			fields.Resolution = &resolution

		case text == "---":
			foundSeparator = true
			break ScanHeader
		}
	}
	bug.Info.Fields = &fields
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("%s: unable to read stored bug: %v", path, err)
	}
	if !foundSeparator {
		return nil, fmt.Errorf("%s: unable to read stored bug: no body separator", path)
	}

	phase = 1
	comment := new(jiraBaseClient.Comment)
	for sr.Scan() {
		switch phase {
		case 1:
			m := reDiskCommentsLineCommentHeader.FindStringSubmatch(dropSpaces(sr.Text()))
			if m == nil {
				return nil, fmt.Errorf("%s: comment header line %d must be of the form 'Comment ID by AUTHOR at DATE': %q", path, len(comments)+1, sr.Text())
			}

			comment.ID = m[1]
			if err != nil {
				return nil, fmt.Errorf("%s: comment header line must have an integer ID: %v", path, err)
			}

			if m[2] == "ANONYMOUS" {
				comment.Author.DisplayName = ""
			} else {
				comment.Author.DisplayName = m[2]
			}
			comment.Created = m[3]
			comment.Updated = comment.Created

			phase = 2

		case 2:
			comment.Body = strings.TrimSuffix(sr.Text(), "\n")
			comments = append(comments, comment)
			comment = &jiraBaseClient.Comment{}

			phase = 1

		default:
			return nil, fmt.Errorf("%s: programmer error, unexpected phase %d", path, phase)
		}
	}
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("%s: failed to parse comments: %v", path, err)
	}

	bug.Comments = comments
	return setFieldsFromIssueComments(&bug), nil
}

func dropSpaces(s string) string {
	s = strings.Replace(s, "\x1e", "", 1)
	return s
}
