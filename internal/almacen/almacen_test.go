// Acortador de URLs — Matías Nicolás Echeveste Cardozo

package almacen

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Las mismas pruebas de contrato corren contra ambas implementaciones.
func implementaciones(t *testing.T) map[string]func() (Almacen, func(time.Duration)) {
	return map[string]func() (Almacen, func(time.Duration)){
		"memoria": func() (Almacen, func(time.Duration)) {
			ahora := time.Now()
			m := NuevaMemoria(func() time.Time { return ahora })
			return m, func(d time.Duration) { ahora = ahora.Add(d) }
		},
		"redis": func() (Almacen, func(time.Duration)) {
			servidor := miniredis.RunT(t)
			cliente := redis.NewClient(&redis.Options{Addr: servidor.Addr()})
			t.Cleanup(func() { _ = cliente.Close() })
			return NuevoRedis(cliente), servidor.FastForward
		},
	}
}

func TestContratoDelAlmacen(t *testing.T) {
	ctx := context.Background()
	for nombre, crear := range implementaciones(t) {
		t.Run(nombre, func(t *testing.T) {
			a, _ := crear()
			e := Enlace{Codigo: "abc1234", URL: "https://www.gub.uy", Creado: time.Now().UTC().Truncate(time.Millisecond)}
			if err := a.Guardar(ctx, e); err != nil {
				t.Fatal(err)
			}
			if err := a.Guardar(ctx, e); !errors.Is(err, ErrExiste) {
				t.Fatalf("se esperaba ErrExiste, se obtuvo %v", err)
			}
			for range 3 {
				if _, err := a.Visitar(ctx, "abc1234"); err != nil {
					t.Fatal(err)
				}
			}
			obtenido, err := a.Obtener(ctx, "abc1234")
			if err != nil {
				t.Fatal(err)
			}
			if obtenido.URL != e.URL || obtenido.Visitas != 3 || !obtenido.Creado.Equal(e.Creado) {
				t.Fatalf("enlace inesperado: %+v", obtenido)
			}
			if _, err := a.Obtener(ctx, "noexiste"); !errors.Is(err, ErrNoEncontrado) {
				t.Fatalf("se esperaba ErrNoEncontrado, se obtuvo %v", err)
			}
			if _, err := a.Visitar(ctx, "noexiste"); !errors.Is(err, ErrNoEncontrado) {
				t.Fatalf("visitar un código inexistente debería fallar, se obtuvo %v", err)
			}
		})
	}
}

func TestLosEnlacesExpiran(t *testing.T) {
	ctx := context.Background()
	for nombre, crear := range implementaciones(t) {
		t.Run(nombre, func(t *testing.T) {
			a, avanzar := crear()
			e := Enlace{Codigo: "temporal", URL: "https://ejemplo.uy", Creado: time.Now(), Expira: time.Now().Add(time.Hour)}
			if err := a.Guardar(ctx, e); err != nil {
				t.Fatal(err)
			}
			avanzar(2 * time.Hour)
			if _, err := a.Visitar(ctx, "temporal"); !errors.Is(err, ErrNoEncontrado) {
				t.Fatalf("el enlace debería haber expirado, se obtuvo %v", err)
			}
			// Un código expirado puede reutilizarse.
			if err := a.Guardar(ctx, Enlace{Codigo: "temporal", URL: "https://otro.uy", Creado: time.Now()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVisitasConcurrentesNoSePierden(t *testing.T) {
	ctx := context.Background()
	for nombre, crear := range implementaciones(t) {
		t.Run(nombre, func(t *testing.T) {
			a, _ := crear()
			_ = a.Guardar(ctx, Enlace{Codigo: "popular", URL: "https://ejemplo.uy", Creado: time.Now()})
			var wg sync.WaitGroup
			for range 50 {
				wg.Go(func() { _, _ = a.Visitar(ctx, "popular") })
			}
			wg.Wait()
			if e, _ := a.Obtener(ctx, "popular"); e.Visitas != 50 {
				t.Fatalf("se esperaban 50 visitas, hay %d", e.Visitas)
			}
		})
	}
}
