package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/hobby-me/money-tracks-sync/internal/store"
	"github.com/hobby-me/money-tracks-sync/internal/version"
)

// Metrics регистрируются при старте, без горутин — первый скрейп видит всё.
// Набор рассчитан на Prometheus + Grafana в будущем: по устройствам и
// хендлерам (RPS, ошибки), по учёткам (размер, свежесть), диск, процесс.
// Сейчас те же серии читает synk-tui с localhost.
type Metrics struct {
	OpsReceived  prometheus.Counter
	OpsServed    prometheus.Counter
	PushRequests prometheus.Counter
	PullRequests prometheus.Counter
	PhotosPut    prometheus.Counter
	Errors       *prometheus.CounterVec
	// Requests — каждый HTTP-запрос: маршрут (метод + шаблон), устройство
	// (или "-" до авторизации), код ответа. Кардинальность — устройства × ~12.
	Requests *prometheus.CounterVec
	Registry *prometheus.Registry
}

func NewMetrics(dataDir string, accounts *store.AccountsDB) *Metrics {
	reg := prometheus.NewRegistry()
	f := promauto.With(reg)
	buildInfo := f.NewGauge(prometheus.GaugeOpts{
		Name: "synk_build_info",
		Help: "Build metadata; value is always 1.",
		ConstLabels: prometheus.Labels{
			"version":    version.Version,
			"git_commit": version.GitCommit,
			"build_hash": version.BuildHash,
			"build_date": version.BuildDate,
			"go_version": version.GoVersion(),
		},
	})
	buildInfo.Set(1)
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(&storageCollector{dataDir: dataDir, accounts: accounts})
	return &Metrics{
		Registry: reg,
		OpsReceived: f.NewCounter(prometheus.CounterOpts{
			Name: "synk_ops_received_total", Help: "Ops accepted via push."}),
		OpsServed: f.NewCounter(prometheus.CounterOpts{
			Name: "synk_ops_served_total", Help: "Ops served via pull."}),
		PushRequests: f.NewCounter(prometheus.CounterOpts{
			Name: "synk_push_requests_total", Help: "Push requests."}),
		PullRequests: f.NewCounter(prometheus.CounterOpts{
			Name: "synk_pull_requests_total", Help: "Pull requests."}),
		PhotosPut: f.NewCounter(prometheus.CounterOpts{
			Name: "synk_photos_put_total", Help: "Photo uploads."}),
		Errors: f.NewCounterVec(prometheus.CounterOpts{
			Name: "synk_errors_total", Help: "Internal errors by handler."},
			[]string{"handler"}),
		Requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "synk_requests_total", Help: "HTTP requests by route, device and status code."},
			[]string{"route", "device", "code"}),
	}
}

// statusWriter запоминает код ответа для synk_requests_total.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// ---- storageCollector: диск и учётки, считаются на каждом скрейпе ----

var (
	descDiskTotal = prometheus.NewDesc("synk_disk_total_bytes",
		"Capacity of the filesystem holding the data directory.", nil, nil)
	descDiskFree = prometheus.NewDesc("synk_disk_free_bytes",
		"Free space available on the data filesystem.", nil, nil)
	descDataBytes = prometheus.NewDesc("synk_data_bytes",
		"Total size of the data directory.", nil, nil)
	descAccountBytes = prometheus.NewDesc("synk_account_bytes",
		"Size of an account directory (sync.db + photos).", []string{"account", "name"}, nil)
	descAccountDevices = prometheus.NewDesc("synk_account_devices",
		"Devices per account by state.", []string{"account", "name", "state"}, nil)
	descAccounts = prometheus.NewDesc("synk_accounts", "Number of accounts.", nil, nil)
)

type storageCollector struct {
	dataDir  string
	accounts *store.AccountsDB
}

func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descDiskTotal
	ch <- descDiskFree
	ch <- descDataBytes
	ch <- descAccountBytes
	ch <- descAccountDevices
	ch <- descAccounts
}

func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	if ds, err := store.Disk(c.dataDir); err == nil {
		ch <- prometheus.MustNewConstMetric(descDiskTotal, prometheus.GaugeValue, float64(ds.TotalBytes))
		ch <- prometheus.MustNewConstMetric(descDiskFree, prometheus.GaugeValue, float64(ds.FreeBytes))
	}
	if n, err := store.DirSize(c.dataDir); err == nil {
		ch <- prometheus.MustNewConstMetric(descDataBytes, prometheus.GaugeValue, float64(n))
	}
	accs, err := c.accounts.ListAccounts()
	if err != nil {
		log.Printf("metrics: accounts: %v", err)
		return
	}
	devs, err := c.accounts.ListDevices("")
	if err != nil {
		log.Printf("metrics: devices: %v", err)
		return
	}
	ch <- prometheus.MustNewConstMetric(descAccounts, prometheus.GaugeValue, float64(len(accs)))
	for _, acc := range accs {
		dir := filepath.Join(c.dataDir, "acc", acc.ID)
		if _, err := os.Stat(dir); err == nil {
			if n, err := store.DirSize(dir); err == nil {
				ch <- prometheus.MustNewConstMetric(descAccountBytes, prometheus.GaugeValue,
					float64(n), acc.ID, acc.Name)
			}
		}
		active, revoked := 0, 0
		for _, d := range devs {
			if d.AccountID != acc.ID {
				continue
			}
			if d.RevokedAt != nil {
				revoked++
			} else {
				active++
			}
		}
		ch <- prometheus.MustNewConstMetric(descAccountDevices, prometheus.GaugeValue,
			float64(active), acc.ID, acc.Name, "active")
		ch <- prometheus.MustNewConstMetric(descAccountDevices, prometheus.GaugeValue,
			float64(revoked), acc.ID, acc.Name, "revoked")
	}
}
