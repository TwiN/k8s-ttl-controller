# k8s-ttl-controller
![test](https://github.com/TwiN/k8s-ttl-controller/workflows/test/badge.svg?branch=master)

This application allow you to specify a TTL (time to live) on your Kubernetes resources. Once the TTL is reached,
the resource will be automatically deleted.

To configure the TTL, all you have to do is annotate the relevant resource(s) with `k8s-ttl-controller.twin.sh/ttl` and
a value such as `30m`, `24h` and `7d`. 

The resource is deleted after the current timestamp surpasses the sum of the resource's `metadata.creationTimestamp` and 
the duration specified by the `k8s-ttl-controller.twin.sh/ttl`.