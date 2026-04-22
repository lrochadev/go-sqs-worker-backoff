package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var (
	MessagesConsumed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sqs_messages_consumed_total",
			Help: "Total messages consumed, labeled by terminal status.",
		},
		[]string{"status"},
	)

	MessagesRetried = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sqs_messages_retried_total",
			Help: "Total messages scheduled for retry via ChangeMessageVisibility.",
		},
	)

	ProcessingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sqs_message_processing_duration_seconds",
			Help:    "Wall-clock time spent handling a single message (excluding SQS delete).",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14),
		},
		[]string{"status"},
	)

	InFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "sqs_messages_in_flight",
			Help: "Messages currently being processed by handlers.",
		},
	)
)

func MustRegister() {
	prometheus.MustRegister(MessagesConsumed, MessagesRetried, ProcessingDuration, InFlight)
}

// Serve starts an HTTP server exposing /metrics and /healthz. It blocks until
// ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, addr string, log *zap.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("metrics server listening", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
