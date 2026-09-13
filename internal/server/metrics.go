package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/scottlaird/drivelist/internal/store"
)

// metrics holds the server's Prometheus registry: request counters kept
// as they happen, and fleet gauges read from the store on every scrape.
type metrics struct {
	registry *prometheus.Registry
	reports  *prometheus.CounterVec
	ingest   prometheus.Histogram
}

func newMetrics(st *store.Store) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drivelist_reports_total",
			Help: "Inventory reports received, by host and outcome (changed, heartbeat, rejected, error).",
		}, []string{"host", "outcome"}),
		ingest: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drivelist_ingest_seconds",
			Help:    "Time to apply one inventory report.",
			Buckets: prometheus.ExponentialBuckets(0.001, 4, 8),
		}),
	}
	m.registry.MustRegister(m.reports, m.ingest, &fleetCollector{store: st},
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *metrics) observeReport(host, outcome string, took time.Duration) {
	m.reports.WithLabelValues(host, outcome).Inc()
	m.ingest.Observe(took.Seconds())
}

// fleetCollector reads the fleet summary from the store at scrape time.
// Per-drive values are deliberately not exported: they live in the
// database and the CLI, and exporting a gauge per drive would duplicate
// the store.
type fleetCollector struct {
	store *store.Store
}

var (
	descHostDrives     = prometheus.NewDesc("drivelist_host_drives", "Drives currently placed on the host.", []string{"host"}, nil)
	descHostMissing    = prometheus.NewDesc("drivelist_host_missing", "Drives that vanished from the host and are not expected to be absent.", []string{"host"}, nil)
	descHostGhosts     = prometheus.NewDesc("drivelist_host_ghosts", "Pool members on the host with no present device.", []string{"host"}, nil)
	descHostStale      = prometheus.NewDesc("drivelist_host_stale", "1 while the host has missed three report intervals.", []string{"host"}, nil)
	descHostLastReport = prometheus.NewDesc("drivelist_host_last_report_timestamp_seconds", "Unix time of the host's last accepted report.", []string{"host"}, nil)
	descDrives         = prometheus.NewDesc("drivelist_drives", "Known drives by status.", []string{"status"}, nil)
	descKernelWarn     = prometheus.NewDesc("drivelist_kernel_warnings_24h", "kernel_warning events in the last 24 hours.", nil, nil)
	descEvents24       = prometheus.NewDesc("drivelist_events_24h", "Events of any kind in the last 24 hours.", nil, nil)
	descScrapeError    = prometheus.NewDesc("drivelist_scrape_error", "1 if reading the fleet summary failed on this scrape.", nil, nil)
	descSASErrors      = prometheus.NewDesc("drivelist_sas_phy_errors_total", "SAS error counter of one phy since the host booted, as last reported. counter is invalid_dword, disparity_error, loss_dword_sync or phy_reset_problem.", []string{"host", "node", "phy", "port", "attached", "device", "counter"}, nil)
	descSASRate        = prometheus.NewDesc("drivelist_sas_phy_rate_gbit", "Negotiated link rate of one phy in Gbit/s, 0 without a link.", []string{"host", "node", "phy", "port", "attached", "device"}, nil)
)

func (c *fleetCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descHostDrives, descHostMissing, descHostGhosts, descHostStale, descHostLastReport, descDrives, descKernelWarn, descEvents24, descScrapeError, descSASErrors, descSASRate} {
		ch <- d
	}
}

func (c *fleetCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := c.store.Metrics(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(descScrapeError, prometheus.GaugeValue, 1)
		return
	}
	ch <- prometheus.MustNewConstMetric(descScrapeError, prometheus.GaugeValue, 0)
	for _, h := range m.Hosts {
		ch <- prometheus.MustNewConstMetric(descHostDrives, prometheus.GaugeValue, float64(h.DriveCount), h.Hostname)
		ch <- prometheus.MustNewConstMetric(descHostMissing, prometheus.GaugeValue, float64(h.MissingCount), h.Hostname)
		ch <- prometheus.MustNewConstMetric(descHostGhosts, prometheus.GaugeValue, float64(h.GhostCount), h.Hostname)
		stale := 0.0
		if !h.StaleSince.IsZero() {
			stale = 1
		}
		ch <- prometheus.MustNewConstMetric(descHostStale, prometheus.GaugeValue, stale, h.Hostname)
		if !h.LastReport.IsZero() {
			ch <- prometheus.MustNewConstMetric(descHostLastReport, prometheus.GaugeValue, float64(h.LastReport.Unix()), h.Hostname)
		}
	}
	for _, status := range []string{store.StatusOK, store.StatusSuspect, store.StatusBad, store.StatusShelved, store.StatusRetired} {
		ch <- prometheus.MustNewConstMetric(descDrives, prometheus.GaugeValue, float64(m.DrivesByStatus[status]), status)
	}
	ch <- prometheus.MustNewConstMetric(descKernelWarn, prometheus.GaugeValue, float64(m.KernelWarnings24))
	ch <- prometheus.MustNewConstMetric(descEvents24, prometheus.GaugeValue, float64(m.Events24))

	// Per-phy SAS counters are the exception to "no per-device series":
	// watching them climb over time is the reason they are collected.
	phys, err := c.store.SASPhyTotals(ctx)
	if err != nil {
		return
	}
	for _, p := range phys {
		labels := []string{p.Hostname, p.OwnerName, strconv.Itoa(p.PhyID), p.Port, p.Attached, p.DevName}
		ch <- prometheus.MustNewConstMetric(descSASRate, prometheus.GaugeValue, p.RateGbit, labels...)
		for name, v := range map[string]uint64{"invalid_dword": p.InvalidDword, "disparity_error": p.DisparityError, "loss_dword_sync": p.LossDwordSync, "phy_reset_problem": p.PhyResetProblem} {
			ch <- prometheus.MustNewConstMetric(descSASErrors, prometheus.CounterValue, float64(v), append(labels, name)...)
		}
	}
}
