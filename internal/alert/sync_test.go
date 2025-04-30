package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func Test_syncer_Get(t *testing.T) {

	partialResponses := []bool{false, true}
	for _, partialResponse := range partialResponses {

		mClient := &mockAlertClient{}

		s, err := NewSyncer(mClient, logr.Discard(), prometheus.NewRegistry(), `labels["instance"] == FullName`, time.Minute)
		assert.NoError(t, err)

		response1 := response1()
		var alerts []promv1.Alert
		var fetchTime, before, after time.Time

		_, _, err = s.Get("node1")
		assert.EqualError(t, err, "cache is not yet ready")
		mClient.AssertExpectations(t)

		mClient.On("GetAlerts", mock.Anything).Return(response1, partialResponse, nil).Once()
		before = time.Now()
		s.SyncOnce()
		after = time.Now()
		alerts, fetchTime, err = s.Get("node1")
		assert.Nil(t, err)
		assert.EqualValues(t, response1, alerts)
		assert.True(t, fetchTime.Before(after))
		assert.True(t, fetchTime.After(before))
		mClient.AssertExpectations(t)

		alerts, _, err = s.Get("node2")
		assert.Nil(t, err)
		assert.Empty(t, alerts)
		mClient.AssertExpectations(t)

		mClient.On("GetAlerts", mock.Anything).Return(nil, partialResponse, errors.New("an error")).Once()
		before = time.Now()
		s.SyncOnce()
		after = time.Now()
		alerts, fetchTime, err = s.Get("node1")
		assert.Nil(t, alerts)
		assert.EqualError(t, err, "an error")
		assert.True(t, fetchTime.Before(after))
		assert.True(t, fetchTime.After(before))
		mClient.AssertExpectations(t)
	}
}

func response1() []promv1.Alert {
	return []promv1.Alert{
		{
			State: promv1.AlertStateFiring,
			Labels: model.LabelSet{
				"alertname": "HouseOnFire",
				"instance":  "node1",
			},
		},
	}
}

type mockAlertClient struct {
	mock.Mock
}

func (m *mockAlertClient) GetAlerts(ctx context.Context) ([]promv1.Alert, bool, error) {
	args := m.Called(ctx)
	resp := args.Get(0)
	if resp == nil {
		return nil, args.Bool(1), args.Error(2)
	}
	return args.Get(0).([]promv1.Alert), args.Bool(1), args.Error(2)
}

var _ Client = &mockAlertClient{}
