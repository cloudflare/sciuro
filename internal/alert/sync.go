package alert

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/prometheus/alertmanager/pkg/labels"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Syncer is an interface designed to be run by a manager.Manager
// that also provides a Cache interface
type Syncer interface {
	manager.LeaderElectionRunnable
	manager.Runnable
	Cache
	// SyncOnce enables the cache to be initialized before use by the Manager
	SyncOnce()
}

// Cache outlines an interface to interact with cached alerts
type Cache interface {
	// Get will return the currently cached alerts for a given node. An error
	// will be returned if the cache is not populated, node specific filters
	// cannot be run, or if the last retrieval resulted in an error. The time
	// returned is the time of the last retrieval attempt.
	Get(nodeName string) (models.GettableAlerts, time.Time, error)
}

type syncer struct {
	log               logr.Logger
	cacheNumAlerts    prometheus.Gauge
	alertsGetDuration prometheus.Histogram
	alertsGetFailures prometheus.Counter
	sync.RWMutex
	nodeFiltersTemplate *template.Template
	alertClient         alertClient
	receiver            string
	interval            time.Duration

	results       models.GettableAlerts
	retrievedAt   time.Time
	lastErr       error
	fetchSilenced bool
}

// NewSyncer provides an implementation of Syncer that gets alerts at syncInterval
func NewSyncer(
	alertClient alertClient,
	log logr.Logger,
	prom prometheus.Registerer,
	receiver,
	nodeTemplate string,
	syncInterval time.Duration,
	fetchSilenced bool,
) (Syncer, error) {
	nodeFiltersTemplate, err := template.New("filter").Parse(nodeTemplate)
	if err != nil {
		return nil, err
	}

	cacheNumAlerts := prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: "sync",
		Name:      "num_cached",
		Help:      "Number of alerts last cached",
	})

	alertsGetDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "sync",
		Name:      "get_duration",
		Help:      "Time to get alerts",
		Buckets:   []float64{0.5, 1, 2, 3, 4, 5, 10, 15, 30, 60, 120},
	})

	alertsGetFailures := prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: "sync",
		Name:      "get_failures",
		Help:      "Count of alerts get failures",
	})

	prom.MustRegister(
		cacheNumAlerts,
		alertsGetDuration,
		alertsGetFailures,
	)

	return &syncer{
		cacheNumAlerts:      cacheNumAlerts,
		log:                 log,
		alertsGetDuration:   alertsGetDuration,
		alertsGetFailures:   alertsGetFailures,
		nodeFiltersTemplate: nodeFiltersTemplate,
		alertClient:         alertClient,
		receiver:            receiver,
		interval:            syncInterval,
		fetchSilenced:       fetchSilenced,
	}, nil
}

type alertClient interface {
	GetAlerts(params *alert.GetAlertsParams) (*alert.GetAlertsOK, error)
}

func (s *syncer) NeedLeaderElection() bool {
	return false
}

func (s *syncer) Start(ctx context.Context) error {
	wait.JitterUntil(s.SyncOnce, s.interval, 1.2, false, ctx.Done())
	return nil
}

type nodeInfo struct {
	FullName  string
	ShortName string
}

func (s *syncer) Get(nodeName string) (models.GettableAlerts, time.Time, error) {
	s.RLock()
	defer s.RUnlock()
	if s.retrievedAt.IsZero() {
		return nil, s.retrievedAt, errors.New("cache is not yet ready")
	}

	if s.lastErr != nil {
		return nil, s.retrievedAt, s.lastErr
	}

	ni := &nodeInfo{
		FullName:  nodeName,
		ShortName: strings.Split(nodeName, ".")[0],
	}

	var buf bytes.Buffer

	if err := s.nodeFiltersTemplate.Execute(&buf, ni); err != nil {
		return nil, s.retrievedAt, err
	}

	matchers, err := labels.ParseMatchers(buf.String())
	if err != nil {
		return nil, s.retrievedAt, err
	}

	matchedAlerts := make([]*models.GettableAlert, 0, 1)
	for _, al := range s.results {
		if alertMatchesFilterLabels(al, matchers) {
			matchedAlerts = append(matchedAlerts, al)
		}
	}
	return matchedAlerts, s.retrievedAt, s.lastErr
}

func (s *syncer) SyncOnce() {
	s.Lock()
	defer s.Unlock()

	var active = true
	var silenced = s.fetchSilenced

	ctx, cancel := context.WithTimeout(context.Background(), s.interval)
	defer cancel()

	params := &alert.GetAlertsParams{
		Silenced: &silenced,
		Active:   &active,
		Receiver: &s.receiver,
		Context:  ctx,
	}

	timer := prometheus.NewTimer(s.alertsGetDuration)
	defer timer.ObserveDuration()
	var resp *alert.GetAlertsOK
	resp, s.lastErr = s.alertClient.GetAlerts(params)
	s.retrievedAt = time.Now()
	if s.lastErr != nil {
		s.log.Error(s.lastErr, "could not retrieve alerts")
		s.alertsGetFailures.Inc()
		s.results = nil
	} else {
		s.results = resp.Payload
		s.cacheNumAlerts.Set(float64(len(s.results)))
	}
}

var _ Syncer = &syncer{}
