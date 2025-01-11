package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//Queued: %d, Processed: %d, Dropped: %d, Queue Size: %d/%d, Workers:

var (
	queuedMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_queued_total",
		Help: "The total number of queued blocks",
	})
)

var (
	processedMetrics = promauto.NewCounter(prometheus.CounterOpts{
		Name: "whales_watcher_processed_total",
		Help: "The total number of processed blocks",
	})
)

var (
	retriedTasksMetrics = promauto.NewCounter(prometheus.CounterOpts{
		Name: "whales_watcher_retries_total",
		Help: "The total number of retries blocks",
	})
)

var (
	inqueueMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_inqueue_total",
		Help: "The total number blocks in queue",
	})
)

var (
	queueSizeMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_queue_size_total",
		Help: "Queue size",
	})
)

var (
	workersMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_workers_total",
		Help: "The total number of workers",
	})
)

func metricsHttp() {
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":2112", nil)
}
