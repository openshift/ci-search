package bugzilla

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

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	"github.com/openshift/ci-search/walk"
)

type CommentDiskStore struct {
	base   string
	maxAge time.Duration

	queue workqueue.Interface
}

type CommentAccessor interface {
	Get(id int) (*BugComments, bool)
}

func NewCommentDiskStore(path string, maxAge time.Duration) *CommentDiskStore {
	return &CommentDiskStore{
		base:   path,
		maxAge: maxAge,
		queue:  workqueue.NewNamed("comment_disk"),
	}
}

func (s *CommentDiskStore) Run(ctx context.Context, lister *BugLister, store CommentAccessor, disableWrite bool) {
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
				bug = &Bug{ObjectMeta: comments.ObjectMeta, Info: comments.Info}
			}
			if err := s.write(bug, comments); err != nil {
				klog.Errorf("failed to write bug: %v", err)
			}
		}
	}, time.Second)
}

func (s *CommentDiskStore) NotifyChanged(id int) {
	s.queue.Add(strconv.Itoa(id))
}

func (s *CommentDiskStore) Sync(keys []string) ([]*BugComments, error) {
	var known sets.String
	if keys != nil {
		known = sets.NewString(keys...)
	}
	start := time.Now()
	mustExpire := s.maxAge != 0
	expiredAt := start.Add(-s.maxAge)
	tempExpiredAfter := start.Add(-15 * time.Minute)

	bugs := make([]*BugComments, 0, 2048)

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
		if strings.HasPrefix(info.Name(), "z-bug-") {
			if tempExpiredAfter.After(info.ModTime()) {
				os.Remove(path)
				klog.V(5).Infof("Temporary file expired: %s", path)
				return nil
			}
			return nil
		}
		if !strings.HasPrefix(info.Name(), "bug-") {
			return nil
		}
		idString := info.Name()[4:]
		id, err := strconv.ParseInt(idString, 10, 64)
		if err != nil {
			os.Remove(path)
			klog.V(5).Infof("File has invalid name: %s", path)
			return nil
		}

		if known != nil && !known.Has(idString) {
			os.Remove(path)
			klog.V(5).Infof("Bug is not in the known list: %s", path)
			return nil
		}

		comments, err := readBugComments(path)
		if err != nil {
			return fmt.Errorf("unable to read %q: %v", path, err)
		}
		if len(comments.Comments) == 0 {
			os.Remove(path)
			klog.V(5).Infof("Bug has no comments: %s", path)
			return nil
		}
		if comments.Name != idString {
			return fmt.Errorf("file has path %q but ID is %d", path, id)
		}
		comments.CreationTimestamp.Time = comments.Comments[0].CreationTime.Time
		comments.RefreshTime = info.ModTime()
		bugs = append(bugs, comments)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bugs, nil
}

func (s *CommentDiskStore) DeleteBug(bug *Bug) error {
	_, path := s.pathForBug(bug)
	return os.Remove(path)
}

func (s *CommentDiskStore) CloseBug(bug *BugComments) error {

	clone := bug.DeepCopyObject().(*BugComments)
	clone.Info.Status = "CLOSED"
	if err := s.write(&Bug{ObjectMeta: clone.ObjectMeta, Info: clone.Info}, clone); err != nil {
		return fmt.Errorf("could not mark bug %s closed due to write error: %v", clone.Info.ID, err)
	}
	return nil
}
func (s *CommentDiskStore) pathForBug(bug *Bug) (string, string) {
	return filepath.Join(s.base, fmt.Sprintf("z-bug-%d", bug.Info.ID)),
		filepath.Join(s.base, fmt.Sprintf("bug-%d", bug.Info.ID))
}

func lineSafe(s string) string {
	return strings.TrimSpace(strings.Replace(s, "\n", " ", -1))
}
func arrayLineSafe(arr []string, delim string) string {
	inputs := make([]string, 0, len(arr))
	for _, s := range arr {
		inputs = append(inputs, lineSafe(s))
	}
	return strings.Join(inputs, delim)
}

