package kube

import (
	"context"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps both typed and dynamic Kubernetes clients.
type Client struct {
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	ClusterName   string
}

// NewClient creates a Kubernetes client. If kubeconfigPath is set, it uses that file.
// Otherwise it attempts in-cluster config via service account.
func NewClient(kubeconfigPath string) (*Client, error) {
	var cfg *rest.Config
	var clusterName string
	var err error

	if kubeconfigPath != "" {
		cfg, clusterName, err = fromKubeconfig(kubeconfigPath)
	} else {
		cfg, err = rest.InClusterConfig()
		clusterName = "in-cluster"
	}
	if err != nil {
		return nil, fmt.Errorf("building kube config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	slog.Info("kubernetes client initialized", "cluster", clusterName, "host", cfg.Host)

	return &Client{
		Clientset:     clientset,
		DynamicClient: dynClient,
		ClusterName:   clusterName,
	}, nil
}

// fromKubeconfig loads config from a kubeconfig file and extracts the current context name.
func fromKubeconfig(path string) (*rest.Config, string, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	cfg, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, "", err
	}

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return cfg, "unknown", nil
	}

	return cfg, rawConfig.CurrentContext, nil
}

// ResolveSecret reads a secret value from the cluster.
func (c *Client) ResolveSecret(ctx context.Context, namespace, name, key string) (string, error) {
	secret, err := c.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}

	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}

	return string(val), nil
}
