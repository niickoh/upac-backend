package app

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	cfg     Config
	db      *pgxpool.Pool
	mux     *http.ServeMux
	limiter *rateLimiter

	loginLimiter *rateLimiter
}

func New(ctx context.Context, cfg Config) (*App, error) {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.AutoMigrate {
		if err := migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	a := &App{
		cfg:     cfg,
		db:      pool,
		mux:     http.NewServeMux(),
		limiter: newRateLimiter(cfg.RateLimitPorHora, time.Hour),
		// 10 intentos de login por IP cada 15 minutos
		loginLimiter: newRateLimiter(10, 15*time.Minute),
	}
	a.registerRoutes()
	return a, nil
}

func (a *App) Close() { a.db.Close() }

// registerRoutes declara todos los endpoints de la API.
func (a *App) registerRoutes() {
	a.mux.HandleFunc("GET /health/{$}", a.health)
	a.mux.HandleFunc("POST /inscripciones/{$}", a.crearPreinscripcion)

	// Panel de administración (front: /admin)
	a.mux.HandleFunc("POST /admin/login/{$}", a.adminLogin)
	a.mux.HandleFunc("GET /admin/inscripciones/{$}", a.requireAdmin(a.adminListar))
	a.mux.HandleFunc("GET /admin/inscripciones/excel/{$}", a.requireAdmin(a.adminExcel))

	if a.cfg.SwaggerEnabled {
		a.registerDocs()
	}
}

// ---------- Middleware ----------

func (a *App) Handler() http.Handler { return http.HandlerFunc(a.serve) }

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond))
	}()
	w = rec

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")

	// CORS: solo los orígenes configurados (front-landing local y publicado)
	origin := r.Header.Get("Origin")
	h.Add("Vary", "Origin")
	if origin != "" && slices.Contains(a.cfg.CORSAllowedOrigins, origin) {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "Content-Disposition")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Headers", "accept, authorization, content-type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "86400")
		}
	}
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	defer func() {
		if p := recover(); p != nil {
			log.Printf("panic en %s %s: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
			writeJSON(w, http.StatusInternalServerError, detail("Error interno del servidor."))
		}
	}()

	// Respuestas 404/405 de ServeMux en JSON, igual que el resto de la API
	if _, pattern := a.mux.Handler(r); pattern == "" {
		if allowsOtherMethod(a.mux, r) {
			writeJSON(w, http.StatusMethodNotAllowed, detail("Método no permitido."))
			return
		}
		writeJSON(w, http.StatusNotFound, detail("Recurso no encontrado."))
		return
	}
	a.mux.ServeHTTP(w, r)
}

// allowsOtherMethod indica si la ruta existe con otro método (para responder 405 en vez de 404).
func allowsOtherMethod(mux *http.ServeMux, r *http.Request) bool {
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if m == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, p := mux.Handler(probe); p != "" {
			return true
		}
	}
	return false
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// ---------- Respuestas ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("no se pudo serializar la respuesta: %v", err)
		status, b = http.StatusInternalServerError, []byte(`{"detail":"Error interno del servidor."}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func detail(msg string) map[string]any { return map[string]any{"detail": msg} }

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "detail": "Base de datos no disponible."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// clientIP devuelve la IP del cliente. Detrás de nginx (TRUST_PROXY=True) usa sus headers.
func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- Rate limit ----------

// rateLimiter es una ventana fija por clave (IP). Evita que alguien llene la tabla con spam.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]*rateEntry
}

type rateEntry struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, entries: map[string]*rateEntry{}}
}

// allow registra un intento y devuelve false si la clave superó el límite.
func (l *rateLimiter) allow(key string) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if len(l.entries) > 10000 {
		for k, e := range l.entries {
			if now.After(e.reset) {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[key]
	if !ok || now.After(e.reset) {
		l.entries[key] = &rateEntry{count: 1, reset: now.Add(l.window)}
		return true
	}
	if e.count >= l.limit {
		return false
	}
	e.count++
	return true
}
