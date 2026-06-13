package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/yourorg/sso-gateway/internal/api"
	"github.com/yourorg/sso-gateway/internal/apikey"
)

type Config struct {
	Addr            string
	APIRateLimitRPM int
}

type Deps struct {
	API     *api.Handlers
	APIKeys *apikey.Store
	Redis   *redis.Client
}

type Server struct {
	cfg Config
	dep Deps
}

func New(cfg Config, dep Deps) *Server { return &Server{cfg: cfg, dep: dep} }

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", promhttp.Handler())

	if s.dep.API != nil {
		r.Group(func(pr chi.Router) {
			pr.Use(api.APIKeyAuth(s.dep.APIKeys))
			if s.dep.Redis != nil && s.cfg.APIRateLimitRPM > 0 {
				pr.Use(api.RateLimit(s.dep.Redis, s.cfg.APIRateLimitRPM))
			}
			s.dep.API.Routes(pr)
		})
	}
	return r
}
