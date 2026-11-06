package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Login del administrador: un único usuario definido por ADMIN_USER / ADMIN_PASSWORD.
// Devuelve un JWT HS256 (misma librería que sphere-go) que se envía como Bearer.

type adminClaims struct {
	Rol string `json:"rol"`
	jwt.RegisteredClaims
}

// sameSecret compara en tiempo constante, sin filtrar el largo de la clave.
func sameSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// adminLogin: POST /admin/login/ {"usuario": "...", "password": "..."}.
func (a *App) adminLogin(w http.ResponseWriter, r *http.Request) {
	if a.cfg.AdminPassword == "" {
		writeJSON(w, http.StatusForbidden, detail("Acceso de administrador deshabilitado: define ADMIN_PASSWORD en el servidor."))
		return
	}
	if !a.loginLimiter.allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "900")
		writeJSON(w, http.StatusTooManyRequests, detail("Demasiados intentos. Espera 15 minutos e intenta nuevamente."))
		return
	}

	var in struct {
		Usuario  string `json:"usuario"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, detail("El cuerpo de la petición debe ser un JSON válido."))
		return
	}

	userOK := sameSecret(strings.TrimSpace(in.Usuario), a.cfg.AdminUser)
	passOK := sameSecret(in.Password, a.cfg.AdminPassword)
	if !userOK || !passOK {
		writeJSON(w, http.StatusUnauthorized, detail("Usuario o contraseña incorrectos."))
		return
	}

	now := time.Now()
	exp := now.Add(a.cfg.JWTDuracion)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{
		Rol: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   a.cfg.AdminUser,
			Issuer:    "upac-inscripciones",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}).SignedString(a.cfg.JWTSecret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, detail("No se pudo iniciar sesión."))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":   token,
		"usuario": a.cfg.AdminUser,
		"expira":  exp.UTC().Format(time.RFC3339),
	})
}

// requireAdmin protege un handler: exige un JWT válido de administrador.
func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			a.unauthorized(w, "Debes iniciar sesión.")
			return
		}
		claims := &adminClaims{}
		_, err := jwt.ParseWithClaims(strings.TrimSpace(raw), claims,
			func(*jwt.Token) (any, error) { return a.cfg.JWTSecret, nil },
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer("upac-inscripciones"),
			jwt.WithExpirationRequired(),
		)
		if err != nil || claims.Rol != "admin" {
			msg := "Sesión inválida. Inicia sesión nuevamente."
			if errors.Is(err, jwt.ErrTokenExpired) {
				msg = "Tu sesión expiró. Inicia sesión nuevamente."
			}
			a.unauthorized(w, msg)
			return
		}
		next(w, r)
	}
}

func (a *App) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
	writeJSON(w, http.StatusUnauthorized, detail(msg))
}
