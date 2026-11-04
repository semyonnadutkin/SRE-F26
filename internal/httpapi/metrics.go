package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metricsResponseWriter struct {
	http.ResponseWriter
	status      int
	success     bool
	writeFailed bool
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return // the status was already sent
	}

	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}

	w.ResponseWriter.WriteHeader(status)
	w.status = status
}

func (w *metricsResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	n, err := w.ResponseWriter.Write(data)
	if err != nil || n != len(data) {
		w.writeFailed = true
	}
	return n, err
}

func (w *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

var (
	uploadedBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_uploaded_bytes_total",
		Help: "Total bytes stored by successful uploads after committing file metadata",
	})

	downloadedBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fileserver_downloaded_bytes_total",
		Help: "Total file bytes accepted by the HTTP ResponseWriter for GET downloads, including partial downloads",
	})

	operationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fileserver_operations_total",
			Help: "Total number of completed operation attempts.",
		},
		[]string{"operation", "outcome", "status"},
	)

	operationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "fileserver_operation_duration_seconds",
			Help: "Duration of operation requests.",
			Buckets: []float64{
				0.01, 0.05, 0.1, 0.25, 0.5,
				1, 2.5, 5, 10, 30, 60, 120, 300, 600,
			},
		},
		[]string{"operation", "outcome", "status"},
	)
)

func trackOperation(
	w http.ResponseWriter,
	operation string,
) (*metricsResponseWriter, func()) {
	started := time.Now()
	response := &metricsResponseWriter{ResponseWriter: w}

	return response, func() {
		panicValue := recover()

		status := response.status
		if status == 0 {
			status = http.StatusOK
			if panicValue != nil {
				status = http.StatusInternalServerError
			}
		}

		outcome := "failed"
		if response.success && !response.writeFailed && panicValue == nil &&
			status >= 200 && status < 400 {
			outcome = "success"
		}

		statusLabel := strconv.Itoa(status)

		operationsTotal.
			WithLabelValues(operation, outcome, statusLabel).
			Inc()

		operationDuration.
			WithLabelValues(operation, outcome, statusLabel).
			Observe(time.Since(started).Seconds())

		if panicValue != nil {
			panic(panicValue)
		}
	}
}
