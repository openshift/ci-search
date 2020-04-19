package bugzilla

import (
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/diff"
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
	bug := &Bug{
		ObjectMeta: metav1.ObjectMeta{
			Name: "181",
		},
		Info: BugInfo{
			ID:      181,
			Summary: "Test bug",
		},
	}
	comments := &BugComments{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "181",
			ResourceVersion:   "1970-01-01T00:04:10Z",
			CreationTimestamp: metav1.Time{Time: time.Unix(100, 0).Local()},
		},
		Info: BugInfo{
			ID:      181,
			Summary: "Test bug",
		},
		Comments: []BugComment{
			{
				ID:           0,
				CreationTime: metav1.Time{Time: time.Unix(100, 0).Local()},
				Time:         metav1.Time{Time: time.Unix(100, 0).Local()},
				Creator:      "Alice",
				Text:         "",
			},
			{
				ID:           1,
				CreationTime: metav1.Time{Time: time.Unix(150, 0)},
				Time:         metav1.Time{Time: time.Unix(200, 0)},
				Creator:      "Bob Bicycle",
				Text:         "Text with newlines\n\nNewline\n",
			},
			{
				ID:           2,
				CreationTime: metav1.Time{Time: time.Unix(200, 0)},
				Time:         metav1.Time{Time: time.Unix(200, 0)},
				Creator:      "Charles Changer",
				Text:         "Fake comment\n---\nWith divider",
			},
			{
				ID:           3,
				CreationTime: metav1.Time{Time: time.Unix(225, 0)},
				Time:         metav1.Time{Time: time.Unix(225, 0)},
				Creator:      "David Doublecomment",
				Text:         "Fake\x1e comment\n---",
			},
			{
				ID:           4,
				CreationTime: metav1.Time{Time: time.Unix(250, 0)},
				Time:         metav1.Time{Time: time.Unix(250, 0)},
				Creator:      "Erin",
				Text:         "---",
			},
			{
				ID:           5,
				CreationTime: metav1.Time{Time: time.Unix(250, 0)},
				Time:         metav1.Time{Time: time.Unix(250, 0)},
				Creator:      "Frank",
				Text:         "Test",
			},
		},
	}

	if err := s.write(bug, comments); err != nil {
		t.Fatal(err)
	}
	tempPath, path := s.pathForBug(bug)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", string(data))
	actualComments, err := readBugComments(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tempPath); err == nil || !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// expect the time to be lost (creation time only is sent)
	comments.Comments[1].Time = comments.Comments[1].CreationTime
	actualComments.Comments[3].Text = strings.Replace(actualComments.Comments[3].Text, "Fake  ", "Fake\x1e ", 1)

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
