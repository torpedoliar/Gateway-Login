package karyawan

import (
	"encoding/json"
	"time"
)

type Karyawan struct {
	NIKHRIS         string
	NIKSantos       string
	NamaKaryawan    string
	NamaDepartemen  string
	NamaJabatan     string
	TglBergabung    *time.Time
	TglKeluar       *time.Time
	Lokasi          string
	Gender          string
	SourceUpdatedAt *time.Time
	SyncedAt        time.Time
	RawPayload      json.RawMessage
}

// Active returns true if TglKeluar is nil.
func (k *Karyawan) Active() bool { return k.TglKeluar == nil }

type Filter struct {
	NIKHRIS      string
	NIKSantos    string
	NamaKaryawan string
	Departemen   string
	Jabatan      string
	Lokasi       string
	StatusAktif  *bool
	Limit        int
	Offset       int
}
