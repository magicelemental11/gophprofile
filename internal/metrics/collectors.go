package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// StatsSource reads the avatar statistics reported at scrape time.
type StatsSource interface {
	Stats(ctx context.Context) (domain.Stats, error)
}

var (
	storageBytesDesc = prometheus.NewDesc("avatars_storage_bytes",
		"Size of stored avatar originals per user.", []string{"user_id"}, nil)
	storageTotalDesc = prometheus.NewDesc("avatars_storage_total_bytes",
		"Size of all stored avatar originals.", nil, nil)
	avatarsDesc = prometheus.NewDesc("avatars_total",
		"Stored avatars by processing status.", []string{"processing_status"}, nil)
	outboxDesc = prometheus.NewDesc("outbox_pending_events",
		"Events waiting in the outbox to be published.", nil, nil)
)

type statsCollector struct {
	src     StatsSource
	timeout time.Duration
}

// NewStatsCollector reports storage usage, avatar counts and the outbox backlog, querying src
// on every scrape with the given timeout.
func NewStatsCollector(src StatsSource, timeout time.Duration) prometheus.Collector {
	return statsCollector{src: src, timeout: timeout}
}

func (c statsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- storageBytesDesc
	ch <- storageTotalDesc
	ch <- avatarsDesc
	ch <- outboxDesc
}

func (c statsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	s, err := c.src.Stats(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(storageTotalDesc, err)
		return
	}
	var total int64
	for user, bytes := range s.StorageBytes {
		total += bytes
		ch <- prometheus.MustNewConstMetric(storageBytesDesc, prometheus.GaugeValue, float64(bytes), user)
	}
	ch <- prometheus.MustNewConstMetric(storageTotalDesc, prometheus.GaugeValue, float64(total))
	for status, n := range s.Avatars {
		ch <- prometheus.MustNewConstMetric(avatarsDesc, prometheus.GaugeValue, float64(n), string(status))
	}
	ch <- prometheus.MustNewConstMetric(outboxDesc, prometheus.GaugeValue, float64(s.OutboxPending))
}

// PoolStat is the part of pgxpool.Stat reported as metrics.
type PoolStat interface {
	AcquiredConns() int32
	IdleConns() int32
	TotalConns() int32
	MaxConns() int32
	AcquireCount() int64
	EmptyAcquireCount() int64
	EmptyAcquireWaitTime() time.Duration
}

var (
	poolConnsDesc = prometheus.NewDesc("db_pool_connections",
		"Database pool connections by state.", []string{"state"}, nil)
	poolMaxDesc = prometheus.NewDesc("db_pool_max_connections",
		"Maximum size of the database pool.", nil, nil)
	poolAcquiresDesc = prometheus.NewDesc("db_pool_acquires_total",
		"Connections acquired from the database pool.", nil, nil)
	poolEmptyDesc = prometheus.NewDesc("db_pool_empty_acquires_total",
		"Acquires that waited because the pool had no idle connection.", nil, nil)
	poolWaitDesc = prometheus.NewDesc("db_pool_empty_acquire_wait_seconds_total",
		"Time spent waiting for a connection from an empty pool.", nil, nil)
)

type poolCollector struct{ stat func() PoolStat }

// NewPoolCollector reports database pool statistics taken from stat on every scrape.
func NewPoolCollector(stat func() PoolStat) prometheus.Collector {
	return poolCollector{stat: stat}
}

func (c poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolConnsDesc
	ch <- poolMaxDesc
	ch <- poolAcquiresDesc
	ch <- poolEmptyDesc
	ch <- poolWaitDesc
}

func (c poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	gauge := func(desc *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
	}
	counter := func(desc *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, v)
	}
	gauge(poolConnsDesc, float64(s.AcquiredConns()), "acquired")
	gauge(poolConnsDesc, float64(s.IdleConns()), "idle")
	gauge(poolConnsDesc, float64(s.TotalConns()), "total")
	gauge(poolMaxDesc, float64(s.MaxConns()))
	counter(poolAcquiresDesc, float64(s.AcquireCount()))
	counter(poolEmptyDesc, float64(s.EmptyAcquireCount()))
	counter(poolWaitDesc, s.EmptyAcquireWaitTime().Seconds())
}
