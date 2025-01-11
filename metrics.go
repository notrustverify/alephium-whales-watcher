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

var (
	txQueueMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_tx_queue",
		Help: "Total number of transactions in queue",
	})
)

var (
	txQueueWorkersMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_tx_queue_workers",
		Help: "Total number of workers for tx queue",
	})
)

var (
	txQueueSizeMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_tx_queue_size",
		Help: "Size tx queue",
	})
)

var (
	cexQueueMetrics = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_cex_queue",
		Help: "Total number of cex messages in queue",
	})
)

var (
	notificationQueueMetric = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_notification_queue",
		Help: "Total number of message to send to telegram/twitter in queue",
	})
)

var (
	notificationQueueSizeMetric = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_notification_queue_size",
		Help: "Size queue to send to telegram/twitter",
	})
)

var (
	cexQueueSizeMetric = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "whales_watcher_cex_queue_size",
		Help: "Size queue cex messages",
	})
)

func metricsHttp() {
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":2112", nil)
}
