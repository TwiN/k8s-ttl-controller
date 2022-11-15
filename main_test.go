package main

import (
	"context"
	"testing"
	"time"

	"github.com/TwiN/kevent"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	fakekubernetes "k8s.io/client-go/kubernetes/fake"
)

func TestReconcile(t *testing.T) {
	// Before jumping into this test, you must understand that kubernetesClient and dynamicClient are used for two different purposes:
	// - kubernetesClient: Takes care of discovery. This means that we need to inject the API resources we want to test through dynamicClient
	// - dynamicClient: Takes care of listing and deleting resources.

	// Create scheme
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	_ = metav1.AddMetaToScheme(scheme)

	// Create scenarios
	scenarios := []struct {
		name                                     string
		podsToCreate                             []*unstructured.Unstructured
		expectedResourcesLeftAfterReconciliation int
	}{
		{
			name: "expired-pod-is-deleted",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "5m"}),
			},
			expectedResourcesLeftAfterReconciliation: 0,
		},
		{
			name: "not-expired-pod-is-not-deleted",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "not-expired-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "3d"}),
			},
			expectedResourcesLeftAfterReconciliation: 1,
		},
		{
			name: "unannotated-pod-is-not-deleted",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "unannotated-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{}),
			},
			expectedResourcesLeftAfterReconciliation: 1,
		},
		{
			name: "one-out-of-two-pods-is-deleted-because-only-one-expired",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "not-expired-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "3d"}),
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "5m"}),
			},
			expectedResourcesLeftAfterReconciliation: 1,
		},
		{
			name: "multiple-expired-pods-are-deleted",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name-1", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "5m"}),
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name-2", time.Now().Add(-72*time.Hour), map[string]interface{}{AnnotationTTL: "2d"}),
			},
			expectedResourcesLeftAfterReconciliation: 0,
		},
		{
			name: "only-expired-pods-are-deleted",
			podsToCreate: []*unstructured.Unstructured{
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name-1", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "5m"}),
				newUnstructuredWithAnnotations("v1", "Pod", "default", "not-expired-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{AnnotationTTL: "3d"}),
				newUnstructuredWithAnnotations("v1", "Pod", "default", "expired-pod-name-2", time.Now().Add(-72*time.Hour), map[string]interface{}{AnnotationTTL: "2d"}),
				newUnstructuredWithAnnotations("v1", "Pod", "default", "unannotated-pod-name", time.Now().Add(-time.Hour), map[string]interface{}{}),
			},
			expectedResourcesLeftAfterReconciliation: 2,
		},
	}

	// Run scenarios
	for _, scenario := range scenarios {
		// Create clients
		kubernetesClient := fakekubernetes.NewSimpleClientset()
		dynamicClient := fakedynamic.NewSimpleDynamicClient(scheme)
		eventManager := kevent.NewEventManager(kubernetesClient, "k8s-ttl-controller")

		fakeDiscovery, _ := kubernetesClient.Discovery().(*fakediscovery.FakeDiscovery)
		fakeDiscovery.Fake.Resources = []*metav1.APIResourceList{
			{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{
					{
						Name:       "pods",
						Kind:       "Pod",
						Namespaced: true,
						Verbs:      []string{"create", "delete", "get", "list", "patch", "update", "watch"},
					},
				},
			},
		}
		// Run scenario
		t.Run(scenario.name, func(t *testing.T) {
			for _, podToCreate := range scenario.podsToCreate {
				_, err := dynamicClient.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}).Namespace("default").Create(context.TODO(), podToCreate, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
			// Make sure that the resources have been created
			list, err := dynamicClient.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}).Namespace("default").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(list.Items) != len(scenario.podsToCreate) {
				t.Errorf("expected 3 resources, got %d", len(list.Items))
			}
			// Reconcile once
			if err := Reconcile(kubernetesClient, dynamicClient, eventManager); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			// Make sure that the expired resources have been deleted
			list, err = dynamicClient.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}).Namespace("default").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(list.Items) != scenario.expectedResourcesLeftAfterReconciliation {
				t.Errorf("expected 3 resources, got %d", len(list.Items))
			}
		})
	}
}

func newUnstructuredWithAnnotations(apiVersion, kind, namespace, name string, creationTimestamp time.Time, annotations map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"namespace":         namespace,
				"name":              name,
				"creationTimestamp": creationTimestamp.Format(time.RFC3339),
				"annotations":       annotations,
			},
		},
	}
}
