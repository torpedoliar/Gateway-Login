package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yourorg/sso-gateway/internal/karyawan"
)

type Handlers struct {
	repo *karyawan.Repo
}

func NewHandlers(repo *karyawan.Repo) *Handlers { return &Handlers{repo: repo} }

func (h *Handlers) Routes(r chi.Router) {
	r.Get("/api/v1/karyawan", h.List)
	r.Get("/api/v1/karyawan/{nik_hris}", h.Get)
}

type karyawanView struct {
	NIKHRIS        string  `json:"nik_hris"`
	NIKSantos      string  `json:"nik_santos"`
	NamaKaryawan   string  `json:"nama_karyawan"`
	NamaDepartemen string  `json:"nama_departemen"`
	NamaJabatan    string  `json:"nama_jabatan"`
	TglBergabung   *string `json:"tgl_bergabung"`
	TglKeluar      *string `json:"tgl_keluar"`
	Lokasi         string  `json:"lokasi"`
	Gender         string  `json:"gender"`
}

func toView(k karyawan.Karyawan) karyawanView {
	v := karyawanView{
		NIKHRIS:        k.NIKHRIS,
		NIKSantos:      k.NIKSantos,
		NamaKaryawan:   k.NamaKaryawan,
		NamaDepartemen: k.NamaDepartemen,
		NamaJabatan:    k.NamaJabatan,
		Lokasi:         k.Lokasi,
		Gender:         k.Gender,
	}
	if k.TglBergabung != nil {
		s := k.TglBergabung.Format("2006-01-02")
		v.TglBergabung = &s
	}
	if k.TglKeluar != nil {
		s := k.TglKeluar.Format("2006-01-02")
		v.TglKeluar = &s
	}
	return v
}

type listResponse struct {
	Data   []karyawanView `json:"data"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := karyawan.Filter{
		NIKHRIS:      q.Get("nik_hris"),
		NIKSantos:    q.Get("nik_santos"),
		NamaKaryawan: q.Get("nama_karyawan"),
		Departemen:   q.Get("departemen"),
		Jabatan:      q.Get("jabatan"),
		Lokasi:       q.Get("lokasi"),
		Limit:        limit,
		Offset:       offset,
	}
	if sa := q.Get("status_aktif"); sa != "" {
		b := sa == "true" || sa == "1"
		f.StatusAktif = &b
	}

	rows, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query_error")
		return
	}
	out := make([]karyawanView, 0, len(rows))
	for _, k := range rows {
		out = append(out, toView(k))
	}
	effLimit := limit
	if effLimit <= 0 || effLimit > 500 {
		effLimit = 50
	}
	writeJSON(w, http.StatusOK, listResponse{
		Data:   out,
		Total:  total,
		Limit:  effLimit,
		Offset: offset,
	})
}

func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	nik := chi.URLParam(r, "nik_hris")
	if nik == "" {
		writeErr(w, http.StatusBadRequest, "missing_nik")
		return
	}
	k, err := h.repo.GetByNIK(r.Context(), nik)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, toView(*k))
}

// helpers
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

var _ = time.Now
