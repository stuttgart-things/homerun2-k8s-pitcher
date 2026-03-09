package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/banner"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/collector"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/config"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/informer"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/kube"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	profilePath := flag.String("profile", "", "path to K8sPitcherProfile YAML (required)")
	kubeconfigPath := flag.String("kubeconfig", "", "path to kubeconfig file (optional, uses in-cluster if not set)")
	flag.Parse()

	banner.Show()
	config.SetupLogging()

	slog.Info("starting homerun2-k8s-pitcher",
		"version", version,
		"commit", commit,
		"date", date,
		"go", runtime.Version(),
	)

	if *profilePath == "" {
		slog.Error("--profile flag is required")
		os.Exit(1)
	}

	// Load profile
	prof, err := profile.Load(*profilePath)
	if err != nil {
		slog.Error("failed to load profile", "path", *profilePath, "error", err)
		os.Exit(1)
	}
	slog.Info("profile loaded", "name", prof.Metadata.Name)

	// Initialize Kubernetes client
	kubeClient, err := kube.NewClient(*kubeconfigPath)
	if err != nil {
		slog.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}

	// Resolve secrets if *From fields are set
	resolveSecrets(kubeClient, prof)

	// Initialize pitcher
	var p pitcher.K8sPitcher
	if os.Getenv("PITCHER_MODE") == "file" {
		filePath := os.Getenv("PITCHER_FILE")
		if filePath == "" {
			filePath = "pitched.log"
		}
		p = &pitcher.FileK8sPitcher{Path: filePath}
		slog.Info("pitcher mode: file", "path", filePath)
	} else {
		rp := pitcher.NewRedisK8sPitcher(prof.Spec.Redis, kubeClient.ClusterName)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := rp.HealthCheck(ctx); err != nil {
			slog.Error("redis health check failed", "error", err)
			cancel()
			os.Exit(1)
		}
		cancel()
		p = rp
		slog.Info("pitcher mode: redis",
			"addr", prof.Spec.Redis.Addr,
			"port", prof.Spec.Redis.Port,
			"stream", prof.Spec.Redis.Stream,
		)
	}

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Start collectors
	if len(prof.Spec.Collectors) > 0 {
		c := collector.New(kubeClient.DynamicClient, p, prof.Spec.Collectors, kubeClient.ClusterName)
		go c.Start(ctx)
		slog.Info("collectors started", "count", len(prof.Spec.Collectors))
	}

	// Start informers
	if len(prof.Spec.Informers) > 0 {
		m := informer.New(kubeClient.DynamicClient, p, prof.Spec.Informers, kubeClient.ClusterName)
		go m.Start(ctx)
		slog.Info("informers started", "count", len(prof.Spec.Informers))
	}

	slog.Info("homerun2-k8s-pitcher running", "cluster", kubeClient.ClusterName)
	<-quit

	slog.Info("shutting down")
	cancel()
	// Give goroutines time to finish
	time.Sleep(500 * time.Millisecond)
	slog.Info("homerun2-k8s-pitcher exited gracefully")
}

func resolveSecrets(kubeClient *kube.Client, prof *profile.K8sPitcherProfile) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ref := prof.Spec.Redis.PasswordFrom; ref != nil {
		val, err := kubeClient.ResolveSecret(ctx, ref.SecretKeyRef.Namespace, ref.SecretKeyRef.Name, ref.SecretKeyRef.Key)
		if err != nil {
			slog.Warn("failed to resolve redis password from secret, falling back to inline",
				"error", err,
			)
		} else {
			prof.Spec.Redis.Password = val
			slog.Info("redis password resolved from secret",
				"secret", ref.SecretKeyRef.Name,
				"namespace", ref.SecretKeyRef.Namespace,
			)
		}
	}

	if ref := prof.Spec.Auth.TokenFrom; ref != nil {
		val, err := kubeClient.ResolveSecret(ctx, ref.SecretKeyRef.Namespace, ref.SecretKeyRef.Name, ref.SecretKeyRef.Key)
		if err != nil {
			slog.Warn("failed to resolve auth token from secret, falling back to inline",
				"error", err,
			)
		} else {
			prof.Spec.Auth.Token = val
			slog.Info("auth token resolved from secret",
				"secret", ref.SecretKeyRef.Name,
				"namespace", ref.SecretKeyRef.Namespace,
			)
		}
	}
}
