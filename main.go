// API de pre-inscripciones de UPAC Allstars (temporada 2027).
// Recibe el formulario de pre-inscripción del front-landing y lo guarda en
// PostgreSQL, con la misma configuración de base de datos que sphere-go.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // zona America/Santiago aunque el sistema no tenga tzdata

	"github.com/joho/godotenv"

	"upac-inscripciones-api/internal/app"
)

func main() {
	// Carga .env si existe, sin pisar variables ya definidas (igual que sphere-go)
	_ = godotenv.Load()

	cfg := app.LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatalf("no se pudo iniciar la API: %v", err)
	}
	defer a.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("API de inscripciones UPAC escuchando en %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("error del servidor: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("cerrando servidor...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
