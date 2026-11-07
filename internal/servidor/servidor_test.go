// Acortador de URLs — Matías Nicolás Echeveste Cardozo

package servidor

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/almacen"
	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/enlaces"
)

func probar(t *testing.T, cfg Config) (*httptest.Server, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	a := almacen.NuevoRedis(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if cfg.URLPublica == "" {
		cfg.URLPublica = "https://acortar.uy"
	}
	srv := httptest.NewServer(Nuevo(cfg, enlaces.Nuevo(a, "acortar.uy", nil), a, log).Rutas())
	t.Cleanup(srv.Close)
	return srv, mr
}

var sinSeguirRedirecciones = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func crear(t *testing.T, srv *httptest.Server, cuerpo string) (*http.Response, map[string]any) {
	t.Helper()
	r, err := http.Post(srv.URL+"/api/enlaces", "application/json", strings.NewReader(cuerpo))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var datos map[string]any
	_ = json.NewDecoder(r.Body).Decode(&datos)
	return r, datos
}

func TestCrearRedirigirYContar(t *testing.T) {
	srv, _ := probar(t, Config{})
	r, datos := crear(t, srv, `{"url": "https://www.gub.uy/tramites", "alias": "tramites"}`)
	if r.StatusCode != http.StatusCreated || datos["corto"] != "https://acortar.uy/tramites" || r.Header.Get("Location") != "/api/enlaces/tramites" {
		t.Fatalf("creación inesperada: %d %v", r.StatusCode, datos)
	}

	for range 2 {
		red, err := sinSeguirRedirecciones.Get(srv.URL + "/tramites")
		if err != nil {
			t.Fatal(err)
		}
		red.Body.Close()
		if red.StatusCode != http.StatusFound || red.Header.Get("Location") != "https://www.gub.uy/tramites" {
			t.Fatalf("redirección inesperada: %d %s", red.StatusCode, red.Header.Get("Location"))
		}
	}

	est, _ := http.Get(srv.URL + "/api/enlaces/tramites")
	var e map[string]any
	_ = json.NewDecoder(est.Body).Decode(&e)
	est.Body.Close()
	if e["visitas"] != float64(2) {
		t.Fatalf("se esperaban 2 visitas: %v", e)
	}
}

func TestCodigosDeError(t *testing.T) {
	srv, _ := probar(t, Config{})
	casos := []struct {
		cuerpo string
		estado int
	}{
		{`{"url": "ftp://x.uy"}`, http.StatusUnprocessableEntity},
		{`{"url": "https://x.uy", "extra": 1}`, http.StatusBadRequest},
		{`no es json`, http.StatusBadRequest},
		{`{"url": "https://x.uy", "alias": "repetido"}`, http.StatusCreated},
		{`{"url": "https://y.uy", "alias": "repetido"}`, http.StatusConflict},
		{`{"url": "https://x.uy", "vigenciaHoras": 9999}`, http.StatusUnprocessableEntity},
	}
	for _, c := range casos {
		if r, _ := crear(t, srv, c.cuerpo); r.StatusCode != c.estado {
			t.Errorf("%s: se esperaba %d, se obtuvo %d", c.cuerpo, c.estado, r.StatusCode)
		}
	}
	if r, _ := http.Get(srv.URL + "/inexistente"); r.StatusCode != http.StatusNotFound {
		t.Errorf("código inexistente: %d", r.StatusCode)
	}
}

func TestLimitePorIP(t *testing.T) {
	srv, _ := probar(t, Config{CreacionesPorIP: 0.001, Rafaga: 3})
	estados := []int{}
	for range 5 {
		r, _ := crear(t, srv, `{"url": "https://ejemplo.uy"}`)
		estados = append(estados, r.StatusCode)
	}
	if estados[2] != http.StatusCreated || estados[3] != http.StatusTooManyRequests || estados[4] != http.StatusTooManyRequests {
		t.Fatalf("estados inesperados: %v", estados)
	}
}

func TestSondasYMetricas(t *testing.T) {
	srv, mr := probar(t, Config{})
	if r, _ := http.Get(srv.URL + "/listo"); r.StatusCode != http.StatusOK {
		t.Fatalf("listo: %d", r.StatusCode)
	}
	mr.SetError("caído")
	if r, _ := http.Get(srv.URL + "/listo"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("con Redis caído, /listo debería dar 503: %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/salud"); r.StatusCode != http.StatusOK {
		t.Fatalf("la salud no depende de Redis: %d", r.StatusCode)
	}
	mr.SetError("")

	crear(t, srv, `{"url": "https://ejemplo.uy", "alias": "metrica"}`)
	r, _ := http.Get(srv.URL + "/metricas")
	cuerpo, _ := io.ReadAll(r.Body)
	r.Body.Close()
	for _, esperado := range []string{`acortador_pedidos_total{codigo="201",ruta="POST /api/enlaces"} 1`, "go_goroutines"} {
		if !strings.Contains(string(cuerpo), esperado) {
			t.Errorf("las métricas no contienen %q", esperado)
		}
	}
}
