/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resultstore

import (
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/duration"
	"github.com/golang/protobuf/ptypes/timestamp"
	resultstore "google.golang.org/genproto/googleapis/devtools/resultstore/v2"
)

func TestFromTiming(t *testing.T) {
	cases := []struct {
		name string
		t    *resultstore.Timing
		when time.Time
		dur  time.Duration
	}{
		{
			name: "basically works",
		},
		{
			name: "only StartTime works",
			t: &resultstore.Timing{
				StartTime: &timestamp.Timestamp{
					Seconds: 15,
					Nanos:   7,
				},
			},
			when: time.Unix(15, 7),
		},
		{
			name: "only Duration works",
			t: &resultstore.Timing{
				Duration: &duration.Duration{
					Seconds: 3,
					Nanos:   4,
				},
			},
			dur: 3*time.Second + 4*time.Nanosecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			when, dur := fromTiming(tc.t)
			if !when.Equal(tc.when) {
				t.Errorf("when: %v != expected %v", when, tc.when)
			}
			if dur != tc.dur {
				t.Errorf("dur: %v != expected %v", dur, tc.dur)
			}
		})
	}
}
