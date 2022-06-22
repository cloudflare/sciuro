package node

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cloudflare/sciuro/internal/alert"
	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	oldTime       = v1.Date(2020, 3, 18, 12, 33, 45, 0, time.UTC)
	reallyOldTime = v1.Date(2020, 3, 17, 12, 33, 45, 0, time.UTC)
	currentTime   = v1.Date(2020, 3, 18, 13, 17, 58, 0, time.UTC)
)

func Test_Reconcile(t *testing.T) {
	const resyncInterval = 2 * time.Minute
	tests := []struct {
		name        string
		updateMocks func(c *mockK8SClient, cache *mockAlertCache)
		want        reconcile.Result
		wantErr     bool
	}{
		{
			name: "update node",
			updateMocks: func(c *mockK8SClient, cache *mockAlertCache) {
				cache.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
				c.On("Get", mock.Anything, types.NamespacedName{Name: "node1"}, mock.Anything).Run(
					func(args mock.Arguments) {
						node := args.Get(2).(*corev1.Node)
						node.Name = "node1"
						node.Status.Conditions = []corev1.NodeCondition{
							{
								Type:   "Ready",
								Status: "True",
							},
						}
					},
				).Return(nil)
				match := func(obj interface{}) bool {
					return equality.Semantic.DeepEqual(obj, newNode(
						corev1.NodeCondition{
							Type:   "Ready",
							Status: "True",
						},
						corev1.NodeCondition{
							Status:             "True",
							Type:               "AlertManager_NodeOnFire",
							Reason:             "AlertIsFiring",
							Message:            "[P9] Node has erupted into fire at 500C",
							LastHeartbeatTime:  currentTime,
							LastTransitionTime: currentTime,
						},
					))
				}
				c.On("Patch", mock.Anything, mock.MatchedBy(match), mock.Anything, mock.Anything).Return(nil)
			},
			want:    reconcile.Result{RequeueAfter: resyncInterval},
			wantErr: false,
		},
		{
			name: "no update",
			updateMocks: func(c *mockK8SClient, cache *mockAlertCache) {
				cache.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
									"priority":  "3",
								},
							},
						},
					},
					oldTime.Time,
					nil,
				)
				c.On("Get", mock.Anything, types.NamespacedName{Name: "node1"}, mock.Anything).Run(
					func(args mock.Arguments) {
						node := args.Get(2).(*corev1.Node)
						node.Name = "node1"
						node.Status.Conditions = []corev1.NodeCondition{
							{
								Type:   "Ready",
								Status: "True",
							},
							{
								Status:             "True",
								Type:               "AlertManager_NodeOnFire",
								Reason:             "AlertIsFiring",
								Message:            "[P3] Node has erupted into fire at 500C",
								LastHeartbeatTime:  oldTime,
								LastTransitionTime: oldTime,
							},
						}
					},
				).Return(nil)
			},
			want:    reconcile.Result{RequeueAfter: resyncInterval},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "node1"},
			}
			c := &mockK8SClient{}
			ac := &mockAlertCache{}
			tt.updateMocks(c, ac)
			n := NewNodeStatusReconciler(c, logr.Discard(), prometheus.NewRegistry(), resyncInterval, time.Minute, time.Minute, ac)
			got, err := n.Reconcile(context.Background(), request)
			if (err != nil) != tt.wantErr {
				t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Reconcile() got = %v, want %v", got, tt.want)
			}
			mock.AssertExpectationsForObjects(t, c, ac)
		})
	}
}

