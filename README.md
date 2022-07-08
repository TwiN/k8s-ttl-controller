# k8s-ttl-controller
![test](https://github.com/TwiN/k8s-ttl-controller/workflows/test/badge.svg?branch=master)

This application allow you to specify a TTL (time to live) on your Kubernetes resources. Once the TTL is reached,
the resource will be automatically deleted.

To configure the TTL, all you have to do is annotate the relevant resource(s) with `k8s-ttl-controller.twin.sh/ttl` and
a value such as `30m`, `24h` and `7d`. 

The resource is deleted after the current timestamp surpasses the sum of the resource's `metadata.creationTimestamp` and 
the duration specified by the `k8s-ttl-controller.twin.sh/ttl`.


## Deploying on Kubernetes
### Using Helm
For the chart associated to this project, see [TwiN/helm-charts](https://github.com/TwiN/helm-charts):
```console
helm repo add twin https://twin.github.io/helm-charts
helm repo update
helm install k8s-ttl-controller twin/k8s-ttl-controller
```

### Using a YAML file
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: k8s-ttl-controller
  namespace: kube-system
  labels:
    app: k8s-ttl-controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: k8s-ttl-controller
  labels:
    app: k8s-ttl-controller
rules:
  - apiGroups:
      - "*"
    resources:
      - "*"
    verbs:
      - "*"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: k8s-ttl-controller
  labels:
    app: k8s-ttl-controller
roleRef:
  kind: ClusterRole
  name: k8s-ttl-controller
  apiGroup: rbac.authorization.k8s.io
subjects:
  - kind: ServiceAccount
    name: k8s-ttl-controller
    namespace: kube-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: k8s-ttl-controller
  namespace: kube-system
  labels:
    app: k8s-ttl-controller
spec:
  replicas: 1
  selector:
    matchLabels:
      app: k8s-ttl-controller
  template:
    metadata:
      labels:
        app: k8s-ttl-controller
    spec:
      automountServiceAccountToken: true
      serviceAccountName: k8s-ttl-controller
      restartPolicy: Always
      dnsPolicy: Default
      containers:
        - name: k8s-ttl-controller
          image: ghcr.io/twin/k8s-ttl-controller
          imagePullPolicy: Always
```

## Setting a TTL on a resource
To set a TTL on a resource, all you have to do is add the annotation `k8s-ttl-controller.twin.sh/ttl` on the resource 
you want to eventually expire with a duration from the creation of the resource as value.

In other words, if you had a pod named `hello-world` that was created 20 minutes ago, and you annotated it with:
```console
kubectl annotate pod hello-world k8s-ttl-controller.twin.sh/ttl=1h
```
The pod `hello-world` would be deleted in approximately 40 minutes, because 20 minutes have already elapsed, leaving 
40 minutes until the target TTL of 1h is reached.

Alternatively, you can create resources with the annotation already present:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginx
  annotations:
    k8s-ttl-controller.twin.sh/ttl: "1h"
spec:
  containers:
    - name: web
      image: nginx
```
The above would cause the pod to be deleted 1 hour after its creation. 

This is especially useful if you want to create temporary resources without having to worry about unnecessary 
resources accumulating over time.
