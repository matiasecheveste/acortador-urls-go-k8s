// Acortador de URLs — Matías Nicolás Echeveste Cardozo

// Comando acortador: servidor HTTP del acortador de URLs.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/almacen"
	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/enlaces"
	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/servidor"
)

func entorno(clave, defecto string) string {
	if v, ok := os.LookupEnv(clave); ok && v != "" {
		return v
	}
	return defecto
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := ejecutar(log); err != nil {
		log.Error("finalizó con error", "error", err)
		os.Exit(1)
	}
}

func ejecutar(log *slog.Logger) error {
	publica := entorno("URL_PUBLICA", "http://localhost:8080")
	u, err := url.Parse(publica)
	if err != nil {
		return err
	}
	porSegundo, _ := strconv.ParseFloat(entorno("CREACIONES_POR_SEGUNDO", "1"), 64)
	rafaga, _ := strconv.Atoi(entorno("RAFAGA", "10"))

	var a almacen.Almacen
	if dir := os.Getenv("REDIS_DIRECCION"); dir != "" {
		cliente := redis.NewClient(&redis.Options{Addr: dir, Password: os.Getenv("REDIS_CLAVE")})
		defer cliente.Close()
		a = almacen.NuevoRedis(cliente)
		log.Info("almacén Redis", "direccion", dir)
	} else {
		a = almacen.NuevaMemoria(nil)
		log.Warn("REDIS_DIRECCION vacío: se usa memoria (sólo para desarrollo)")
	}

	srv := servidor.Nuevo(servidor.Config{URLPublica: publica, CreacionesPorIP: porSegundo, Rafaga: rafaga},
		enlaces.Nuevo(a, u.Hostname(), nil), a, log)
	http := &http.Server{
		Addr:              ":" + entorno("PUERTO", "8080"),
		Handler:           srv.Rutas(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Apagado ordenado: Kubernetes envía SIGTERM y espera terminationGracePeriodSeconds.
	ctx, detener := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer detener()
	errores := make(chan error, 1)
	go func() {
		log.Info("escuchando", "direccion", http.Addr)
		errores <- http.ListenAndServe()
	}()

	select {
	case err := <-errores:
		return err
	case <-ctx.Done():
	}
	log.Info("apagando")
	apagado, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()
	if err := http.Shutdown(apagado); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
