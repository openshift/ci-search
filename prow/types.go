package prow

import (
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
)

type JobAccessor interface {
	Get(name string) (*Job, error)
	List(labels.Selector) ([]*Job, error)
	JobStats(name string, names sets.String, from, to time.Time) JobStats
}

var Empty JobAccessor = emptyJobAccessor{}

type emptyJobAccessor struct{}

func (emptyJobAccessor) Get(name string) (*Job, error) {
	return nil, errors.NewNotFound(prowGR, name)
}
func (emptyJobAccessor) List(_ labels.Selector) ([]*Job, error) {
	return nil, nil
}
func (emptyJobAccessor) JobStats(name string, names sets.String, from, to time.Time) JobStats {
	return JobStats{}
}

type JobList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []*Job `json:"items"`
}

type Job struct {
	metav1.TypeMeta
	metav1.ObjectMeta `json:"metadata"`

	Spec   JobSpec   `json:"spec"`
	Status JobStatus `json:"status"`
}

type JobSpec struct {
	Type string `json:"type"`
	Job  string `json:"job"`
}

type JobStatus struct {
	// Valid states are "aborted", "error", "failure", "success"
	State          string      `json:"state"`
	StartTime      metav1.Time `json:"startTime"`
	CompletionTime metav1.Time `json:"completionTime"`
	URL            string      `json:"url"`
	BuildID        string      `json:"build_id"`
}

func (j Job) DeepCopyObject() runtime.Object {
	copied := j
	copied.ObjectMeta = *j.ObjectMeta.DeepCopy()
	return &copied
}

func (j *JobList) DeepCopyObject() runtime.Object {
	copied := *j
	if j.Items != nil {
		copied.Items = make([]*Job, len(j.Items))
		for i := range j.Items {
			copied.Items[i] = j.Items[i].DeepCopyObject().(*Job)
		}
	}
	return &copied
}
