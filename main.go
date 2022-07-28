package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/xhit/go-str2duration/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	AnnotationTTL = "k8s-ttl-controller.twin.sh/ttl"

	MaximumFailedExecutionBeforePanic = 10                    // Maximum number of allowed failed executions before panicking
	ExecutionTimeout                  = 10 * time.Minute      // Maximum time for each reconciliation before timing out
	ExecutionInterval                 = 5 * time.Minute       // Interval between each reconciliation
	ThrottleDuration                  = 50 * time.Millisecond // Duration to sleep for throttling purposes
)

var (
	ErrTimedOut = errors.New("execution timed out")

	executionFailedCounter = 0

	debug = os.Getenv("DEBUG") == "true"
)

func main() {
	for {
		start := time.Now()
		kubernetesClient, dynamicClient, err := CreateClients()
		if err != nil {
			panic("failed to create Kubernetes clients: " + err.Error())
		}
		if err := Reconcile(kubernetesClient, dynamicClient); err != nil {
			log.Printf("Error during execution: %s", err.Error())
			executionFailedCounter++
			if executionFailedCounter > MaximumFailedExecutionBeforePanic {
				panic(fmt.Errorf("execution failed %d times: %v", executionFailedCounter, err))
			}
		} else if executionFailedCounter > 0 {
			log.Printf("Execution was successful after %d failed attempts, resetting counter to 0", executionFailedCounter)
			executionFailedCounter = 0
		}
		log.Printf("Execution took %dms, sleeping for %s", time.Since(start).Milliseconds(), ExecutionInterval)
		time.Sleep(ExecutionInterval)
	}
}

// Reconcile loops over all resources and deletes all sub resources that have expired
//
// Returns an error if an execution lasts for longer than ExecutionTimeout
func Reconcile(kubernetesClient kubernetes.Interface, dynamicClient dynamic.Interface) error {
	// Use Kubernetes' discovery API to retrieve all resources
	_, resources, err := kubernetesClient.Discovery().ServerGroupsAndResources()
	if err != nil {
		return err
	}
	if debug {
		log.Println("[Reconcile] Found", len(resources), "API resources")
	}
	timeout := make(chan bool, 1)
	result := make(chan bool, 1)
	go func() {
		time.Sleep(ExecutionTimeout)
		timeout <- true
	}()
	go func() {
		result <- DoReconcile(dynamicClient, resources)
	}()
	select {
	case <-timeout:
		return ErrTimedOut
	case <-result:
		return nil
	}
}

// DoReconcile goes over all API resources specified, retrieves all sub resources and deletes those who have expired
func DoReconcile(dynamicClient dynamic.Interface, resources []*metav1.APIResourceList) bool {
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
			list, err := dynamicClient.Resource(gvr).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				log.Println(err)
				continue
			}
			if debug {
				log.Println("Checking", len(list.Items), gvr.Resource, "from", gvr.GroupVersion())
			}
			for _, item := range list.Items {
				ttl, exists := item.GetAnnotations()[AnnotationTTL]
				if !exists {
					continue
				}
				ttlInDuration, err := str2duration.ParseDuration(ttl)
				if err != nil {
					log.Printf("[%s/%s] has an invalid TTL '%s': %s\n", apiResource.Name, item.GetName(), ttl, err)
					continue
				}
				ttlExpired := time.Now().After(item.GetCreationTimestamp().Add(ttlInDuration))
				if ttlExpired {
					log.Printf("[%s/%s] is configured with a TTL of %s, which means it has expired %s ago", apiResource.Name, item.GetName(), ttl, time.Since(item.GetCreationTimestamp().Add(ttlInDuration)).Round(time.Second))
					err := dynamicClient.Resource(gvr).Namespace(item.GetNamespace()).Delete(context.TODO(), item.GetName(), metav1.DeleteOptions{})
					if err != nil {
						log.Printf("[%s/%s] failed to delete: %s\n", apiResource.Name, item.GetName(), err)
						// XXX: Should we retry with GracePeriodSeconds set to &0 to force immediate deletion after the first attempt failed?
					} else {
						log.Printf("[%s/%s] deleted", apiResource.Name, item.GetName())
					}
					// Cool off a tiny bit to avoid hitting the API too often
					time.Sleep(ThrottleDuration)
				} else {
					log.Printf("[%s/%s] is configured with a TTL of %s, which means it will expire in %s", apiResource.Name, item.GetName(), ttl, time.Until(item.GetCreationTimestamp().Add(ttlInDuration)).Round(time.Second))
				}
			}
			// Cool off a tiny bit to avoid hitting the API too often
			time.Sleep(ThrottleDuration)
		}
	}
	return true
}
