// Package server — HTTP-слой: маршруты, Bearer-авторизация, обработчики.
// Бизнес-логики нет: принять правку, присвоить номер, отдать правки после
// номера. Всё остальное — на клиенте.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

// DefaultQuotaBytes — глобальный потолок на учётку, см. store.
const DefaultQuotaBytes = store.DefaultQuotaBytes

type Server struct {
	accounts *store.AccountsDB
	pool     *store.Pool
	metrics  *Metrics
	regLimit *rateLimiter
	// Quota — глобальный потолок в байтах для учёток без своего; 0 — без
	// ограничения. Индивидуальный — accounts.quota_bytes.
	Quota int64
}

func New(accounts *store.AccountsDB, pool *store.Pool) *Server {
	return &Server{
		accounts: accounts,
		pool:     pool,
		metrics:  NewMetrics(pool.DataDir(), accounts),
		regLimit: newRateLimiter(10, time.Minute),
		Quota:    DefaultQuotaBytes,
	}
}

// quotaState — потолок и занятое место учётки: индивидуальный потолок из
// accounts.db, иначе глобальный; занятое — по диску (WalkDir по папке
// учётки, холодный путь: файлов сотни, не миллионы).
type quotaState struct {
	Limit int64 `json:"quota_bytes"` // 0 — без ограничения
	Used  int64 `json:"used_bytes"`
}

func (s *Server) quotaOf(accountID string) (quotaState, error) {
	st := quotaState{Limit: s.Quota}
	q, err := s.accounts.AccountQuota(accountID)
	if err != nil && err != store.ErrNotFound {
		return st, err
	}
	if q != nil {
		st.Limit = *q
	}
	st.Used, err = s.pool.UsedBytes(accountID)
	return st, err
}

// overQuota отвечает 507 с числами в теле, если учётка с учётом extra
// байт вылезает за потолок: клиент показывает пользователю, сколько
// занято и сколько можно, а не голую ошибку.
func (s *Server) overQuota(w http.ResponseWriter, req *http.Request, accountID string, extra int64) bool {
	st, err := s.quotaOf(accountID)
	if err != nil {
		s.internalError(w, "quota", err)
		return true
	}
	if st.Limit <= 0 || st.Used+extra <= st.Limit {
		return false
	}
	log.Printf("deny %d %s %s from %s: quota exceeded (used %d + %d > %d)",
		http.StatusInsufficientStorage, req.Method, req.URL.Path, clientAddr(req), st.Used, extra, st.Limit)
	writeJSON(w, http.StatusInsufficientStorage, map[string]any{
		"error":       "account storage quota exceeded",
		"quota_bytes": st.Limit,
		"used_bytes":  st.Used,
	})
	return true
}

// handleQuota — потолок и занятое место своей учётки: приложение
// показывает их в настройках синхронизации.
func (s *Server) handleQuota(w http.ResponseWriter, req *http.Request) {
	st, err := s.quotaOf(deviceFrom(req).AccountID)
	if err != nil {
		s.internalError(w, "quota", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type ctxKey int

const deviceKey ctxKey = 0

func deviceFrom(req *http.Request) store.Device {
	return req.Context().Value(deviceKey).(store.Device)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	// Каждый маршрут API считается в synk_requests_total, включая отказы
	// авторизации (device="-").
	handle := func(pattern string, h http.Handler) { mux.Handle(pattern, s.counted(h)) }
	handle("POST /v1/register", http.HandlerFunc(s.handleRegister))
	// Учётки заводятся и кросс-учёточные коды выпускаются только с сервера
	// (CLI/TUI): токен на телефоне не должен открывать чужие данные.
	// Пригласить устройство в свою учётку может только админ-устройство.
	handle("POST /v1/invites", s.auth(true, s.handleInvite))
	handle("POST /v1/ops", s.auth(false, s.handlePush))
	handle("GET /v1/ops", s.auth(false, s.handlePull))
	handle("GET /v1/checksum", s.auth(false, s.handleChecksum))
	handle("GET /v1/snapshot", s.auth(false, s.handleSnapshot))
	handle("GET /v1/quota", s.auth(false, s.handleQuota))
	handle("GET /v1/photos", s.auth(false, s.handlePhotoList))
	handle("PUT /v1/photos/{name}", s.auth(false, s.handlePhotoPut))
	handle("GET /v1/photos/{name}", s.auth(false, s.handlePhotoGet))
	handle("DELETE /v1/photos/{name}", s.auth(false, s.handlePhotoDelete))
	return mux
}

// MetricsHandler — /metrics и /healthz для отдельного порта, который
// слушает только localhost: через основной домен метрики недостижимы
// вообще, а не «закрыты в nginx». Будущий Prometheus ходит на этот порт.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}))
	return mux
}

// reqInfo — что auth сообщает счётчику снаружи: устройство, если токен
// прошёл. До авторизации и при отказе остаётся "-".
type reqInfo struct{ device string }

const reqInfoKey ctxKey = 1

// counted учитывает запрос в synk_requests_total по маршруту, устройству и
// коду ответа. Стоит снаружи auth, поэтому видит и 401/403.
func (s *Server) counted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		info := &reqInfo{device: "-"}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req.WithContext(context.WithValue(req.Context(), reqInfoKey, info)))
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		s.metrics.Requests.WithLabelValues(req.Pattern, info.device, strconv.Itoa(code)).Inc()
	})
}

// auth проверяет Bearer-токен и кладёт устройство в контекст. Токен скоупит
// обработчик на файл своей учётки — чужие данные недостижимы по построению.
func (s *Server) auth(adminOnly bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			s.deny(w, req, http.StatusUnauthorized, "missing token")
			return
		}
		dev, err := s.accounts.Authenticate(token)
		if err == store.ErrNotFound {
			s.deny(w, req, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			s.internalError(w, "auth", err)
			return
		}
		if info, ok := req.Context().Value(reqInfoKey).(*reqInfo); ok {
			info.device = dev.ID
		}
		if adminOnly && !dev.IsAdmin {
			s.deny(w, req, http.StatusForbidden, "admin token required (device "+dev.ID+")")
			return
		}
		next(w, req.WithContext(context.WithValue(req.Context(), deviceKey, dev)))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// deny — отказ авторизации с записью в журнал: без IP и маршрута
// инцидент не расследовать. Токен в лог не попадает.
func (s *Server) deny(w http.ResponseWriter, req *http.Request, status int, msg string) {
	log.Printf("deny %d %s %s from %s: %s", status, req.Method, req.URL.Path, clientAddr(req), msg)
	jsonError(w, status, msg)
}

func (s *Server) internalError(w http.ResponseWriter, handler string, err error) {
	s.metrics.Errors.WithLabelValues(handler).Inc()
	log.Printf("%s: %v", handler, err)
	jsonError(w, http.StatusInternalServerError, "internal error")
}
