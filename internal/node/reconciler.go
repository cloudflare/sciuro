package node

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/sciuro/internal/alert"
	"github.com/go-logr/logr"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	alertNameLabel         = "alertname"
	alertPriorityLabel     = "priority"
	defaultPriority        = 9
	alertSummaryAnnotation = "summary"
	reasonFiring           = "AlertIsFiring"
	reasonNotFiring        = "AlertIsNotFiring"
	reasonUnavailable      = "AlertsUnavailable"
	statusTrue             = "True"
	statusFalse            = "False"
	statusUnknown          = "Unknown"
)

type nodeStatusReconciler struct {
	c                   client.Client
	log                 logr.Logger
	resyncInterval      time.Duration
	reconcileTimeout    time.Duration
	linger              time.Duration
	alertCache          alert.Cache
	updateStatusCounter *prometheus.CounterVec
	conditionPrefix     string
}

var _ reconcile.Reconciler = &nodeStatusReconciler{}

// NewNodeStatusReconciler returns a reconcile.Reconciler that will PATCH the subresource
// node/status with updates to NodeConditions from alerts specific to the node. As alerts
// are not known ahead, the NodeConditionType is prefixed with conditionPrefix to allow the reconciler
// to distinguish NodeConditions it "owns" from those it does not. It will not modify non-"owned"
// NodeConditions.
//
// NodeConditions created from a given alert have the provided structure:
//
//	    	NodeCondition{
//			    Type:               conditionPrefix + $labels.alertname
//			    Status:             True - if firing,
//			                        False if not firing,
//			                        Unknown if alerts are unavailable
//			    LastHeartbeatTime:  currentTime,
//			    LastTransitionTime: currentTime if status changed,
//			    Reason:             One of "AlertIsFiring", "AlertIsNotFiring", "AlertsUnavailable"
//			    Message:            $annotations.summary if present
//		    }
//
// The linger option sets the minimum time a NodeCondition with a False Status will be retained.
// A NodeCondition that has been False for the entire linger duration will be removed from
// the node. Setting this to a zero duration disables this behavior.
func NewNodeStatusReconciler(
	c client.Client,
	log logr.Logger,
	prom prometheus.Registerer,
	resyncInterval,
	reconcileTimeout,
	linger time.Duration,
	ac alert.Cache,
	conditionPrefix string,
) reconcile.Reconciler {

	updateStatusCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "reconcile",
		Name:      "update_status",
		Help:      "Count of reconciler status changes",
	}, []string{"old_status", "new_status"})

	prom.MustRegister(updateStatusCounter)

	return &nodeStatusReconciler{
		c:                   c,
		log:                 log,
		resyncInterval:      resyncInterval,
		reconcileTimeout:    reconcileTimeout,
		linger:              linger,
		alertCache:          ac,
		updateStatusCounter: updateStatusCounter,
		conditionPrefix:     conditionPrefix,
	}
}

func (n *nodeStatusReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := n.log.WithValues("request", request)
	ctx, cancel := context.WithTimeout(ctx, n.reconcileTimeout)
	defer cancel()

	currentNode := &corev1.Node{}
	err := n.c.Get(ctx, request.NamespacedName, currentNode)
	if k8serrors.IsNotFound(err) {
		log.Error(err, "could not find Node")
		return reconcile.Result{}, nil
	}
	if err != nil {
		log.Error(err, "could not fetch Node")
		return reconcile.Result{}, err
	}
	desiredNode := currentNode.DeepCopy()
	if err := n.updateNodeStatuses(log, desiredNode); err != nil {
		log.Error(err, "could not update node status")
		return reconcile.Result{}, err
	}
	if equality.Semantic.DeepEqual(desiredNode, currentNode) {
		return reconcile.Result{RequeueAfter: n.resyncInterval}, nil
	}
	patch := client.MergeFrom(currentNode)
	if err := n.c.Status().Patch(ctx, desiredNode, patch); err != nil {
		log.Error(err, "could not patch node")
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: n.resyncInterval}, nil
}

