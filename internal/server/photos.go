package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// Имя файла — ULID + расширение, присваивается на устройстве. Строгий regex
// заодно отсекает path traversal: ни точек в имени, ни разделителей.
var photoNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}\.(jpg|jpeg|png|webp)$`)

const maxPhotoBytes = 20 << 20

func (s *Server) photoPath(w http.ResponseWriter, req *http.Request) (string, bool) {
	name := req.PathValue("name")
	if !photoNameRe.MatchString(name) {
		jsonError(w, http.StatusBadRequest, "bad photo name")
		return "", false
	}
	dir := s.pool.PhotosDir(deviceFrom(req).AccountID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.internalError(w, "photos", err)
		return "", false
	}
	return filepath.Join(dir, name), true
}

// handlePhotoPut идемпотентен: повтор перезаписывает. Пишем во временный
// файл и переименовываем — оборванная заливка не оставит битого чека.
func (s *Server) handlePhotoPut(w http.ResponseWriter, req *http.Request) {
	path, ok := s.photoPath(w, req)
	if !ok {
		return
	}
	accountID := deviceFrom(req).AccountID
	if req.ContentLength > 0 && s.overQuota(w, req, accountID, req.ContentLength) {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		s.internalError(w, "photo_put", err)
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, http.MaxBytesReader(w, req.Body, maxPhotoBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonError(w, http.StatusRequestEntityTooLarge, "photo too large")
			return
		}
		s.internalError(w, "photo_put", err)
		return
	}
	// Временный файл уже лежит в папке учётки и посчитан в UsedBytes;
	// повторная проверка ловит chunked-заливку без Content-Length.
	if s.overQuota(w, req, accountID, 0) {
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		s.internalError(w, "photo_put", err)
		return
	}
	s.metrics.PhotosPut.Inc()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePhotoGet(w http.ResponseWriter, req *http.Request) {
	path, ok := s.photoPath(w, req)
	if !ok {
		return
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		// 404 на ленивой докачке tombstone-записи — не ошибка.
		jsonError(w, http.StatusNotFound, "no such photo")
		return
	}
	if err != nil {
		s.internalError(w, "photo_get", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.internalError(w, "photo_get", err)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, req, req.PathValue("name"), st.ModTime(), f)
}

// handlePhotoDelete идемпотентен: удаление отсутствующего — тоже успех.
func (s *Server) handlePhotoDelete(w http.ResponseWriter, req *http.Request) {
	path, ok := s.photoPath(w, req)
	if !ok {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.internalError(w, "photo_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePhotoList(w http.ResponseWriter, req *http.Request) {
	dir := s.pool.PhotosDir(deviceFrom(req).AccountID)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		s.internalError(w, "photo_list", err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && photoNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}
