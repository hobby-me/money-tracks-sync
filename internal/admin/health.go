package admin

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sample — одна серия из /metrics с лейблами.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Health — состояние работающего сервера по /healthz и /metrics.
// Сервер опрашивается по localhost: TUI и CLI живут на той же машине.
type Health struct {
	Listen        string
	MetricsListen string
	Up            bool
	Err           string
	Version       string
	Counters      map[string]float64 // synk_*_total, суммы по всем лейблам
	Samples       []Sample           // synk_* и process_* как есть — для разрезов
	StartedAt     time.Time          // process_start_time_seconds
	RSSBytes      int64              // process_resident_memory_bytes
	CheckedAt     time.Time
}

func (h Health) Counter(name string) float64 { return h.Counters[name] }

func (h Health) Uptime(now time.Time) time.Duration {
	if h.StartedAt.IsZero() {
		return 0
	}
	return now.Sub(h.StartedAt)
}

// Gauge — значение серии без лейблов (или первой попавшейся).
func (h Health) Gauge(name string) float64 {
	for _, s := range h.Samples {
		if s.Name == name {
			return s.Value
		}
	}
	return 0
}

// RequestsByDevice — synk_requests_total, свёрнутое по устройству.
func (h Health) RequestsByDevice() map[string]float64 {
	return h.sumBy("synk_requests_total", "device", nil)
}

// ErrorsByDevice — ответы 5xx по устройству.
func (h Health) ErrorsByDevice() map[string]float64 {
	return h.sumBy("synk_requests_total", "device", func(l map[string]string) bool {
		return strings.HasPrefix(l["code"], "5")
	})
}

// RequestsByRoute — synk_requests_total по маршруту.
func (h Health) RequestsByRoute() map[string]float64 {
	return h.sumBy("synk_requests_total", "route", nil)
}

// ErrorsByRoute — ответы 4xx и 5xx по маршруту.
func (h Health) ErrorsByRoute() map[string]float64 {
	return h.sumBy("synk_requests_total", "route", func(l map[string]string) bool {
		return l["code"] >= "400"
	})
}

func (h Health) sumBy(name, label string, keep func(map[string]string) bool) map[string]float64 {
	out := map[string]float64{}
	for _, s := range h.Samples {
		if s.Name != name || (keep != nil && !keep(s.Labels)) {
			continue
		}
		out[s.Labels[label]] += s.Value
	}
	return out
}

// Probe опрашивает сервер с коротким таймаутом: лежащий сервер не должен
// подвешивать интерфейс.
func Probe(ctx context.Context, listen, metricsListen string) Health {
	h := Health{Listen: listen, MetricsListen: metricsListen, Counters: map[string]float64{}, CheckedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()

	body, err := get(ctx, "http://"+listen+"/healthz")
	if err != nil {
		h.Err = err.Error()
		return h
	}
	h.Up = strings.TrimSpace(body) == "ok"
	if !h.Up {
		h.Err = "healthz: " + strings.TrimSpace(fitErr(body))
		return h
	}
	metrics, err := get(ctx, "http://"+metricsListen+"/metrics")
	if err != nil {
		h.Err = "metrics: " + err.Error()
		return h
	}
	parseMetrics(metrics, &h)
	return h
}

func fitErr(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return string(b), &statusError{resp.Status}
	}
	return string(b), nil
}

type statusError struct{ status string }

func (e *statusError) Error() string { return "HTTP " + e.status }

// parseMetrics читает текстовый формат Prometheus без библиотеки: synk_*
// целиком, из process_* — старт и RSS.
func parseMetrics(text string, h *Health) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "synk_") && !strings.HasPrefix(line, "process_") {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		val, err := strconv.ParseFloat(line[sp+1:], 64)
		if err != nil {
			continue
		}
		key := line[:sp]
		var labels map[string]string
		if br := strings.IndexByte(key, '{'); br >= 0 {
			labels = parseLabels(key[br:])
			key = key[:br]
		}
		switch key {
		case "synk_build_info":
			h.Version = labels["version"]
			continue
		case "process_start_time_seconds":
			h.StartedAt = time.Unix(int64(val), 0)
			continue
		case "process_resident_memory_bytes":
			h.RSSBytes = int64(val)
			continue
		}
		if strings.HasPrefix(key, "process_") {
			continue
		}
		h.Samples = append(h.Samples, Sample{Name: key, Labels: labels, Value: val})
		if strings.HasSuffix(key, "_total") {
			h.Counters[key] += val
		}
	}
}

// parseLabels — {a="x",b="y"} → map. Экранирование внутри значений у нас
// не встречается (ULID, маршруты, коды), поэтому разбор простой.
func parseLabels(s string) map[string]string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
	out := map[string]string{}
	for s != "" {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			break
		}
		name := s[:eq]
		rest := s[eq+2:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			break
		}
		out[name] = rest[:end]
		s = strings.TrimPrefix(rest[end+1:], ",")
	}
	return out
}
