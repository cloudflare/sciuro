// Copyright 2018 Prometheus Team
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package alert

import (
	"fmt"
	"testing"

	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/prometheus/alertmanager/pkg/labels"
	"github.com/stretchr/testify/require"
)

func TestAlertFiltering(t *testing.T) {
	type test struct {
		alert    *models.GettableAlert
		msg      string
		expected bool
	}

	// Equal
	equal, err := labels.NewMatcher(labels.MatchEqual, "label1", "test1")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	tests := []test{
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test1"}}}, "label1=test1", true},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test2"}}}, "label1=test2", false},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label2": "test2"}}}, "label2=test2", false},
	}

	for _, test := range tests {
		actual := alertMatchesFilterLabels(test.alert, []*labels.Matcher{equal})
		msg := fmt.Sprintf("Expected %t for %s", test.expected, test.msg)
		require.Equal(t, test.expected, actual, msg)
	}

	// Not Equal
	notEqual, err := labels.NewMatcher(labels.MatchNotEqual, "label1", "test1")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	tests = []test{
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test1"}}}, "label1!=test1", false},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test2"}}}, "label1!=test2", true},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label2": "test2"}}}, "label2!=test2", true},
	}

	for _, test := range tests {
		actual := alertMatchesFilterLabels(test.alert, []*labels.Matcher{notEqual})
		msg := fmt.Sprintf("Expected %t for %s", test.expected, test.msg)
		require.Equal(t, test.expected, actual, msg)
	}

	// Regexp Equal
	regexpEqual, err := labels.NewMatcher(labels.MatchRegexp, "label1", "tes.*")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	tests = []test{
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test1"}}}, "label1=~test1", true},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test2"}}}, "label1=~test2", true},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label2": "test2"}}}, "label2=~test2", false},
	}

	for _, test := range tests {
		actual := alertMatchesFilterLabels(test.alert, []*labels.Matcher{regexpEqual})
		msg := fmt.Sprintf("Expected %t for %s", test.expected, test.msg)
		require.Equal(t, test.expected, actual, msg)
	}

	// Regexp Not Equal
	regexpNotEqual, err := labels.NewMatcher(labels.MatchNotRegexp, "label1", "tes.*")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	tests = []test{
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test1"}}}, "label1!~test1", false},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label1": "test2"}}}, "label1!~test2", false},
		{&models.GettableAlert{Alert: models.Alert{Labels: models.LabelSet{"label2": "test2"}}}, "label2!~test2", true},
	}

	for _, test := range tests {
		actual := alertMatchesFilterLabels(test.alert, []*labels.Matcher{regexpNotEqual})
		msg := fmt.Sprintf("Expected %t for %s", test.expected, test.msg)
		require.Equal(t, test.expected, actual, msg)
	}
}

func TestMultipleMatcherAlertFiltering(t *testing.T) {
	type test struct {
		alert    *models.GettableAlert
		matchers []*labels.Matcher
		msg      string
		expected bool
	}

	matcher1, err := labels.NewMatcher(labels.MatchEqual, "label1", "test1")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	matcher1Opposite, err := labels.NewMatcher(labels.MatchNotEqual, "label1", "test1")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	matcher1Complement, err := labels.NewMatcher(labels.MatchEqual, "label2", "test2")
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}

	tests := []test{
		{
			&models.GettableAlert{
				Alert: models.Alert{
					Labels: models.LabelSet{"label1": "test1"},
				},
			},
			[]*labels.Matcher{
				matcher1,
				matcher1Opposite,
			},
			"label1=test1",
			true,
		},
		{
			&models.GettableAlert{
				Alert: models.Alert{
					Labels: models.LabelSet{"label1": "test2"},
				},
			},
			[]*labels.Matcher{
				matcher1,
				matcher1Complement,
			},
			"label1=test2",
			false,
		},
		{
			&models.GettableAlert{
				Alert: models.Alert{
					Labels: models.LabelSet{"label2": "test2"},
				},
			},
			[]*labels.Matcher{
				matcher1,
				matcher1Complement,
			},
			"label2=test2",
			true,
		},
		{
			&models.GettableAlert{
				Alert: models.Alert{
					Labels: models.LabelSet{
						"label2": "test2",
						"label1": "test2",
					},
				},
			},
			[]*labels.Matcher{
				matcher1,
				matcher1Complement,
			},
			"label1=test2,label2=test2",
			true,
		},
	}

	for _, test := range tests {
		actual := alertMatchesFilterLabels(test.alert, test.matchers)
		msg := fmt.Sprintf("Expected %t for %s", test.expected, test.msg)
		require.Equal(t, test.expected, actual, msg)
	}
}
