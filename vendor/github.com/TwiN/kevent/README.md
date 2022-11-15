# kevent
Very simple library for creating Kubernetes events.

```go
eventManager := kevent.NewEventManager(kubernetesClient, applicationName)
eventManager.Create(resourceNamespace, resourceKind, resourceName, reason, message, isWarning)
```
Where:
- `applicationName` is the name of the application that creates the event.
- `kubernetesClient` is a Kubernetes client (e.g. `kubernetes.Clientset`). Note that for testing purposes, the actual parameter type is kubernetes.Interface, which `kubernetes.Clientset` implements.
- `resourceNamespace` is the namespace of the resource that the event is related to.
- `resourceKind` is the kind of the resource that the event is related to.
- `resourceName` is the name of the resource that the event is related to.
- `reason` is the reason for the event (e.g. `FailedToCreate`, `Scheduled`, `FailedToSchedule`, etc.).
- `message` is the message for the event.
- `isWarning` is a boolean indicating whether the event is of type `Warning` or `Normal`.

Example:
```go
package main

import (
    "github.com/TwiN/kevent"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
)

func main() {
    clientConfig, _ := rest.InClusterConfig()
    kubernetesClient, _ := kubernetes.NewForConfig(clientConfig)
    eventManager := kevent.NewEventManager(kubernetesClient, "your-application-name")
    eventManager.Create("kube-system", "pod", "nginx", "Deleted", "Application was unstable", true)
}
```
