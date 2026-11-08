// Acortador de URLs — Matías Nicolás Echeveste Cardozo

// Package servidor expone la API HTTP, las sondas de Kubernetes y las métricas de Prometheus.
package servidor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"

	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/almacen"
	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/enlaces"
)

type Config struct {
	URLPublica      string  // por ejemplo https://acortar.uy
	CreacionesPorIP float64 // pedidos de creación por segundo permitidos por IP
	Rafaga          int
}

type Servidor struct {
	cfg      Config
	enlaces  *enlaces.Servicio
	almacen  almacen.Almacen
	log      *slog.Logger
	registro *prometheus.Registry

	pedidos       *prometheus.CounterVec
	duracion      *prometheus.HistogramVec
	redirecciones prometheus.Counter

	mu        sync.Mutex
	limitesIP map[string]*rate.Limiter
}

func Nuevo(cfg Config, s *enlaces.Servicio, a almacen.Almacen, log *slog.Logger) *Servidor {
	srv := &Servidor{cfg: cfg, enlaces: s, almacen: a, log: log, registro: prometheus.NewRegistry(), limitesIP: map[string]*rate.Limiter{}}
	srv.pedidos = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "acortador_pedidos_total", Help: "Pedidos HTTP por ruta y código."}, []string{"ruta", "codigo"})
	srv.duracion = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "acortador_duracion_segundos", Help: "Duración de los pedidos HTTP.", Buckets: prometheus.DefBuckets}, []string{"ruta"})
	srv.redirecciones = prometheus.NewCounter(prometheus.CounterOpts{Name: "acortador_redirecciones_total", Help: "Redirecciones servidas."})
	srv.registro.MustRegister(srv.pedidos, srv.duracion, srv.redirecciones, collectors.NewGoCollector())
	return srv
}

