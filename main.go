package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/xhit/go-str2duration/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	AnnotationTTL = "k8s-ttl-controller.twin.sh/ttl"

	MaximumFailedExecutionBeforePanic = 10              // Maximum number of allowed failed executions before panicking
	ExecutionInterval                 = 5 * time.Minute // Interval between each reconciliation
)

var (
	ErrTimedOut = errors.New("execution timed out")

	executionFailedCounter = 0
)

func main() {
	for {
		start := time.Now()
		if err := run(); err != nil {
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

func run() error {
	kubernetesClient, dynamicClient, err := CreateClients()
	if err != nil {
		return err
	}
	// Use Kubernetes' discovery API to retrieve all resources
	resources, err := kubernetesClient.Discovery().ServerPreferredResources()
	if err != nil {
		return err
	}
	return Reconcile(dynamicClient, resources)
}

// Reconcile loops over all resources and deletes all sub resources that have expired
//
// Returns an error if an execution lasts for longer than ExecutionTimeout
func Reconcile(dynamicClient dynamic.Interface, resources []*metav1.APIResourceList) error {
	timeout := make(chan bool, 1)
	result := make(chan bool, 1)
	go func() {
		time.Sleep(10 * time.Minute)
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

// DoReconcile handles rolling upgrades by iterating over every single AutoScalingGroups' outdated
// instances
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
				fmt.Println(err)
				continue
			}
			for _, item := range list.Items {
				ttl, exists := item.GetAnnotations()[AnnotationTTL]
				if !exists {
					continue
				}
				ttlInDuration, err := str2duration.ParseDuration(ttl)
				if err != nil {
					fmt.Printf("[%s/%s] has an invalid TTL '%s': %s\n", apiResource.Name, item.GetName(), ttl, err)
					continue
				}
				ttlExpired := time.Now().After(item.GetCreationTimestamp().Add(ttlInDuration))
				if ttlExpired {
					log.Printf("[%s/%s] is configured with a TTL of %s, which means it has expired %s ago", apiResource.Name, item.GetName(), ttl, time.Since(item.GetCreationTimestamp().Add(ttlInDuration)).Round(time.Second))
					err := dynamicClient.Resource(gvr).Namespace(item.GetNamespace()).Delete(context.TODO(), item.GetName(), metav1.DeleteOptions{})
					if err != nil {
						fmt.Printf("[%s/%s] failed to delete: %s\n", apiResource.Name, item.GetName(), err)
						// XXX: Should we retry with GracePeriodSeconds set to &0 to force immediate deletion after the first attempt failed?
					} else {
						log.Printf("[%s/%s] deleted", apiResource.Name, item.GetName())
					}
				} else {
					log.Printf("[%s/%s] is configured with a TTL of %s, which means it will expire in %s", apiResource.Name, item.GetName(), ttl, time.Until(item.GetCreationTimestamp().Add(ttlInDuration)).Round(time.Second))
				}
			}
			// Cool off a tiny bit to avoid hitting the API too often
			time.Sleep(50 * time.Millisecond)
		}
	}
	return true
}
