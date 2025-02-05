package alert

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/prometheus/alertmanager/api/v2/client"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/prometheus/alertmanager/cli"
	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
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
	Get(nodeName string) ([]promv1.Alert, time.Time, error)
}

type syncer struct {
	log               logr.Logger
	cacheNumAlerts    prometheus.Gauge
	alertsGetDuration prometheus.Histogram
	alertsGetFailures prometheus.Counter
	sync.RWMutex
	program     cel.Program
	alertClient Client
	interval    time.Duration
	results     []promv1.Alert
	retrievedAt time.Time
	lastErr     error
}

// NewSyncer provides an implementation of Syncer that gets alerts at syncInterval
func NewSyncer(
	alertClient Client,
	log logr.Logger,
	prom prometheus.Registerer,
	celExpression string,
	syncInterval time.Duration,
) (Syncer, error) {
	env, err := cel.NewEnv(
		cel.Declarations(
			decls.NewVar("labels", decls.NewMapType(decls.String, decls.String)),
			decls.NewVar("FullName", decls.String),
			decls.NewVar("ShortName", decls.String),
		),
	)
	if err != nil {
		return nil, err
	}

	ast, issues := env.Compile(celExpression)
	if err := issues.Err(); err != nil {
		return nil, err
	}
	program, err := env.Program(ast)
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
		cacheNumAlerts:    cacheNumAlerts,
		log:               log,
		alertsGetDuration: alertsGetDuration,
		alertsGetFailures: alertsGetFailures,
		program:           program,
		alertClient:       alertClient,
		interval:          syncInterval,
	}, nil
}

type Client interface {
	GetAlerts(context.Context) ([]promv1.Alert, bool, error)
}

func (s *syncer) NeedLeaderElection() bool {
	return false
}

func (s *syncer) Start(ctx context.Context) error {
	wait.JitterUntil(s.SyncOnce, s.interval, 1.2, false, ctx.Done())
	return nil
}

func (s *syncer) Get(nodeName string) ([]promv1.Alert, time.Time, error) {
	s.RLock()
	defer s.RUnlock()
	if s.retrievedAt.IsZero() {
		return nil, s.retrievedAt, errors.New("cache is not yet ready")
	}

	if s.lastErr != nil {
		return nil, s.retrievedAt, s.lastErr
	}

	matchedAlerts := make([]promv1.Alert, 0, 1)
	for _, al := range s.results {
		out, _, err := s.program.Eval(map[string]any{
			"labels":    al.Labels,
			"FullName":  nodeName,
			"ShortName": strings.Split(nodeName, ".")[0],
		})
		if err != nil {
			return nil, s.retrievedAt, err
		}
		matched, ok := out.Value().(bool)
		if !ok {
			return nil, s.retrievedAt, fmt.Errorf("result of provided CLE expression was not a boolean")
		}
		if matched {
			matchedAlerts = append(matchedAlerts, al)
		}
	}
	return matchedAlerts, s.retrievedAt, s.lastErr
}

func (s *syncer) SyncOnce() {
	s.Lock()
	defer s.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.interval)
	defer cancel()

	timer := prometheus.NewTimer(s.alertsGetDuration)
	defer timer.ObserveDuration()
	var resp []promv1.Alert
	var partial bool
	resp, partial, s.lastErr = s.alertClient.GetAlerts(ctx)
	s.retrievedAt = time.Now()
	// only store results on partial or full results
	if partial || s.lastErr == nil {
		s.results = resp
		s.cacheNumAlerts.Set(float64(len(s.results)))
	} else {
		// clear the cache on non-partial failures
		s.results = nil
	}
	// surface sync errors
	if s.lastErr != nil {
		s.log.Error(s.lastErr, "could not retrieve all alerts")
		s.alertsGetFailures.Inc()
	}
}

// Get alerts from a single prometheus
type PromClient struct {
	api promv1.API
}

func NewPrometheusClient(address string) (Client, error) {
	c, err := api.NewClient(api.Config{
		Address: address,
	})
	return &PromClient{
		api: promv1.NewAPI(c),
	}, err
}

func (p *PromClient) GetAlerts(ctx context.Context) ([]promv1.Alert, bool, error) {
	partial := false // does not apply to a single prometheus
	alerts, err := p.api.Alerts(ctx)
	if err != nil {
		return nil, partial, err
	}
	filteredAlerts := make([]promv1.Alert, 0)
	for _, alert := range alerts.Alerts {
		// api/v1/alerts does not accept query parameters
		// filter out alerts that are not firing (e.g. pending)
		if alert.State != promv1.AlertStateFiring {
			continue
		}
		filteredAlerts = append(filteredAlerts, alert)
	}
	return filteredAlerts, partial, nil
}

// Get alerts from Alertmanager
type AlertmanagerClient struct {
	client   *client.AlertmanagerAPI
	receiver string
	silenced bool
}

func NewAlertmanagerClient(address, receiver string, silenced bool) (*AlertmanagerClient, error) {
	parsedURL, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	cli.NewAlertmanagerClient(parsedURL)
	return &AlertmanagerClient{
		client:   cli.NewAlertmanagerClient(parsedURL),
		receiver: receiver,
		silenced: silenced,
	}, nil
}

// Get alerts from multiple promethei and combine them
type PromMultiClient struct {
	clients []PromClient
}

func NewPrometheusMultiClient(addresses []string) (Client, error) {
	clients := make([]PromClient, 0, len(addresses))
	for _, address := range addresses {
		c, err := api.NewClient(api.Config{
			Address: address,
		})
		if err != nil {
			return nil, err
		}
		clients = append(clients, PromClient{
			api: promv1.NewAPI(c)},
		)
	}

	return &PromMultiClient{clients: clients}, nil
}

func (p *PromMultiClient) GetAlerts(ctx context.Context) ([]promv1.Alert, bool, error) {
	// Get alerts for each prometheus client
	allAlerts := make([]promv1.Alert, 0)
	var allErrs error
	partial := false

	for _, client := range p.clients {
		alerts, _, err := client.GetAlerts(ctx)
		if err != nil {
			allErrs = errors.Join(allErrs, err)
			partial = true
		} else {
			allAlerts = append(allAlerts, alerts...)
		}
	}
	return allAlerts, partial, allErrs
}

func (a *AlertmanagerClient) GetAlerts(ctx context.Context) ([]promv1.Alert, bool, error) {
	active := true
	partial := false
	alerts, err := a.client.Alert.GetAlerts(&alert.GetAlertsParams{
		Silenced: &a.silenced,
		Active:   &active,
		Receiver: &a.receiver,
		Context:  ctx,
	})
	if err != nil {
		return nil, partial, err
	}

	// convert AlertManager alerts into the prometheus alert structure
	filteredAlerts := make([]promv1.Alert, 0, len(alerts.Payload))
	for _, alert := range alerts.Payload {
		filteredAlerts = append(filteredAlerts, promv1.Alert{
			Annotations: convertToLabelSet(alert.Annotations),
			Labels:      convertToLabelSet(alert.Labels),
			State:       promv1.AlertStateFiring,
		})
	}
	return filteredAlerts, partial, nil
}

func convertToLabelSet(input models.LabelSet) model.LabelSet {
	res := make(model.LabelSet, len(input))
	for k, v := range input {
		res[model.LabelName(k)] = model.LabelValue(v)
	}
	return res
}

var _ Syncer = &syncer{}
