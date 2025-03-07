package main

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v9"
	"github.com/cloudflare/sciuro/internal/alert"
	"github.com/cloudflare/sciuro/internal/node"
	corev1 "k8s.io/api/core/v1"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type config struct {
	// AlertmanagerURL is the url for the Alertmanager instance to sync from
	AlertmanagerURL string `env:"SCIURO_ALERTMANAGER_URL"`
	// PrometheusURLs is a list of Prometheus urls to sync from
	PrometheusURLs []string `env:"SCIURO_PROMETHEUS_URLS"`
	// MetricsAddr is the address and port to serve metrics from
	MetricsAddr string `env:"SCIURO_METRICS_ADDR" envDefault:"0.0.0.0:8080"`
	// AlertCacheTTL is the time between fetching alerts
	AlertCacheTTL time.Duration `env:"SCIURO_ALERT_CACHE_TTL" envDefault:"60s"`
	// NodeResync is the period at which a node fully syncs with the current alerts
	NodeResync time.Duration `env:"SCIURO_NODE_RESYNC" envDefault:"2m"`
	// DevMode toggles additional logging information
	DevMode bool `env:"SCIURO_DEV_MODE" envDefault:"false"`
	// ReconcileTimeout is the maximum time given to reconcile a node.
	ReconcileTimeout time.Duration `env:"SCIURO_RECONCILE_TIMEOUT" envDefault:"45s"`
	// MaxConcurrentReconciles is the maximum number of nodes which can be
	// reconciled concurrently.
	MaxConcurrentReconciles int `env:"SCIURO_MAX_CONCURRENT_RECONCILES" envDefault:"1"`
	// AlertReceiver is the receiver to use for server-side filtering of alerts
	// must be the same across all targeted nodes in the cluster
	AlertReceiver string `env:"SCIURO_ALERT_RECEIVER"`
	// AlertSilenced controls whether silenced alerts are retrieved from alertmanager
	AlertSilenced bool `env:"SCIURO_ALERT_SILENCED" envDefault:"false"`
	// CelExpression is a Common Expression Language expression that runs against each alert.
	// `labels` is a map representing the prometheus labels of the alert.
	// There are two other valid variables available for substitution:
	// `FullName` and `ShortName` where `ShortName` is `FullName` up to the first . (dot)
	CelExpression string `env:"SCIURO_CEL_EXPRESSION,required"`
	// LeaderElectionNamespace is the namespace where the leader election config map will be
	// managed. Defaults to the current namespace.
	LeaderElectionNamespace string `env:"SCIURO_LEADER_NAMESPACE"`
	// LeaderElectionID is the name of the configmap used to manage leader elections
	LeaderElectionID string `env:"SCIURO_LEADER_ID" envDefault:"sciuro-leader"`
	// LingerResolvedDuration is the time that non-firing alerts are kept as conditions
	// with the False status. After this time, the condition will be removed entirely.
	// A value of 0 will never remove these conditions.
	LingerResolvedDuration time.Duration `env:"SCIURO_LINGER_DURATION" envDefault:"96h"`
	// NodeConditionPrefix is the prefix for type of node condition.
	NodeConditionPrefix string `env:"SCIURO_NODE_CONDITION_PREFIX" envDefault:"AlertManager_"`
}

const name = "sciuro"

var log = logf.Log.WithName(name)

func main() {
	cfg := &config{}
	if err := env.Parse(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse config: %v\n", err)
		os.Exit(1)
	}

	logf.SetLogger(zap.New(zap.UseDevMode(cfg.DevMode), zap.WriteTo(os.Stderr)))
	entryLog := log.WithName("entrypoint")

	mgr, err := manager.New(clientconfig.GetConfigOrDie(), manager.Options{
		LeaderElection:          true,
		LeaderElectionID:        cfg.LeaderElectionID,
		LeaderElectionNamespace: cfg.LeaderElectionNamespace,
	})
	if err != nil {
		entryLog.Error(err, "unable to set up overall controller manager")
		os.Exit(1)
	}

	var as alert.Syncer
	{
		var client alert.Client
		if cfg.AlertmanagerURL != "" {
			if cfg.AlertReceiver == "" {
				entryLog.Error(err, "receiver must be set when using alertmanager")
				os.Exit(1)
			}
			var err error
			client, err = alert.NewAlertmanagerClient(cfg.AlertmanagerURL, cfg.AlertReceiver, cfg.AlertSilenced)
			if err != nil {
				entryLog.Error(err, "unable to setup alertmanager client")
				os.Exit(1)
			}
		} else if cfg.PrometheusURLs != nil {
			var err error
			client, err = alert.NewPrometheusMultiClient(cfg.PrometheusURLs)
			if err != nil {
				entryLog.Error(err, "unable to setup prometheus api client(s)")
				os.Exit(1)
			}
		} else {
			entryLog.Error(err, "must specify either alertmanager url or prometheus url(s)")
			os.Exit(1)
		}
		as, err = alert.NewSyncer(
			client,
			log.WithName("syncer"),
			metrics.Registry,
			cfg.CelExpression,
			cfg.AlertCacheTTL,
		)
		if err != nil {
			entryLog.Error(err, "unable to parse template")
			os.Exit(1)
		}
		as.SyncOnce()
		err = mgr.Add(as)
		if err != nil {
			entryLog.Error(err, "unable to add runnable to mgr")
			os.Exit(1)
		}
	}

	{
		r := node.NewNodeStatusReconciler(
			mgr.GetClient(),
			log.WithName("reconciler"),
			metrics.Registry,
			cfg.NodeResync,
			cfg.ReconcileTimeout,
			cfg.LingerResolvedDuration,
			as,
			cfg.NodeConditionPrefix,
		)

		c, err := controller.New("node-status-controller", mgr, controller.Options{
			MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
			Reconciler:              r,
		})
		if err != nil {
			entryLog.Error(err, "unable to set up individual controller")
			os.Exit(1)
		}

		// Watch Nodes and enqueue object key
		if err := c.Watch(source.Kind(mgr.GetCache(), &corev1.Node{}, &handler.TypedEnqueueRequestForObject[*corev1.Node]{})); err != nil {
			entryLog.Error(err, "unable to watch Nodes")
			os.Exit(1)
		}
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		entryLog.Error(err, "unable to run manager")
		os.Exit(1)
	}
}
