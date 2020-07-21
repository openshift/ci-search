package bugzilla

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type BugList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Bug
}

type Bug struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Info BugInfo
}

type BugComments struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Info        BugInfo
	RefreshTime time.Time
	Comments    []BugComment
}

type Error struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type BugInfoList struct {
	Bugs   []BugInfo     `json:"bugs"`
	Faults []interface{} `json:"faults"`
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

type BugCommentsList struct {
	Bugs map[IDString]BugCommentInfo `json:"bugs"`
}

type BugCommentInfo struct {
	Comments []BugComment `json:"comments"`
}

type BugComment struct {
	ID           int         `json:"id"`
	IsPrivate    bool        `json:"is_private"`
	Creator      string      `json:"creator"`
	CreationTime metav1.Time `json:"creation_time"`
	Time         metav1.Time `json:"time"`
	Text         string      `json:"text"`
}

var bugCommentFields = []string{"id", "is_private", "creator", "creation_time", "time", "text"}

type BugInfo struct {
	ID                 int         `json:"id"`
	Status             string      `json:"status"`
	Resolution         string      `json:"resolution"`
	Severity           string      `json:"severity"`
	Priority           string      `json:"priority"`
	Summary            string      `json:"summary"`
	Keywords           []string    `json:"keywords"`
	Whiteboard         string      `json:"whiteboard"`
	InternalWhiteboard string      `json:"cf_internal_whiteboard"`
	Creator            string      `json:"creator"`
	Component          []string    `json:"component"`
	AssignedTo         string      `json:"assigned_to"`
	CreationTime       metav1.Time `json:"creation_time"`
	LastChangeTime     metav1.Time `json:"last_change_time"`
	Environment        string      `json:"cf_environment"`
	TargetRelease      []string    `json:"target_release"`
}

var bugInfoFields = []string{"id", "status", "resolution", "severity", "priority", "summary", "keywords", "whiteboard", "cf_internal_whiteboard", "creator", "assigned_to", "creation_time", "last_change_time", "cf_environment", "target_release", "component"}

type SearchBugsArgs struct {
	LastChangeTime time.Time
	IDs            []int
	Quicksearch    string

	IncludeFields []string
	Limit         int
	Offset        int
}

func (arg SearchBugsArgs) Add(v url.Values) {
	if !arg.LastChangeTime.IsZero() {
		v.Set("last_change_time", arg.LastChangeTime.UTC().Format(time.RFC3339))
	}
	for _, id := range arg.IDs {
		v.Add("id", strconv.Itoa(id))
	}
	if len(arg.Quicksearch) > 0 {
		v.Set("quicksearch", arg.Quicksearch)
	}
	if len(arg.IncludeFields) > 0 {
		v.Set("include_fields", strings.Join(arg.IncludeFields, ","))
	}
	if arg.Limit > 0 {
		v.Set("limit", strconv.Itoa(arg.Limit))
	}
	if arg.Offset > 0 || arg.Limit > 0 {
		v.Set("offset", strconv.Itoa(arg.Offset))
	}
}

func NewBug(info *BugInfo) *Bug {
	return &Bug{
		ObjectMeta: metav1.ObjectMeta{
			Name:              strconv.Itoa(info.ID),
			UID:               types.UID(strconv.Itoa(info.ID)),
			CreationTimestamp: info.CreationTime,
			ResourceVersion:   timeToRV(info.LastChangeTime),
		},
		Info: *info,
	}
}

func NewBugComments(id int, info *BugCommentInfo) *BugComments {
	return setFieldsFromBugComments(&BugComments{
		ObjectMeta: metav1.ObjectMeta{
			Name: strconv.Itoa(id),
			UID:  types.UID(strconv.Itoa(id)),
		},
		Comments: info.Comments,
	})
}

func setFieldsFromBugComments(bug *BugComments) *BugComments {
	var oldest, newest time.Time
	for _, comment := range bug.Comments {
		if oldest.IsZero() || comment.CreationTime.Time.Before(oldest) {
			oldest = comment.CreationTime.Time
		}
		if comment.Time.Time.After(newest) {
			newest = comment.CreationTime.Time
		}
	}
	bug.CreationTimestamp.Time = oldest
	bug.ResourceVersion = timeToRV(metav1.Time{Time: newest})
	return bug
}

func timeToRV(t metav1.Time) string {
	s, _ := t.MarshalQueryParameter()
	return s
}

func NewBugList(bugs *BugInfoList, includeFn func(*BugInfo) bool) *BugList {
	var change time.Time
	items := make([]Bug, 0, len(bugs.Bugs))
	for _, info := range bugs.Bugs {
		if includeFn != nil && !includeFn(&info) {
			continue
		}
		if t := info.LastChangeTime.Time; change.Before(t) {
			change = t
		}
		items = append(items, Bug{
			ObjectMeta: metav1.ObjectMeta{
				Name:              strconv.Itoa(info.ID),
				UID:               types.UID(strconv.Itoa(info.ID)),
				CreationTimestamp: info.CreationTime,
				ResourceVersion:   timeToRV(info.LastChangeTime),
			},
			Info: info,
		})
	}
	list := &BugList{Items: items}
	if !change.IsZero() {
		list.ResourceVersion = timeToRV(metav1.Time{Time: change})
	}
	return list
}

func (b Bug) DeepCopyObject() runtime.Object {
	copied := b
	copied.ObjectMeta = *b.ObjectMeta.DeepCopy()
	return &copied
}

func (b BugComments) DeepCopyObject() runtime.Object {
	copied := b
	copied.ObjectMeta = *b.ObjectMeta.DeepCopy()
	if b.Comments != nil {
		copied.Comments = make([]BugComment, len(b.Comments))
		copy(copied.Comments, b.Comments)
	}
	return &copied
}

func (b *BugList) DeepCopyObject() runtime.Object {
	copied := *b
	if b.Items != nil {
		copied.Items = make([]Bug, len(b.Items))
		for i := range b.Items {
			copied.Items[i] = *b.Items[i].DeepCopyObject().(*Bug)
		}
	}
	return &copied
}
