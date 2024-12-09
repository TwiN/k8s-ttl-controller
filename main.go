package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/TwiN/kevent"
	"github.com/sirupsen/logrus"
	str2duration "github.com/xhit/go-str2duration/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	AnnotationTTL         = "k8s-ttl-controller.twin.sh/ttl"
	AnnotationRefreshedAt = "k8s-ttl-controller.twin.sh/refreshed-at"

	MaximumFailedExecutionBeforePanic = 10                    // Maximum number of allowed failed executions before panicking
	ExecutionTimeout                  = 20 * time.Minute      // Maximum time for each reconciliation before timing out
	ExecutionInterval                 = 5 * time.Minute       // Interval between each reconciliation
	ThrottleDuration                  = 50 * time.Millisecond // Duration to sleep for throttling purposes

	ListLimit = 500 // Maximum number of items to list at once
)

var (
	ErrTimedOut = errors.New("execution timed out")

	listTimeoutSeconds     = int64(60)
	executionFailedCounter = 0

	debug    = os.Getenv("DEBUG") == "true"
	jsonLogs = os.Getenv("JSON_LOGS") == "true"
)

func init() {
	if jsonLogs {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	} else {
		logrus.SetFormatter(&logrus.TextFormatter{})
	}
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
}

func main() {
	for {
		start := time.Now()
		kubernetesClient, dynamicClient, err := CreateClients()
		if err != nil {
			logrus.WithError(err).Panic("failed to create Kubernetes clients")
		}
		eventManager := kevent.NewEventManager(kubernetesClient, "k8s-ttl-controller")
		if err = Reconcile(kubernetesClient, dynamicClient, eventManager); err != nil {
			logrus.WithError(err).Error("Error during execution")
			executionFailedCounter++
			if executionFailedCounter > MaximumFailedExecutionBeforePanic {
				logrus.WithError(err).Panicf("execution failed %d times", executionFailedCounter)
			}
		} else if executionFailedCounter > 0 {
			logrus.Infof("Execution was successful after %d failed attempts, resetting counter to 0", executionFailedCounter)
			executionFailedCounter = 0
		}
		logrus.Infof("Execution took %dms, sleeping for %s", time.Since(start).Milliseconds(), ExecutionInterval)
		time.Sleep(ExecutionInterval)
	}
}

// Reconcile loops over all resources and deletes all sub resources that have expired
//
// Returns an error if an execution lasts for longer than ExecutionTimeout
func Reconcile(kubernetesClient kubernetes.Interface, dynamicClient dynamic.Interface, eventManager *kevent.EventManager) error {
	// Use Kubernetes' discovery API to retrieve all resources
	_, resources, err := kubernetesClient.Discovery().ServerGroupsAndResources()
	if err != nil {
		return err
	}
	if debug {
		logrus.Debugf("[Reconcile] Found %d API resources", len(resources))
	}
	timeout := make(chan bool, 1)
	result := make(chan bool, 1)
	go func() {
		time.Sleep(ExecutionTimeout)
		timeout <- true
	}()
	go func() {
		result <- DoReconcile(dynamicClient, eventManager, resources)
	}()
	select {
	case <-timeout:
		return ErrTimedOut
	case <-result:
		return nil
	}
}

func getStartTime(item unstructured.Unstructured) metav1.Time {
	refreshedAt, exists := item.GetAnnotations()[AnnotationRefreshedAt]
	if exists {
		t, err := time.Parse(time.RFC3339, refreshedAt)
		if err == nil {
			return metav1.NewTime(t)
		}
		logrus.WithFields(logrus.Fields{
			"kind": item.GetKind(),
			"name": item.GetName(),
		}).Warnf("Failed to parse refreshed-at timestamp '%s': %s", refreshedAt, err)
	}
	return item.GetCreationTimestamp()
}

