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

	"net/http"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/banner"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/collector"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/config"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/informer"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/kube"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/probe"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/throttle"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/webhook"

	homerun "github.com/stuttgart-things/homerun-library/v4"
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

	// spec.cluster names the cluster where the kubeconfig context cannot
	// (in-cluster); the system defaults to k8s-<cluster>.
	if prof.Spec.Cluster != "" {
		kubeClient.ClusterName = prof.Spec.Cluster
	}
	system := prof.SystemFor(kubeClient.ClusterName)

	// Canceled on SIGINT/SIGTERM: ends a startup wait for the pitch target, then
	// the collectors, informers and webhook server.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// Initialize pitcher. Both network modes wait for their target with bounded
	// backoff: a single 5s check used to exit and crashloop the pod while Redis
	// or omni-pitcher was still starting (#65).
	var p pitcher.K8sPitcher
	if os.Getenv("PITCHER_MODE") == "file" {
		filePath := os.Getenv("PITCHER_FILE")
		if filePath == "" {
			filePath = "pitched.log"
		}
		p = &pitcher.FileK8sPitcher{Path: filePath}
		slog.Info("pitcher mode: file", "path", filePath)
	} else if prof.Spec.Pitcher.Addr != "" {
		hp := pitcher.NewHTTPK8sPitcher(
			prof.Spec.Pitcher.Addr,
			prof.Spec.Auth.Token,
			prof.Spec.Pitcher.Insecure,
			kubeClient.ClusterName,
		)
		hp.SystemName = system
		timeout, err := config.LoadStartupTimeout(config.PitcherStartupTimeoutEnv)
		if err != nil {
			slog.Error("invalid configuration", "error", err)
			os.Exit(1)
		}
		waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
		err = homerun.WaitForReady(waitCtx, hp.HealthCheck, 5*time.Second)
		cancelWait()
		exitIfNotReady(ctx, "pitcher", err, "addr", prof.Spec.Pitcher.Addr, "startup_timeout", timeout.String())
		p = hp
		slog.Info("pitcher mode: http",
			"addr", prof.Spec.Pitcher.Addr,
			"insecure", prof.Spec.Pitcher.Insecure,
		)
	} else {
		rp := pitcher.NewRedisK8sPitcher(prof.Spec.Redis, kubeClient.ClusterName)
		rp.SystemName = system
		timeout, err := homerun.LoadRedisStartupTimeout()
		if err != nil {
			slog.Error("invalid configuration", "error", err)
			os.Exit(1)
		}
		err = homerun.WaitForRedisContext(ctx, rp.RedisConfig, timeout)
		exitIfNotReady(ctx, "redis", err, "addr", prof.Spec.Redis.Addr, "port", prof.Spec.Redis.Port, "startup_timeout", timeout.String())
		p = rp
		slog.Info("pitcher mode: redis",
			"addr", prof.Spec.Redis.Addr,
			"port", prof.Spec.Redis.Port,
			"stream", prof.Spec.Redis.Stream,
		)
	}

	// Cooldown and rate limit (#87) apply to every source: collectors,
	// informers and the webhook. Unset, the pitcher is used as is.
	if prof.Spec.Cooldown > 0 || prof.Spec.RateLimit != nil {
		tp := throttle.New(p, prof.Spec.Cooldown, prof.Spec.RateLimit, kubeClient.ClusterName)
		attrs := []any{"cooldown", prof.Spec.Cooldown.String()}
		if rl := prof.Spec.RateLimit; rl != nil {
			// Flush the suppressed summary of a window even when it stays quiet.
			go tp.Run(ctx, min(rl.Per, 30*time.Second))
			attrs = append(attrs, "rateLimitMax", rl.Max, "rateLimitPer", rl.Per.String())
		}
		p = tp
		slog.Info("throttle enabled", attrs...)
	}

	// Start collectors
	if len(prof.Spec.Collectors) > 0 {
		c := collector.New(kubeClient.DynamicClient, p, prof.Spec.Collectors, kubeClient.ClusterName)
		c.ServerVersion = func() (string, error) {
			v, err := kubeClient.Clientset.Discovery().ServerVersion()
			if err != nil {
				return "", err
			}
			return v.GitVersion, nil
		}
		go c.Start(ctx)
		slog.Info("collectors started", "count", len(prof.Spec.Collectors))
	}

	// Start informers
	if len(prof.Spec.Informers) > 0 {
		m := informer.New(kubeClient.DynamicClient, p, prof.Spec.Informers, kubeClient.ClusterName)
		go m.Start(ctx)
		slog.Info("informers started", "count", len(prof.Spec.Informers))
	}

	// Start endpoint probes (#109) and their /metrics server, which runs on
	// its own port, apart from the optional webhook server.
	if pr := prof.Spec.Probes; pr.Enabled {
		probeSystem := prof.ProbeSystemFor(kubeClient.ClusterName)
		pm := probe.New(kubeClient.DynamicClient, p, pr, kubeClient.ClusterName, probeSystem)
		go pm.Run(ctx)
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", pm.Metrics.Handler())
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		serve(ctx, "metrics", pr.MetricsPort, mux)
		slog.Info("endpoint probes started",
			"system", probeSystem,
			"interval", pr.Interval.String(),
			"snapshotInterval", pr.SnapshotInterval.String(),
			"metricsPort", pr.MetricsPort,
		)
	}

	// Start webhook server if enabled
	if prof.Spec.Webhook.Enabled {
		port := prof.Spec.Webhook.Port
		if port == "" {
			port = "8080"
		}
		ws := webhook.NewServer(p, kubeClient.ClusterName, prof.Spec.Webhook.HMACKey)
		serve(ctx, "webhook", port, ws.Handler())
	}

	slog.Info("homerun2-k8s-pitcher running", "cluster", kubeClient.ClusterName, "system", system)
	<-ctx.Done()

	slog.Info("shutting down")
	stop()
	// Give goroutines time to finish
	time.Sleep(500 * time.Millisecond)
	slog.Info("homerun2-k8s-pitcher exited gracefully")
}