func (n *nodeStatusReconciler) updateNodeStatuses(log logr.Logger, node *corev1.Node) error {
	alerts, currentTime, fetchErr := n.alertCache.Get(node.Name)
	current := v1.NewTime(currentTime)

	incomingConditions := make(map[corev1.NodeConditionType]*conditionAndPriority, len(alerts))
	// only if we have valid results (no err) will we need converted conditions
	if fetchErr == nil {
		for _, al := range alerts {
			condAndPriority, err := convertAlertToCondition(log, al, current, n.conditionPrefix)
			if err != nil {
				return err
			}
			existing, ok := incomingConditions[condAndPriority.condition.Type]
			// only overwrite if new condition is of higher priority
			if !ok || existing.priority > condAndPriority.priority {
				incomingConditions[condAndPriority.condition.Type] = condAndPriority
			}
		}
	}

	nonDeletedConditions := make([]corev1.NodeCondition, 0, len(node.Status.Conditions))
	for i := range node.Status.Conditions {
		existing := &node.Status.Conditions[i]
		if !strings.HasPrefix(string(existing.Type), n.conditionPrefix) {
			nonDeletedConditions = append(nonDeletedConditions, *existing)
			continue
		}

		condLog := log.WithValues("condition", existing.Type, "oldStatus", existing.Status)
		updatedAndPriority, updateExists := incomingConditions[existing.Type]

		// fetchErr present - mark conditions as Unknown
		if fetchErr != nil {
			if existing.Status != statusUnknown {
				existing.LastTransitionTime = current
				n.updateStatusCounter.WithLabelValues(string(existing.Status), statusUnknown).Inc()
				condLog.WithValues("newStatus", statusUnknown).Info("updating existing condition with new status")
				existing.Status = statusUnknown
			}
			existing.Reason = reasonUnavailable
			existing.Message = ""
			existing.LastHeartbeatTime = current
			nonDeletedConditions = append(nonDeletedConditions, *existing)
			continue
		}

		// alert is present - update accordingly
		if updateExists {
			updated := updatedAndPriority.condition
			existing.LastHeartbeatTime = updated.LastHeartbeatTime
			existing.Message = updated.Message
			existing.Reason = updated.Reason
			if existing.Status != updated.Status {
				n.updateStatusCounter.WithLabelValues(string(existing.Status), string(updated.Status)).Inc()
				condLog.WithValues("newStatus", updated.Status).Info("updating existing condition with new status")
				existing.Status = updated.Status
				existing.LastTransitionTime = updated.LastTransitionTime
			}
			nonDeletedConditions = append(nonDeletedConditions, *existing)
			incomingConditions[existing.Type] = nil
			continue
		}
		// else alert is not present - set status to false (or delete)
		if existing.Status != statusFalse {
			existing.LastTransitionTime = current
			n.updateStatusCounter.WithLabelValues(string(existing.Status), statusFalse).Inc()
			condLog.WithValues("newStatus", statusFalse).Info("updating existing condition with new status")
			existing.Status = statusFalse
		}
		existing.Reason = reasonNotFiring
		existing.Message = ""
		existing.LastHeartbeatTime = current
		if n.linger != 0 {
			if shouldDelete(existing, n.linger, current, n.conditionPrefix) {
				n.updateStatusCounter.WithLabelValues(string(existing.Status), "").Inc()
				condLog.Info("deleting lingering condition")
				continue
			}
		}
		nonDeletedConditions = append(nonDeletedConditions, *existing)
	}

	// for any remaining incoming conditions we haven't yet seen on the current conditions, append
	for _, incomingCondAndPriority := range incomingConditions {
		if incomingCondAndPriority == nil {
			continue
		}
		incomingCondition := incomingCondAndPriority.condition
		n.updateStatusCounter.WithLabelValues("", string(incomingCondition.Status)).Inc()
		condLog := log.WithValues("condition", incomingCondition.Type, "newStatus", incomingCondition.Status)
		condLog.Info("adding new condition")
		nonDeletedConditions = append(nonDeletedConditions, *incomingCondition)
	}

	node.Status.Conditions = nonDeletedConditions

	return nil
}

func shouldDelete(condition *corev1.NodeCondition, linger time.Duration, current v1.Time, conditionPrefix string) bool {
	return strings.HasPrefix(string(condition.Type), conditionPrefix) &&
		condition.Status == statusFalse &&
		current.Sub(condition.LastTransitionTime.Time) > linger
}

type conditionAndPriority struct {
	condition *corev1.NodeCondition
	priority  int
}

func convertAlertToCondition(olog logr.Logger, al promv1.Alert, currentTime v1.Time, conditionPrefix string) (*conditionAndPriority, error) {
	alertname := al.Labels[alertNameLabel]
	if alertname == "" {
		return nil, errors.New("no alertname label")
	}
	log := olog.WithValues("alertname", alertname)
	priority := defaultPriority
	rawPriority, ok := al.Labels[alertPriorityLabel]
	if ok {
		parsed, err := strconv.Atoi(string(rawPriority))
		if err != nil {
			return nil, errors.New("malformed alert priority")
		}
		priority = parsed
	} else {
		log.Info("No priority label present, using default priority")
	}
	message := fmt.Sprintf("[P%d]", priority)
	if summary, ok := al.Annotations[alertSummaryAnnotation]; ok {
		message = message + " " + string(summary)
	}
	condition := &corev1.NodeCondition{
		Type:               corev1.NodeConditionType(fmt.Sprintf("%s%s", conditionPrefix, alertname)),
		Status:             statusTrue,
		LastHeartbeatTime:  currentTime,
		LastTransitionTime: currentTime,
		Reason:             reasonFiring,
		Message:            message,
	}
	return &conditionAndPriority{
		condition: condition,
		priority:  priority,
	}, nil
}
