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

	mockAlertClient := &mockAlertClient{}

	s, err := NewSyncer(mockAlertClient, logr.Discard(), metrics.Registry, "re", "instance={{.FullName}}", time.Minute, true)
	assert.NoError(t, err)

	response1 := response1()
	var alerts models.GettableAlerts
	var fetchTime, before, after time.Time

	_, _, err = s.Get("node1")
	assert.EqualError(t, err, "cache is not yet ready")
	mockAlertClient.AssertExpectations(t)

	getParamsMatcher := func(params *alert.GetAlertsParams) bool {
		return *params.Active && *params.Silenced
	}

	mockAlertClient.On("GetAlerts", mock.MatchedBy(getParamsMatcher)).Return(response1, nil).Once()
	before = time.Now()
	s.SyncOnce()
	after = time.Now()
	alerts, fetchTime, err = s.Get("node1")
	assert.Nil(t, err)
	assert.EqualValues(t, response1.Payload, alerts)
	assert.True(t, fetchTime.Before(after))
	assert.True(t, fetchTime.After(before))
	mockAlertClient.AssertExpectations(t)

	alerts, fetchTime, err = s.Get("node2")
	assert.Nil(t, err)
	assert.Empty(t, alerts)
	mockAlertClient.AssertExpectations(t)

	mockAlertClient.On("GetAlerts", mock.Anything).Return(nil, errors.New("an error")).Once()
	before = time.Now()
	s.SyncOnce()
	after = time.Now()
	alerts, fetchTime, err = s.Get("node1")
	assert.Nil(t, alerts)
	assert.EqualError(t, err, "an error")
	assert.True(t, fetchTime.Before(after))
	assert.True(t, fetchTime.After(before))
	mockAlertClient.AssertExpectations(t)

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

func (m *mockAlertClient) GetAlerts(params *alert.GetAlertsParams) (*alert.GetAlertsOK, error) {
	args := m.Called(params)
	resp := args.Get(0)
	if resp == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*alert.GetAlertsOK), args.Error(1)
}

var _ alertClient = &mockAlertClient{}