// DoReconcile goes over all API resources specified, retrieves all sub resources and deletes those who have expired
func DoReconcile(dynamicClient dynamic.Interface, eventManager *kevent.EventManager, resources []*metav1.APIResourceList) bool {
	for _, resource := range resources {
		if len(resource.APIResources) == 0 {
			continue
		}
		gv := strings.Split(resource.GroupVersion, "/")
		gvr := schema.GroupVersionResource{}
		if len(gv) == 2 {
			gvr.Group = gv[0]
			gvr.Version = gv[1]
		} else if len(gv) == 1 {
			gvr.Version = gv[0]
		} else {
			continue
		}
		for _, apiResource := range resource.APIResources {
			// Make sure that we can list and delete the resource. If we can't, then there's no point querying it.
			verbs := apiResource.Verbs.String()
			if !strings.Contains(verbs, "list") || !strings.Contains(verbs, "delete") {
				continue
			}
			// List all items under the resource
			gvr.Resource = apiResource.Name
			var list *unstructured.UnstructuredList
			var continueToken string
			var ttlInDuration time.Duration
			var err error
			for list == nil || continueToken != "" {
				list, err = dynamicClient.Resource(gvr).List(context.TODO(), metav1.ListOptions{TimeoutSeconds: &listTimeoutSeconds, Continue: continueToken, Limit: ListLimit})
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"resource":     gvr.Resource,
						"groupVersion": gvr.GroupVersion(),
					}).Errorf("Error checking: %s", err)
					continue
				}
				if list != nil {
					continueToken = list.GetContinue()
				}
				if debug {
					logrus.Debugf("Checking %d %s from %s", len(list.Items), gvr.Resource, gvr.GroupVersion())
				}
				for _, item := range list.Items {
					ttl, exists := item.GetAnnotations()[AnnotationTTL]
					if !exists {
						continue
					}
					ttlInDuration, err = str2duration.ParseDuration(ttl)
					if err != nil {
						logrus.WithFields(logrus.Fields{
							"resource": apiResource.Name,
							"name":     item.GetName(),
						}).Warnf("Invalid TTL '%s': %s", ttl, err)
						continue
					}
					ttlExpired := time.Now().After(getStartTime(item).Add(ttlInDuration))
					if ttlExpired {
						durationSinceExpired := time.Since(getStartTime(item).Add(ttlInDuration)).Round(time.Second)
						logrus.WithFields(logrus.Fields{
							"resource":     apiResource.Name,
							"name":         item.GetName(),
							"ttl":          ttl,
							"expiredSince": durationSinceExpired,
						}).Info("Resource has expired")
						err = dynamicClient.Resource(gvr).Namespace(item.GetNamespace()).Delete(context.TODO(), item.GetName(), metav1.DeleteOptions{})
						if err != nil {
							logrus.WithFields(logrus.Fields{
								"resource": apiResource.Name,
								"name":     item.GetName(),
							}).Errorf("Failed to delete: %s", err)
							eventManager.Create(item.GetNamespace(), item.GetKind(), item.GetName(), "FailedToDeleteExpiredTTL", "Unable to delete expired resource:"+err.Error(), true)
						} else {
							logrus.WithFields(logrus.Fields{
								"resource": apiResource.Name,
								"name":     item.GetName(),
							}).Info("Resource deleted")
							eventManager.Create(item.GetNamespace(), item.GetKind(), item.GetName(), "DeletedExpiredTTL", "Deleted resource because "+ttl+" or more has elapsed", false)
						}
						// Cool off a tiny bit to avoid hitting the API too often
						time.Sleep(ThrottleDuration)
					} else {
						logrus.WithFields(logrus.Fields{
							"resource":  apiResource.Name,
							"name":      item.GetName(),
							"ttl":       ttl,
							"expiresIn": time.Until(item.GetCreationTimestamp().Add(ttlInDuration)).Round(time.Second),
						}).Info("Resource will expire")
					}
				}
				// Cool off a tiny bit to avoid hitting the API too often
				time.Sleep(ThrottleDuration)
			}
			// Cool off a tiny bit to avoid hitting the API too often
			time.Sleep(ThrottleDuration)
		}
	}
	return true
}
