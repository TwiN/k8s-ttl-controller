package main

import (
	"errors"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// CreateClients initializes a Kubernetes client and a dynamic client using either the kubeconfig file
// (if ENVIRONMENT is set to dev) or the in-cluster config otherwise.
func CreateClients() (kubernetes.Interface, dynamic.Interface, error) {
	var cfg *rest.Config
	if os.Getenv("ENVIRONMENT") == "dev" {
		var kubeconfig string
		if home := homeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		} else {
			return nil, nil, errors.New("home directory not found")
		}
		// use the current context in kubeconfig
		clientConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, err
		}
		cfg = clientConfig
	} else {
		clientConfig, err := rest.InClusterConfig()
		if err != nil {
			return nil, nil, err
		}
		cfg = clientConfig
	}
	cfg.WarningHandler = rest.NoWarnings{}
	kubernetesClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return kubernetesClient, dynamicClient, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
