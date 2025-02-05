
![Sciuro](img/sciuro.png "Sciuro")

* [Introduction](#introduction)
* [Requirements](#requirements)

# Introduction

Sciuro is a bridge between Alertmanager or Prometheus and Kubernetes to sync alerts as Node
Conditions. It is designed to work in tandem with other controllers that
observe Node Conditions such as [draino](https://github.com/planetlabs/draino)
or [Cluster API](https://cluster-api.sigs.k8s.io/tasks/automated-machine-management/healthchecking).

# Requirements

* Alertmanager API v2 or Prometheus API v1
* Kubernetes 1.12+

# Deployment
1. Download the manifests from the latest Github release

```
wget https://github.com/cloudflare/sciuro/releases/latest/download/cluster.yaml
wget https://github.com/cloudflare/sciuro/releases/latest/download/stable.yaml
```

2. Apply the cluster-scoped resources that allow Sciuro to read nodes and
   modify their status. If you choose a different namespace, adjust the
   namespace name and `ClusterRoleBinding` accordingly.

```
# Review manifests and make adjustments for different namespace
kubectl apply -f cluster.yaml
```

3. Edit the `sciuro` ConfigMap referencing the [Sciuro Configuration](#sciruo-configuration) section below. Apply the namespaced resources.  

```
# Review manifests and make adjustments to config map
kubectl apply -f stable.yaml
```

## Sciuro Configuration
The following environment variables can be set to configure Sciuro. Modifying
the supplied ConfigMap will set the environment variables on the Deployment.

### Alerts Fetch Configuration

You must set the URL for the Alertmanager or Prometheus instance(s) to sync from. In addition,
filtering should be configured both on a global level and for each specific
node. The Alertmanager
[receiver](https://prometheus.io/docs/alerting/latest/configuration/#receiver)
should be set to filter globally, while the node filters are set for matching
alerts to a specific node.

```
# AlertmanagerURL is the url for the Alertmanager instance to sync from
SCIURO_ALERTMANAGER_URL: "https://CHANGEME.example.com"

#PrometheusURLs is a list of Prometheus urls to sync from
SCIURO_PROMETHEUS_URLS: "https://CHANGEME.example.com,https://CHANGEME2.example.com"

# AlertReceiver is the receiver to use for server-side filtering of alerts
# must be the same across all targeted nodes in the cluster
SCIURO_ALERT_RECEIVER: "CHANGEME"

# CEL_EXPRESSION is a Common Expression Language expression that runs against each alert.
# `labels` is a map representing the prometheus labels of the alert.
# There are two other valid variables available for substitution:
# `FullName` and `ShortName` where `ShortName` is `FullName` up to the first . (dot)
SCIURO_CEL_EXPRESSION: `"node" in labels && (labels["node"] == FullName || labels["node"] == ShortName)`
```

Some additional optional settings are as follows:
```
# AlertSilenced controls whether silenced alerts are retrieved from Alertmanager
SCIURO_ALERT_SILENCED: "false"

# AlertCacheTTL is the time between fetching alerts
SCIURO_ALERT_CACHE_TTL: "60s"
```

### Reconciliation Configuration

The following are optional settings to configure how reconciliation with the
Kubernetes node resources behaves

```
# NodeResync is the period at which a node fully syncs with the current alerts
SCIURO_NODE_RESYNC: "2m"

# ReconcileTimeout is the maximum time given to reconcile a node.
SCIURO_RECONCILE_TIMEOUT: "45s"

# LingerResolvedDuration is the time that non-firing alerts are kept as conditions
# with the False status. After this time, the condition will be removed entirely.
# A value of 0 will never remove these conditions.
SCIURO_LINGER_DURATION: "96h"
```

### Miscellaneous Configuration

To change the address and port to serve metrics from:
```
# MetricsAddr is the address and port to serve metrics from
SCIURO_METRICS_ADDR: "0.0.0.0:8080"

# DevMode toggles additional logging information
SCIURO_DEV_MODE: "false"

# LeaderElectionNamespace is the namespace where the leader election config map will be
# managed. Defaults to the current namespace.
SCIURO_LEADER_NAMESPACE: ""

# LeaderElectionID is the name of the configmap used to manage leader elections
SCIURO_LEADER_ID: "sciuro-leader"
```

## Alertmanager Configuration
Sciuro is recommended to have its own Alertmanager
[receiver](https://prometheus.io/docs/alerting/latest/configuration/#receiver).
Since Sciuro works in a pull model currently, this receiver does not need to
push anywhere and can simply be an empty receiver. In addition, a
[route](https://prometheus.io/docs/alerting/latest/configuration/#route) needs
to be setup to match alerts to this receiver. There are many configurations that
will achieve the above, however the below is one example partial Alertmanager
configuration that allows alerts with a `notify: node-condition-k8s` label to
be picked up by Sciuro:

```
route:
  routes:
    - match_re:
        notify: (?:.*\s+)?node-condition-k8s(?:\s+.*)?
      receiver: node-condition-k8s
      continue: true

receivers:
  - name: node-condition-k8s
```

## Prometheus Configuration
When using Prometheus as an input source,
a more complex CEL expression is recommended since Prometheus
does not have the concept of silences or receivers.
A typical expression when using the Prometheus configuration may look like this:
```
labels["node"] == FullName && labels["notify"].contains("node-condition-k8s")
```

You may also want to drop the alerts with a particular receiver.
Example Prometheus configuration:
```
      alertmanagers:
        - alert_relabel_configs:
            - action: drop
              regex: node-condition-k8s
              source_labels:
                - notify
```

# Creating alerts
Assuming Prometheus as a source of alerts, an alert like the following can be
created to add a condition to nodes for high uptime:
```
alert: NodeUpTooLong
expr: (time() - node_boot_time_seconds) / 60 / 60 / 24 > 7
labels:
  notify: node-condition-k8s
  priority: "8"
annotations:
  description: Node '{{ $labels.instance }}' has been up for more than 7 days
  summary: Node '{{ $labels.instance }}' uptime too long
```

With this alert in place, conditions are added to the affected nodes:
```
$ kubectl get node worker01 -o json | jq '.status.conditions[] | select(.type | test("^AlertManager_"))'
{
  "lastHeartbeatTime": "2021-06-16T16:07:10Z",
  "lastTransitionTime": "2021-06-16T15:34:07Z",
  "message": "[P8] Node 'worker01' uptime too long",
  "reason": "AlertIsFiring",
  "status": "True",
  "type": "AlertManager_NodeUpTooLong"
}
```

# Building
Sciuro is built and tested with [bazel](https://bazel.build/). To run tests:
```
make test
```

To build and push images, define the docker repository base with the run of the
manifests targets:
```
bazel run --define repo=quay.io/myrepo  //manifests:cluster > /tmp/cluster.yaml
bazel run --define repo=quay.io/myrepo  //manifests:stable > /tmp/stable.yaml
```
