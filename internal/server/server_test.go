package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealthz(t *testing.T) {
	s := New(Config{Addr: ":0"}, Deps{})
	r := s.Router()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
