package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveSecret(t *testing.T) {
	fakeClient := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-credentials",
			Namespace: "homerun2",
		},
		Data: map[string][]byte{
			"password": []byte("s3cret"),
		},
	})

	c := &Client{Clientset: fakeClient}

	val, err := c.ResolveSecret(context.Background(), "homerun2", "redis-credentials", "password")
	if err != nil {
		t.Fatalf("ResolveSecret() error: %v", err)
	}
	if val != "s3cret" {
		t.Errorf("got %q, want %q", val, "s3cret")
	}
}

func TestResolveSecretMissingKey(t *testing.T) {
	fakeClient := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-credentials",
			Namespace: "homerun2",
		},
		Data: map[string][]byte{
			"password": []byte("s3cret"),
		},
	})

	c := &Client{Clientset: fakeClient}

	_, err := c.ResolveSecret(context.Background(), "homerun2", "redis-credentials", "nonexistent")
	if err == nil {
		t.Error("ResolveSecret() expected error for missing key, got nil")
	}
}

func TestResolveSecretNotFound(t *testing.T) {
	fakeClient := fake.NewClientset()

	c := &Client{Clientset: fakeClient}

	_, err := c.ResolveSecret(context.Background(), "homerun2", "missing-secret", "key")
	if err == nil {
		t.Error("ResolveSecret() expected error for missing secret, got nil")
	}
}