// serve runs an HTTP server on port until ctx is done.
func serve(ctx context.Context, name, port string, handler http.Handler) {
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info(name+" server started", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error(name+" server error", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

// exitIfNotReady ends startup when waiting for the pitch target did not
// succeed: exit 0 if a shutdown signal ended the wait, exit 1 if the target
// never answered within its budget.
func exitIfNotReady(ctx context.Context, target string, err error, attrs ...any) {
	if ctx.Err() != nil {
		slog.Info("shutdown requested while waiting for " + target)
		os.Exit(0)
	}
	if err != nil {
		slog.Error(target+" not reachable", append([]any{"error", err}, attrs...)...)
		os.Exit(1)
	}
}

func resolveSecrets(kubeClient *kube.Client, prof *profile.K8sPitcherProfile) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ref := prof.Spec.Redis.PasswordFrom; ref != nil {
		ns := profile.ResolveSecretNamespace(ref.SecretKeyRef.Namespace)
		val, err := kubeClient.ResolveSecret(ctx, ns, ref.SecretKeyRef.Name, ref.SecretKeyRef.Key)
		if err != nil {
			slog.Warn("failed to resolve redis password from secret, falling back to inline",
				"error", err,
			)
		} else {
			prof.Spec.Redis.Password = val
			slog.Info("redis password resolved from secret",
				"secret", ref.SecretKeyRef.Name,
				"namespace", ns,
			)
		}
	}

	if ref := prof.Spec.Auth.TokenFrom; ref != nil {
		ns := profile.ResolveSecretNamespace(ref.SecretKeyRef.Namespace)
		val, err := kubeClient.ResolveSecret(ctx, ns, ref.SecretKeyRef.Name, ref.SecretKeyRef.Key)
		if err != nil {
			slog.Warn("failed to resolve auth token from secret, falling back to inline",
				"error", err,
			)
		} else {
			prof.Spec.Auth.Token = val
			slog.Info("auth token resolved from secret",
				"secret", ref.SecretKeyRef.Name,
				"namespace", ns,
			)
		}
	}
}