// Rutas arma el enrutador con los patrones con método y comodines de net/http (Go 1.22+).
func (s *Servidor) Rutas() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enlaces", s.crear)
	mux.HandleFunc("GET /api/enlaces/{codigo}", s.estadisticas)
	mux.HandleFunc("GET /salud", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /listo", s.listo)
	mux.Handle("GET /metricas", promhttp.HandlerFor(s.registro, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /{codigo}", s.redirigir)
	return s.recuperar(s.medir(mux))
}

type respuestaEnlace struct {
	almacen.Enlace
	Corto string `json:"corto"`
}

func (s *Servidor) json(w http.ResponseWriter, estado int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(estado)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Servidor) error(w http.ResponseWriter, estado int, mensaje string) {
	s.json(w, estado, map[string]string{"error": mensaje})
}

func (s *Servidor) crear(w http.ResponseWriter, r *http.Request) {
	if !s.permitido(r) {
		w.Header().Set("Retry-After", "1")
		s.error(w, http.StatusTooManyRequests, "demasiados pedidos, intente en un momento")
		return
	}
	var cuerpo struct {
		URL           string `json:"url"`
		Alias         string `json:"alias"`
		VigenciaHoras int    `json:"vigenciaHoras"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decodificador := json.NewDecoder(r.Body)
	decodificador.DisallowUnknownFields()
	if err := decodificador.Decode(&cuerpo); err != nil {
		var demasiado *http.MaxBytesError
		if errors.As(err, &demasiado) {
			s.error(w, http.StatusRequestEntityTooLarge, "el cuerpo del pedido es demasiado grande")
			return
		}
		s.error(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	e, err := s.enlaces.Crear(r.Context(), enlaces.Pedido{URL: cuerpo.URL, Alias: cuerpo.Alias, TTL: time.Duration(cuerpo.VigenciaHoras) * time.Hour})
	switch {
	case errors.Is(err, enlaces.ErrInvalido):
		s.error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, almacen.ErrExiste):
		s.error(w, http.StatusConflict, "el alias ya está en uso")
	case err != nil:
		s.log.ErrorContext(r.Context(), "no se pudo crear el enlace", "error", err)
		s.error(w, http.StatusServiceUnavailable, "servicio no disponible")
	default:
		w.Header().Set("Location", "/api/enlaces/"+e.Codigo)
		s.json(w, http.StatusCreated, respuestaEnlace{Enlace: e, Corto: s.cfg.URLPublica + "/" + e.Codigo})
	}
}

func (s *Servidor) estadisticas(w http.ResponseWriter, r *http.Request) {
	e, err := s.enlaces.Estadisticas(r.Context(), r.PathValue("codigo"))
	if errors.Is(err, almacen.ErrNoEncontrado) {
		s.error(w, http.StatusNotFound, "enlace no encontrado")
		return
	}
	if err != nil {
		s.error(w, http.StatusServiceUnavailable, "servicio no disponible")
		return
	}
	s.json(w, http.StatusOK, respuestaEnlace{Enlace: e, Corto: s.cfg.URLPublica + "/" + e.Codigo})
}

func (s *Servidor) redirigir(w http.ResponseWriter, r *http.Request) {
	e, err := s.enlaces.Resolver(r.Context(), r.PathValue("codigo"))
	if errors.Is(err, almacen.ErrNoEncontrado) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "servicio no disponible", http.StatusServiceUnavailable)
		return
	}
	s.redirecciones.Inc()
	// 302 y no 301: un 301 queda en la caché del navegador y no se contarían las visitas.
	w.Header().Set("Cache-Control", "private, max-age=0")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, e.URL, http.StatusFound)
}

// listo es la sonda de disponibilidad: falla si Redis no responde, para que Kubernetes
// saque la réplica del Service sin reiniciarla.
func (s *Servidor) listo(w http.ResponseWriter, r *http.Request) {
	ctx, cancelar := context.WithTimeout(r.Context(), time.Second)
	defer cancelar()
	if err := s.almacen.Ping(ctx); err != nil {
		s.error(w, http.StatusServiceUnavailable, "almacén no disponible")
		return
	}
	w.Write([]byte("listo"))
}

func ipCliente(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Servidor) permitido(r *http.Request) bool {
	if s.cfg.CreacionesPorIP <= 0 {
		return true
	}
	ip := ipCliente(r)
	s.mu.Lock()
	limite, ok := s.limitesIP[ip]
	if !ok {
		if len(s.limitesIP) > 10_000 { // cota de memoria: se reinicia el mapa
			s.limitesIP = map[string]*rate.Limiter{}
		}
		limite = rate.NewLimiter(rate.Limit(s.cfg.CreacionesPorIP), s.cfg.Rafaga)
		s.limitesIP[ip] = limite
	}
	s.mu.Unlock()
	return limite.Allow()
}

type registroEstado struct {
	http.ResponseWriter
	estado int
}

func (r *registroEstado) WriteHeader(estado int) {
	r.estado = estado
	r.ResponseWriter.WriteHeader(estado)
}

func (s *Servidor) medir(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		rec := &registroEstado{ResponseWriter: w, estado: http.StatusOK}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		siguiente.ServeHTTP(rec, r)
		ruta := r.Pattern // patrón y no URL concreta: evita métricas con cardinalidad infinita
		if ruta == "" {
			ruta = "desconocida"
		}
		s.pedidos.WithLabelValues(ruta, strconv.Itoa(rec.estado)).Inc()
		s.duracion.WithLabelValues(ruta).Observe(time.Since(inicio).Seconds())
		if ruta != "GET /salud" && ruta != "GET /listo" && ruta != "GET /metricas" {
			s.log.InfoContext(r.Context(), "pedido", "ruta", ruta, "estado", rec.estado, "duracion_ms", time.Since(inicio).Milliseconds())
		}
	})
}

func (s *Servidor) recuperar(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				s.log.ErrorContext(r.Context(), "pánico en el manejador", "valor", p)
				http.Error(w, "error interno", http.StatusInternalServerError)
			}
		}()
		siguiente.ServeHTTP(w, r)
	})
}
