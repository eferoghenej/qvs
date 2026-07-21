// Command api is a tiny HTTP service used to exercise the platform:
// it needs a Postgres connection and exposes /, /healthz and /metrics.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const listenAddr = ":8080"

// httpRequests counts requests so /metrics exposes something meaningful to
// alert on (e.g. a spike in 5xx). The default registry also ships Go runtime
// and process metrics for free.
var httpRequests = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by path and status code.",
	},
	[]string{"path", "status"},
)

func main() {
	// The connection string is never hard-coded; it arrives from a Secret.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	// Keep the pool small: this API is a probe, not the main DB consumer.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("/", hello)
	mux.HandleFunc("/healthz", healthz(db))
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Serve in the background so we can wait for a shutdown signal.
	go func() {
		log.Printf("listening on %s", listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Kubernetes sends SIGTERM before removing the pod; drain in-flight
	// requests instead of dropping them.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

// hello answers GET / with a plain greeting.
func hello(w http.ResponseWriter, r *http.Request) {
	// net/http's ServeMux sends every unmatched path to "/", so reject
	// anything that is not exactly the root.
	if r.URL.Path != "/" {
		httpRequests.WithLabelValues(r.URL.Path, "404").Inc()
		http.NotFound(w, r)
		return
	}
	httpRequests.WithLabelValues("/", "200").Inc()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("hello from qvs-api\n"))
}

// healthz runs SELECT 1 against the database: 200 if reachable, 503 if not.
func healthz(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			httpRequests.WithLabelValues("/healthz", "503").Inc()
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		httpRequests.WithLabelValues("/healthz", "200").Inc()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}
