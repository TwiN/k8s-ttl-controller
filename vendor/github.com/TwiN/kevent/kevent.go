package kevent

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	typedv1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
)

type EventManager struct {
	kubernetesClient kubernetes.Interface
	component        string

	broadcaster record.EventBroadcaster
	recorder    record.EventRecorder

	scheme *runtime.Scheme
}

// NewEventManager creates a new EventManager with the given parameters.
func NewEventManager(kubernetesClient kubernetes.Interface, component string) *EventManager {
	em := &EventManager{
		kubernetesClient: kubernetesClient,
		component:        component,
		scheme:           runtime.NewScheme(),
	}
	em.broadcaster = record.NewBroadcaster()
	em.broadcaster.StartRecordingToSink(&typedv1core.EventSinkImpl{Interface: kubernetesClient.CoreV1().Events("")})
	em.recorder = em.broadcaster.NewRecorder(em.scheme, corev1.EventSource{Component: component})
	return em
}

// Create creates a Kubernetes Event with the given parameters.
func (em *EventManager) Create(resourceNamespace, resourceKind, resourceName, reason, message string, isWarning bool) {
	var eventType string
	if isWarning {
		eventType = corev1.EventTypeWarning
	} else {
		eventType = corev1.EventTypeNormal
	}
	us := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": resourceKind,
			"metadata": map[string]interface{}{
				"name":      resourceName,
				"namespace": resourceNamespace,
			},
		},
	}
	em.recorder.Event(us, eventType, reason, message)
}

// EnableDebugLogs enables debug logs for the EventManager.
func (em *EventManager) EnableDebugLogs() {
	em.broadcaster.StartStructuredLogging(4)
}
