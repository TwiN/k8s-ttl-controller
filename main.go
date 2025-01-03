package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/TwiN/kevent"
	"github.com/xhit/go-str2duration/v2"
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

	APIResourcesToWatchEnv = "API_RESOURCES_TO_WATCH"
)

var (
	ErrTimedOut = errors.New("execution timed out")

	listTimeoutSeconds     = int64(60)
	executionFailedCounter = 0

	logger       *slog.Logger  // Global logger
	programLevel slog.LevelVar // Info by default

	apiResourcesToWatch []string
)

func init() {
	// Create a new logger, either in JSON or text format
	if os.Getenv("JSON_LOG") == "true" {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	// Set the log level based on the DEBUG environment variable
	if os.Getenv("DEBUG") == "true" {
		programLevel.Set(slog.LevelDebug)
	}

	// Parse the trackable resources from the environment
	if os.Getenv(APIResourcesToWatchEnv) != "" {
		apiResourcesToWatch = strings.Split(os.Getenv(APIResourcesToWatchEnv), ",")
	}

}

func main() {
	for {
		start := time.Now()
		kubernetesClient, dynamicClient, err := CreateClients()
		if err != nil {
			panic("failed to create Kubernetes clients: " + err.Error())
		}
		eventManager := kevent.NewEventManager(kubernetesClient, "k8s-ttl-controller")
		if err = Reconcile(kubernetesClient, dynamicClient, eventManager); err != nil {
			logger.Info(fmt.Sprintf("Error during execution: %s", err.Error()))
			executionFailedCounter++
			if executionFailedCounter > MaximumFailedExecutionBeforePanic {
				panic(fmt.Errorf("execution failed %d times: %w", executionFailedCounter, err))
			}
		} else if executionFailedCounter > 0 {
			logger.Info(fmt.Sprintf("Execution was successful after %d failed attempts, resetting counter to 0", executionFailedCounter))
			executionFailedCounter = 0
		}
		logger.Info(fmt.Sprintf("Execution took %dms, sleeping for %s", time.Since(start).Milliseconds(), ExecutionInterval))
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
	logger.Debug(fmt.Sprintf("[Reconcile] Found %d API resources", len(resources)))
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
		logger.Info("Failed to parse refreshed-at timestamp '%s' for %s/%s: %s", refreshedAt, item.GetKind(), item.GetName(), err)
	}
	return item.GetCreationTimestamp()
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
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
			// Skip resources that are not in the list of trackable resources
			if len(apiResourcesToWatch) != 0 && !contains(apiResourcesToWatch, apiResource.Name) {
				continue
			}
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
					logger.Info(fmt.Sprintf("Error checking %s from %s: %s", gvr.Resource, gvr.GroupVersion(), err))
					continue
				}
				if list != nil {
					continueToken = list.GetContinue()
				}
				logger.Debug(fmt.Sprintf("Checking %d %s from %s", len(list.Items), gvr.Resource, gvr.GroupVersion()))
				for _, item := range list.Items {
					ttl, exists := item.GetAnnotations()[AnnotationTTL]
					if !exists {
						continue
					}
					ttlInDuration, err = str2duration.ParseDuration(ttl)
					if err != nil {
						logger.Info(fmt.Sprintf("[%s/%s] has an invalid TTL '%s': %s", apiResource.Name, item.GetName(), ttl, err))
						continue
					}
					ttlExpired := time.Now().After(getStartTime(item).Add(ttlInDuration))
					if ttlExpired {
						durationSinceExpired := time.Since(getStartTime(item).Add(ttlInDuration)).Round(time.Second)
						logger.Info(fmt.Sprintf("[%s/%s] is configured with a TTL of %s, which means it has expired %s ago", apiResource.Name, item.GetName(), ttl, durationSinceExpired))
						err = dynamicClient.Resource(gvr).Namespace(item.GetNamespace()).Delete(context.TODO(), item.GetName(), metav1.DeleteOptions{})
						if err != nil {
							logger.Info(fmt.Sprintf("[%s/%s] failed to delete: %s", apiResource.Name, item.GetName(), err))
							eventManager.Create(item.GetNamespace(), item.GetKind(), item.GetName(), "FailedToDeleteExpiredTTL", "Unable to delete expired resource:"+err.Error(), true)
							// XXX: Should we retry with GracePeriodSeconds set to &0 to force immediate deletion after the first attempt failed?
						} else {
							logger.Info(fmt.Sprintf("[%s/%s] deleted", apiResource.Name, item.GetName()))
							eventManager.Create(item.GetNamespace(), item.GetKind(), item.GetName(), "DeletedExpiredTTL", "Deleted resource because "+ttl+" or more has elapsed", false)
						}
						// Cool off a tiny bit to avoid hitting the API too often
						time.Sleep(ThrottleDuration)
					} else {
						logger.Info(fmt.Sprintf("[%s/%s] is configured with a TTL of %s, which means it will expire in %s", apiResource.Name, item.GetName(), ttl, time.Until(getStartTime(item).Add(ttlInDuration)).Round(time.Second)))
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