func (s *CommentDiskStore) write(bug *Bug, comments *BugComments) error {
	path, finalPath := s.pathForBug(bug)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(f)

	if _, err := fmt.Fprintf(
		w,
		"Bug %d: %s\nStatus: %s %s\nSeverity: %s\nCreator: %s\nAssigned To: %s\nKeywords: %s\nWhiteboard: %s\nInternal Whiteboard: %s\nTarget Release: %s\nComponent: %s\nEnvironment:%s\n---\n",
		bug.Info.ID,
		lineSafe(bug.Info.Summary),
		lineSafe(bug.Info.Status),
		lineSafe(bug.Info.Resolution),
		lineSafe(bug.Info.Severity),
		lineSafe(bug.Info.Creator),
		lineSafe(bug.Info.AssignedTo),
		arrayLineSafe(bug.Info.Keywords, ", "),
		lineSafe(bug.Info.Whiteboard),
		lineSafe(bug.Info.InternalWhiteboard),
		arrayLineSafe(bug.Info.TargetRelease, ", "),
		arrayLineSafe(bug.Info.Component, ", "),
		lineSafe(strings.ReplaceAll(bug.Info.Environment, "\x0D", "")),
	); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}

	for _, comment := range comments.Comments {
		escapedText := strings.ReplaceAll(strings.ReplaceAll(comment.Text, "\x00", " "), "\x1e", " ")
		if _, err := fmt.Fprintf(
			w,
			"Comment %d by %s at %s\n%s\n\x1e",
			comment.ID,
			strings.TrimSpace(comment.Creator),
			timeToRV(comment.CreationTime),
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
	reDiskCommentsLineHeader        = regexp.MustCompile(`^Bug (\d+): (.*)$`)
	reDiskCommentsLineCommentHeader = regexp.MustCompile(`^Comment (\d+) by (.+) at (\d\d\d\d-\d\d-\d\dT\d\d:\d\d:\d\dZ)$`)
)

const (
	bugCommentDelimiter = "\x1e"
)

func readBugComments(path string) (*BugComments, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var bug BugComments
	comments := make([]BugComment, 0, 4)

	// allow lines of up to 4MB
	delim := []byte(bugCommentDelimiter)
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
	m := reDiskCommentsLineHeader.FindStringSubmatch(sr.Text())
	if m == nil {
		return nil, fmt.Errorf("%s: first line must be of the form 'Bug NUMBER: DESCRIPTION'", path)
	}

	bug.Name = m[1]
	bug.Info.ID, _ = strconv.Atoi(m[1])
	bug.Info.Summary = m[2]

	// strip the rest of the header
	var foundSeparator bool
ScanHeader:
	for sr.Scan() {
		text := sr.Text()
		switch {
		case strings.HasPrefix(text, "Status: "):
			parts := strings.SplitN(text, " ", 3)
			if len(parts) < 2 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Status = parts[1]
			if len(parts) > 2 {
				bug.Info.Resolution = parts[2]
			}
		case strings.HasPrefix(text, "Severity: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Severity = parts[1]
		case strings.HasPrefix(text, "Creator: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Creator = parts[1]
		case strings.HasPrefix(text, "Assigned To: "):
			parts := strings.SplitN(text, " ", 3)
			if len(parts) < 2 || len(parts[2]) == 0 {
				continue
			}
			bug.Info.AssignedTo = parts[2]
		case strings.HasPrefix(text, "Keywords: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Keywords = strings.Split(parts[1], ", ")
		case strings.HasPrefix(text, "Whiteboard: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Whiteboard = parts[1]
		case strings.HasPrefix(text, "Internal Whiteboard: "):
			parts := strings.SplitN(text, " ", 3)
			if len(parts) < 1 || len(parts[2]) == 0 {
				continue
			}
			bug.Info.InternalWhiteboard = parts[2]
		case strings.HasPrefix(text, "Target Release: "):
			parts := strings.SplitN(text, " ", 3)
			if len(parts) < 1 || len(parts[2]) == 0 {
				continue
			}
			bug.Info.TargetRelease = strings.Split(parts[2], ", ")
		case strings.HasPrefix(text, "Component: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Component = strings.Split(parts[1], ", ")
		case strings.HasPrefix(text, "Environment: "):
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 1 || len(parts[1]) == 0 {
				continue
			}
			bug.Info.Environment = parts[1]
		case text == "---":
			foundSeparator = true
			break ScanHeader
		}
	}
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("%s: unable to read stored bug: %v", path, err)
	}
	if !foundSeparator {
		return nil, fmt.Errorf("%s: unable to read stored bug: no body separator", path)
	}

	phase = 1
	var comment BugComment
	for sr.Scan() {
		switch phase {
		case 1:
			m := reDiskCommentsLineCommentHeader.FindStringSubmatch(sr.Text())
			if m == nil {
				return nil, fmt.Errorf("%s: comment header line %d must be of the form 'Comment ID by AUTHOR at DATE': %q", path, len(comments)+1, sr.Text())
			}

			comment.ID, err = strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("%s: comment header line must have an integer ID: %v", path, err)
			}

			comment.Creator = m[2]

			if err := comment.CreationTime.UnmarshalQueryParameter(m[3]); err != nil {
				return nil, fmt.Errorf("%s: comment header line must have an RFC3339 DATE: %v", path, err)
			}
			comment.Time = comment.CreationTime

			phase = 2

		case 2:
			comment.Text = strings.TrimSuffix(sr.Text(), "\n")
			comments = append(comments, comment)
			comment = BugComment{}

			phase = 1

		default:
			return nil, fmt.Errorf("%s: programmer error, unexpected phase %d", path, phase)
		}
	}
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("%s: failed to parse comments: %v", path, err)
	}

	bug.Comments = comments
	return setFieldsFromBugComments(&bug), nil
}
