// Acortador de URLs — Matías Nicolás Echeveste Cardozo

package enlaces

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/almacen"
)

func nuevo() *Servicio {
	return Nuevo(almacen.NuevaMemoria(nil), "acortar.uy", nil)
}

func TestValidarURL(t *testing.T) {
	s := nuevo()
	validas := []string{"https://www.gub.uy/tramites", "http://ejemplo.com:8080/a?b=c#d", "https://8.8.8.8/dns"}
	for _, u := range validas {
		if _, err := s.ValidarURL(u); err != nil {
			t.Errorf("%q debería ser válida: %v", u, err)
		}
	}
	invalidas := map[string]string{
		"ftp://archivos.uy":                    "sólo se aceptan",
		"javascript:alert(1)":                  "no es válida",
		"https://usuario:clave@banco.uy":       "credenciales",
		"https://acortar.uy/abc":               "no está permitido",
		"http://localhost:3000":                "no está permitido",
		"http://192.168.1.1/admin":             "privadas",
		"http://[::1]/":                        "privadas",
		"https://" + strings.Repeat("a", 2100): "supera",
	}
	for u, mensaje := range invalidas {
		_, err := s.ValidarURL(u)
		if !errors.Is(err, ErrInvalido) || !strings.Contains(err.Error(), mensaje) {
			t.Errorf("%q: se esperaba un error con %q, se obtuvo %v", u, mensaje, err)
		}
	}
}

func TestCrearConCodigoAleatorio(t *testing.T) {
	s := nuevo()
	vistos := map[string]bool{}
	for range 200 {
		e, err := s.Crear(context.Background(), Pedido{URL: "https://ejemplo.uy"})
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Codigo) != largoCodigo || strings.Trim(e.Codigo, alfabeto) != "" {
			t.Fatalf("código mal formado: %q", e.Codigo)
		}
		if vistos[e.Codigo] {
			t.Fatalf("código repetido: %q", e.Codigo)
		}
		vistos[e.Codigo] = true
	}
}

func TestCrearConAliasYVigencia(t *testing.T) {
	ahora := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	s := Nuevo(almacen.NuevaMemoria(func() time.Time { return ahora }), "acortar.uy", func() time.Time { return ahora })
	ctx := context.Background()

	e, err := s.Crear(ctx, Pedido{URL: "https://ejemplo.uy/evento", Alias: "evento-2026", TTL: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if e.Codigo != "evento-2026" || !e.Expira.Equal(ahora.Add(48*time.Hour)) {
		t.Fatalf("enlace inesperado: %+v", e)
	}
	if _, err := s.Crear(ctx, Pedido{URL: "https://otro.uy", Alias: "evento-2026"}); !errors.Is(err, almacen.ErrExiste) {
		t.Fatalf("el alias repetido debería fallar con ErrExiste: %v", err)
	}
	for _, alias := range []string{"ab", "con espacio", "api", "Metricas"} {
		if _, err := s.Crear(ctx, Pedido{URL: "https://otro.uy", Alias: alias}); !errors.Is(err, ErrInvalido) {
			t.Errorf("el alias %q debería ser inválido: %v", alias, err)
		}
	}
	if _, err := s.Crear(ctx, Pedido{URL: "https://otro.uy", TTL: 400 * 24 * time.Hour}); !errors.Is(err, ErrInvalido) {
		t.Errorf("una vigencia de 400 días debería ser inválida: %v", err)
	}
}

// almacenLleno simula colisiones permanentes para verificar el límite de reintentos.
type almacenLleno struct{ almacen.Almacen }

func (almacenLleno) Guardar(context.Context, almacen.Enlace) error { return almacen.ErrExiste }

func TestReintentaAnteColisiones(t *testing.T) {
	s := Nuevo(almacenLleno{almacen.NuevaMemoria(nil)}, "acortar.uy", nil)
	if _, err := s.Crear(context.Background(), Pedido{URL: "https://ejemplo.uy"}); err == nil || !strings.Contains(err.Error(), "código libre") {
		t.Fatalf("se esperaba el error de reintentos agotados, se obtuvo %v", err)
	}
}
