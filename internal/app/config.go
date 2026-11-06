package app

import (
	"crypto/rand"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config se lee de variables de entorno (o del .env). Las DB_* tienen los
// mismos nombres y valores por defecto que en sphere-go.
type Config struct {
	Addr string

	DBHost, DBPort, DBName, DBUser, DBPass string
	DBSSLMode                              string
	AutoMigrate                            bool

	// Temporada a la que se inscribe; la edad se calcula al 31 de diciembre de este año.
	Temporada int

	// Login del panel de administración (/admin en el front). Sin ADMIN_PASSWORD, el login queda apagado.
	AdminUser     string
	AdminPassword string
	JWTSecret     []byte
	JWTDuracion   time.Duration

	RateLimitPorHora int
	TrustProxy       bool

	CORSAllowedOrigins []string

	SwaggerEnabled bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return n
	}
	return def
}

func LoadConfig() Config {
	port := env("PORT", "8090")

	origins := []string{
		"http://localhost:4200",
		"http://127.0.0.1:4200",
		"http://localhost:4300",
		"http://127.0.0.1:4300",
	}
	// Dominio(s) donde está publicado el front-landing, separados por coma
	for _, o := range strings.Split(env("CORS_EXTRA_ORIGINS", ""), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins = append(origins, o)
		}
	}

	return Config{
		Addr: env("LISTEN_ADDR", ":"+port),

		DBHost:      os.Getenv("DB_HOST"),
		DBPort:      env("DB_PORT", "5432"),
		DBName:      os.Getenv("DB_NAME"),
		DBUser:      os.Getenv("DB_USER"),
		DBPass:      os.Getenv("DB_PASS"),
		DBSSLMode:   env("DB_SSLMODE", "prefer"),
		AutoMigrate: env("DB_AUTO_MIGRATE", "True") == "True",

		Temporada: envInt("TEMPORADA", 2027),

		AdminUser:     strings.TrimSpace(env("ADMIN_USER", "admin")),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		JWTSecret:     jwtSecret(),
		JWTDuracion:   time.Duration(envInt("JWT_HORAS", 8)) * time.Hour,

		RateLimitPorHora: envInt("RATE_LIMIT_POR_HORA", 20),
		// True solo si la API está detrás de nginx (usa X-Real-IP / X-Forwarded-For)
		TrustProxy: env("TRUST_PROXY", "False") == "True",

		CORSAllowedOrigins: origins,

		// /swagger/ y /redoc/: activar en dev, dejar apagado en producción
		SwaggerEnabled: env("SWAGGER_ENABLED", "False") == "True",
	}
}

// jwtSecret usa JWT_SECRET; si no está definido genera uno aleatorio (las sesiones se cierran al reiniciar).
func jwtSecret() []byte {
	if s := os.Getenv("JWT_SECRET"); len(s) >= 32 {
		return []byte(s)
	} else if s != "" {
		log.Print("JWT_SECRET tiene menos de 32 caracteres: se usará uno aleatorio")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("no se pudo generar JWT_SECRET: %v", err)
	}
	return b
}
