package alert

import (
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

func Test_syncer_Get(t *testing.T) {

	mClient := &mockAlertClient{}

	s, err := NewSyncer(mClient, logr.Discard(), metrics.Registry, "re", "instance={{.FullName}}", time.Minute, true)
	assert.NoError(t, err)

	response1 := response1()
	var alerts models.GettableAlerts
	var fetchTime, before, after time.Time

	_, _, err = s.Get("node1")
	assert.EqualError(t, err, "cache is not yet ready")
	mClient.AssertExpectations(t)

	getParamsMatcher := func(params *alert.GetAlertsParams) bool {
		return *params.Active && *params.Silenced
	}

	mClient.On("GetAlerts", mock.MatchedBy(getParamsMatcher), mock.Anything).Return(response1, nil).Once()
	before = time.Now()
	s.SyncOnce()
	after = time.Now()
	alerts, fetchTime, err = s.Get("node1")
	assert.Nil(t, err)
	assert.EqualValues(t, response1.Payload, alerts)
	assert.True(t, fetchTime.Before(after))
	assert.True(t, fetchTime.After(before))
	mClient.AssertExpectations(t)

	alerts, _, err = s.Get("node2")
	assert.Nil(t, err)
	assert.Empty(t, alerts)
	mClient.AssertExpectations(t)

	mClient.On("GetAlerts", mock.Anything, mock.Anything).Return(nil, errors.New("an error")).Once()
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

func response1() *alert.GetAlertsOK {
	state := "active"
	return &alert.GetAlertsOK{
		Payload: []*models.GettableAlert{
			{
				Status: &models.AlertStatus{
					State: &state,
				},
				Alert: models.Alert{
					Labels: map[string]string{
						"alertname": "HouseOnFire",
						"instance":  "node1",
					},
				},
			},
		},
	}
}

type mockAlertClient struct {
	mock.Mock
}

func (m *mockAlertClient) GetAlerts(params *alert.GetAlertsParams, opts ...alert.ClientOption) (*alert.GetAlertsOK, error) {
	args := m.Called(params, opts)
	resp := args.Get(0)
	if resp == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*alert.GetAlertsOK), args.Error(1)
}

var _ alertClient = &mockAlertClient{}
