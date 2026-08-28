// Command truenas-prometheus-exporter polls TrueNAS's JSON-RPC middleware
// for pool state, dataset capacity, disk temperature and SMART self-test
// results, and serves them on /metrics for Prometheus.
//
// It is deliberately not a platformctl subcommand: it is a long-running
// service, not an operator-invoked CLI action, and shipping it as a second
// small binary out of the same Go module keeps it on the already-audited
// truenas.Caller transport (cli/internal/truenas/transport.go) without
// dragging platformctl's Cobra command tree, Vault client or Kubernetes
// client-go dependency into a container that only ever talks to one NAS.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jdwlabs/platform/internal/truenas"
	"github.com/jdwlabs/platform/internal/truenasexporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	addrEnv       = "TRUENAS_PROMETHEUS_ADDR"
	apiKeyEnv     = "TRUENAS_PROMETHEUS_API_KEY"
	listenEnv     = "TRUENAS_PROMETHEUS_EXPORTER_LISTEN"
	caFileEnv     = "TRUENAS_PROMETHEUS_CA_FILE"
	skipVerifyEnv = "TRUENAS_PROMETHEUS_INSECURE_SKIP_TLS_VERIFY"
	defaultListen = ":9124"
	dialTimeout   = 20 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := os.Getenv(addrEnv)
	apiKey := os.Getenv(apiKeyEnv)
	if addr == "" || apiKey == "" {
		logger.Error("missing configuration",
			"problem", "both "+addrEnv+" and "+apiKeyEnv+" must be set",
		)
		os.Exit(1)
	}
	listen := os.Getenv(listenEnv)
	if listen == "" {
		listen = defaultListen
	}
	tlsOpts := truenas.TLSOptions{
		CAFile:     os.Getenv(caFileEnv),
		SkipVerify: os.Getenv(skipVerifyEnv) == "true",
	}

	// Exactly one dial, exactly one authentication attempt, for the lifetime
	// of this process. See docs/adr/0025's "an authentication attempt is a
	// mutation" finding: four rejected auth.login_with_api_key calls in a
	// short window invalidated the truenas-csi key and took every class of
	// provisioning down. A crash-and-restart-on-failure exporter would turn
	// every CrashLoopBackOff cycle into exactly that kind of attempt, against
	// a key this exporter does not own the blast radius of alone — so a
	// failed dial here is terminal for this process's ability to scrape, not
	// a reason to exit and let Kubernetes retry it. The process stays up,
	// keeps answering /metrics with truenas_exporter_dial_ok=0, and a human
	// redeploying it (after fixing whatever the dial error names) is the only
	// path to a second attempt.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	caller, dialErr := truenas.Dial(ctx, addr, apiKey, tlsOpts)
	cancel()

	dialOK := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "truenas_exporter_dial_ok",
		Help: "1 if the one, process-lifetime dial+authenticate to the TrueNAS " +
			"middleware succeeded; 0 if it failed. Never retried in-process — see " +
			"main.go for why a retry here is a credential-invalidation risk, not " +
			"just a reliability trade-off.",
	})
	prometheus.MustRegister(dialOK)

	if dialErr != nil {
		dialOK.Set(0)
		switch {
		case errors.Is(dialErr, truenas.ErrAPIKeyRejected):
			logger.Error("TrueNAS rejected the API key on the one dial attempt this process will make",
				"error", dialErr.Error(),
				"remedy", "fix the credential and redeploy — this process will not retry",
			)
		default:
			logger.Error("failed to connect to TrueNAS on the one dial attempt this process will make",
				"error", dialErr.Error(),
				"remedy", "fix connectivity/TLS and redeploy — this process will not retry",
			)
		}
		serveDegraded(logger, listen)
		return
	}
	dialOK.Set(1)
	defer caller.Close()

	collector := truenasexporter.New(caller, func(name, msg string) {
		logger.Warn("sub-collector failed", "collector", name, "error", msg)
	})
	prometheus.MustRegister(collector)

	serve(logger, listen)
}

// serveDegraded runs the HTTP server with only the process-level gauges
// registered (no Collector, since there is no authenticated session to poll)
// so kubelet's liveness/readiness probes against /metrics keep succeeding —
// a probe failure would restart the pod, and a restart re-runs main(), which
// is exactly the retry this design avoids.
func serveDegraded(logger *slog.Logger, listen string) {
	serve(logger, listen)
}

func serve(logger *slog.Logger, listen string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("serving metrics", "addr", listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server exited", "error", err.Error())
			os.Exit(1)
		}
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sigCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
