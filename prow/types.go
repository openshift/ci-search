package prow

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

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
