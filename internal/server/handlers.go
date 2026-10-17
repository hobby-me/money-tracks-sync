package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

const (
	maxOpsPerBatch = 500
	maxBodyBytes   = 8 << 20 // пачка ≤500 правок; фото едут отдельным каналом
)

func (s *Server) handleRegister(w http.ResponseWriter, req *http.Request) {
	if !s.regLimit.allow(clientAddr(req)) {
		s.deny(w, req, http.StatusTooManyRequests, "slow down")
		return
	}
	var in struct {
		InviteCode string `json:"invite_code"`
		DeviceName string `json:"device_name"`
	}
	if err := decodeBody(w, req, &in); err != nil {
		return
	}
	if in.InviteCode == "" || in.DeviceName == "" || len(in.DeviceName) > 100 {
		jsonError(w, http.StatusBadRequest, "invite_code and device_name required")
		return
	}
	dev, token, err := s.accounts.Redeem(in.InviteCode, in.DeviceName)
	if err == store.ErrInviteInvalid {
		s.deny(w, req, http.StatusForbidden, "invite invalid or expired")
		return
	}
	if err != nil {
		s.internalError(w, "register", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":  dev.ID,
		"token":      token,
		"account_id": dev.AccountID,
		"is_admin":   dev.IsAdmin,
	})
}

// handleInvite выпускает одноразовый код в свою учётку (маршрут админский).
// Код в чужую учётку через HTTP не выдаётся: только synk invite / TUI.
func (s *Server) handleInvite(w http.ResponseWriter, req *http.Request) {
	dev := deviceFrom(req)
	if other := req.URL.Query().Get("account"); other != "" && other != dev.AccountID {
		s.deny(w, req, http.StatusForbidden, "cross-account invites are server-side only")
		return
	}
	inv, err := s.accounts.CreateInvite(dev.AccountID, false)
	if err == store.ErrNotFound {
		jsonError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		s.internalError(w, "invites", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invite_code": inv.Code,
		"expires_at":  inv.ExpiresAt.UnixMilli(),
	})
}

func (s *Server) handlePush(w http.ResponseWriter, req *http.Request) {
	s.metrics.PushRequests.Inc()
	dev := deviceFrom(req)
	var in struct {
		Ops []store.Op `json:"ops"`
	}
	if err := decodeBody(w, req, &in); err != nil {
		return
	}
	if len(in.Ops) == 0 || len(in.Ops) > maxOpsPerBatch {
		jsonError(w, http.StatusBadRequest, "ops: 1..500 per batch")
		return
	}
	for i := range in.Ops {
		op := &in.Ops[i]
		if op.OpID == "" || len(op.OpID) > 64 || len(op.DeviceID) > 64 ||
			op.Entity == "" || len(op.Entity) > 64 ||
			op.EntityUUID == "" || len(op.EntityUUID) > 64 ||
			op.HLC == "" || len(op.HLC) > 32 || op.Payload == "" {
			jsonError(w, http.StatusBadRequest, "malformed op")
			return
		}
		// device_id в правке — авторство, не авторизация: правка могла
		// приехать перепушем с другого устройства той же учётки.
		if op.DeviceID == "" {
			op.DeviceID = dev.ID
		}
	}
	if s.overQuota(w, req, dev.AccountID, 0) {
		return
	}
	sdb, err := s.pool.Get(dev.AccountID)
	if err != nil {
		s.internalError(w, "push", err)
		return
	}
	acks, latest, err := sdb.Push(in.Ops)
	if err != nil {
		s.internalError(w, "push", err)
		return
	}
	s.metrics.OpsReceived.Add(float64(len(acks)))
	writeJSON(w, http.StatusOK, map[string]any{
		"acked":       acks,
		"latest_seq":  latest,
		"instance_id": sdb.InstanceID,
	})
}

func (s *Server) handlePull(w http.ResponseWriter, req *http.Request) {
	s.metrics.PullRequests.Inc()
	dev := deviceFrom(req)
	after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
	limit := maxOpsPerBatch
	if l, err := strconv.Atoi(req.URL.Query().Get("limit")); err == nil && l > 0 && l < maxOpsPerBatch {
		limit = l
	}
	sdb, err := s.pool.Get(dev.AccountID)
	if err != nil {
		s.internalError(w, "pull", err)
		return
	}
	ops, latest, err := sdb.Pull(after, limit)
	if err != nil {
		s.internalError(w, "pull", err)
		return
	}
	s.metrics.OpsServed.Add(float64(len(ops)))
	writeJSON(w, http.StatusOK, map[string]any{
		"ops":         ops,
		"latest_seq":  latest,
		"instance_id": sdb.InstanceID,
	})
}

func (s *Server) handleChecksum(w http.ResponseWriter, req *http.Request) {
	dev := deviceFrom(req)
	sdb, err := s.pool.Get(dev.AccountID)
	if err != nil {
		s.internalError(w, "checksum", err)
		return
	}
	sums, err := sdb.Checksum()
	if err != nil {
		s.internalError(w, "checksum", err)
		return
	}
	latest, err := sdb.LatestSeq()
	if err != nil {
		s.internalError(w, "checksum", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entities":    sums,
		"latest_seq":  latest,
		"instance_id": sdb.InstanceID,
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, req *http.Request) {
	dev := deviceFrom(req)
	sdb, err := s.pool.Get(dev.AccountID)
	if err != nil {
		s.internalError(w, "snapshot", err)
		return
	}
	rows, latest, err := sdb.Snapshot()
	if err != nil {
		s.internalError(w, "snapshot", err)
		return
	}
	if rows == nil {
		rows = []store.Row{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":        rows,
		"latest_seq":  latest,
		"instance_id": sdb.InstanceID,
	})
}

func decodeBody(w http.ResponseWriter, req *http.Request, v any) error {
	req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
	dec := json.NewDecoder(req.Body)
	if err := dec.Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return err
	}
	return nil
}
