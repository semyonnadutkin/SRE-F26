package httpapi

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	registrationsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_registrations_total",
		Help: "Total number of successful user registrations.",
	})

	loginsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_logins_total",
		Help: "Total number of successful logins.",
	})

	uploadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_uploads_total",
		Help: "Total number of successfully uploaded files.",
	})

	downloadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_downloads_total",
		Help: "Total number of successfully downloaded files.",
	})

	operationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "fileserver_operation_duration_seconds",
			Help: "Duration of operation handlers, including failed attempts.",
			Buckets: []float64{
				0.01, 0.05, 0.1, 0.25, 0.5,
				1, 2.5, 5, 10, 30, 60, 120, 300, 600,
			},
		},
		[]string{"operation"},
	)
)

func observeOperation(operation string) func() {
	started := time.Now()

	return func() {
		operationDuration.WithLabelValues(operation).
			Observe(time.Since(started).Seconds())
	}
}