func Test_updateNodeStatuses(t *testing.T) {

	tests := []struct {
		name       string
		node       *corev1.Node
		expected   *corev1.Node
		updateMock func(client *mockAlertCache)
		wantErr    bool
	}{
		{
			name: "test single add (no priority)",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P9] Node has erupted into fire at 500C",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: currentTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test single add (with priority)",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P5] Node has erupted into fire at 500C",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: currentTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
									"priority":  "5",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "missing alertname label",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
									"priority":  "blah",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
			wantErr: true,
		},
		{
			name: "malformed priority label",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"": "othervalue",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
			wantErr: true,
		},
		{
			name: "missing summary annotation",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P9]",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: currentTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"description": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "error when getting alerts (no existing)",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			expected: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					nil,
					currentTime.Time,
					errors.New("cannot get alerts"),
				)
			},
		},
		{
			name: "error when getting alerts (existing)",
			node: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "Node has erupted into fire at 500C",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "Unknown",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertsUnavailable",
					Message:            "",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: currentTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					nil,
					currentTime.Time,
					errors.New("cannot get alerts"),
				)
			},
		},
		{
			name: "test update heartbeat",
			node: newNode(
				corev1.NodeCondition{
					Status:             "True",
					Type:               "Ready",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P9] Node has erupted into fire at 500C",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status:             "True",
					Type:               "Ready",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P9] Node has erupted into fire at 500C",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: oldTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test update priority",
			node: newNode(
				corev1.NodeCondition{
					Status:             "True",
					Type:               "Ready",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P8] Node has erupted into fire at 500C",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status:             "True",
					Type:               "Ready",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "[P7] Node has erupted into fire at 500C",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: oldTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{
						&models.GettableAlert{
							Annotations: map[string]string{
								"summary": "Node has erupted into fire at 500C",
							},
							Alert: models.Alert{
								Labels: map[string]string{
									"alertname": "NodeOnFire",
									"priority":  "7",
								},
							},
						},
					},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test change status to False",
			node: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "True",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsFiring",
					Message:            "Node has erupted into fire at 500C",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					Message:            "",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: currentTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test false remains if linger timeout not reached",
			node: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: oldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					Message:            "",
					LastHeartbeatTime:  currentTime,
					LastTransitionTime: oldTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test false deleted if linger timeout reached (tail)",
			node: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
			),
			expected: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test false deleted if linger timeout reached (head)",
			node: newNode(
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
			),
			expected: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			}),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{},
					currentTime.Time,
					nil,
				)
			},
		},
		{
			name: "test false deleted if linger timeout reached (mixed)",
			node: newNode(corev1.NodeCondition{
				Status: "True",
				Type:   "Ready",
			},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "DiskPressure",
					Reason:             "NoDiskPressure",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "AlertManager_NodeOnFire",
					Reason:             "AlertIsNotFiring",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
			),
			expected: newNode(
				corev1.NodeCondition{
					Status: "True",
					Type:   "Ready",
				},
				corev1.NodeCondition{
					Status:             "False",
					Type:               "DiskPressure",
					Reason:             "NoDiskPressure",
					LastHeartbeatTime:  oldTime,
					LastTransitionTime: reallyOldTime,
				},
			),
			updateMock: func(client *mockAlertCache) {
				client.On("Get", "node1").Return(
					models.GettableAlerts{},
					currentTime.Time,
					nil,
				)
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAlertCache{}
			if tt.updateMock != nil {
				tt.updateMock(mockClient)
			}
			linger := time.Hour * 24

			r := &nodeStatusReconciler{
				c:                nil,
				log:              logr.Discard(),
				reconcileTimeout: time.Second,
				linger:           linger,
				alertCache:       mockClient,
				updateStatusCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
					Name: "test",
				}, []string{"old_status", "new_status"}),
			}
			if err := r.updateNodeStatuses(logr.Discard(), tt.node); (err != nil) != tt.wantErr {
				t.Errorf("updateNodeStatuses() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !equality.Semantic.DeepEqual(tt.expected, tt.node) {
				t.Errorf("updateNodeStatuses() diff = %v", cmp.Diff(tt.expected, tt.node))
			}
			mock.AssertExpectationsForObjects(t, mockClient)
		})
	}
}

type mockAlertCache struct {
	mock.Mock
}

func (m *mockAlertCache) Get(nodeName string) (models.GettableAlerts, time.Time, error) {
	args := m.Called(nodeName)
	alerts := args.Get(0)
	someTime := args.Get(1).(time.Time)
	if alerts == nil {
		return nil, someTime, args.Error(2)
	}
	return args.Get(0).(models.GettableAlerts), someTime, args.Error(2)
}

var _ alert.Cache = &mockAlertCache{}

func newNode(conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: v1.ObjectMeta{
			Name: "node1",
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
		},
	}
}

type mockK8SClient struct {
	mock.Mock
}

func (m *mockK8SClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	args := m.Called(ctx, key, obj)
	return args.Error(0)
}

func (m *mockK8SClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	args := m.Called(ctx, list, opts)
	return args.Error(0)
}

func (m *mockK8SClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

func (m *mockK8SClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

func (m *mockK8SClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

func (m *mockK8SClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	args := m.Called(ctx, obj, patch, opts)
	return args.Error(0)
}

func (m *mockK8SClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

func (m *mockK8SClient) Scheme() *runtime.Scheme {
	panic("implement me")
}

func (m *mockK8SClient) RESTMapper() meta.RESTMapper {
	panic("implement me")
}

func (m *mockK8SClient) Status() client.StatusWriter {
	return m
}

var _ client.Client = &mockK8SClient{}
var _ client.StatusWriter = &mockK8SClient{}
