// Acortador de URLs — Matías Nicolás Echeveste Cardozo

// Package almacen define la persistencia de enlaces y sus implementaciones en memoria y Redis.
package almacen

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrExiste indica que el código ya está en uso.
	ErrExiste = errors.New("el código ya existe")
	// ErrNoEncontrado indica que el código no existe o expiró.
	ErrNoEncontrado = errors.New("enlace no encontrado")
)

// Enlace es una URL acortada.
type Enlace struct {
	Codigo  string    `json:"codigo"`
	URL     string    `json:"url"`
	Creado  time.Time `json:"creado"`
	Expira  time.Time `json:"expira,omitzero"`
	Visitas int64     `json:"visitas"`
}

// Almacen guarda enlaces. Guardar debe ser atómico: si el código existe, devuelve ErrExiste.
type Almacen interface {
	Guardar(ctx context.Context, e Enlace) error
	Obtener(ctx context.Context, codigo string) (Enlace, error)
	// Visitar devuelve el enlace e incrementa su contador de visitas.
	Visitar(ctx context.Context, codigo string) (Enlace, error)
	Ping(ctx context.Context) error
}

// Memoria es un almacén para desarrollo y pruebas. No comparte estado entre réplicas.
type Memoria struct {
	mu      sync.Mutex
	enlaces map[string]Enlace
	ahora   func() time.Time
}

// NuevaMemoria crea un almacén en memoria. ahora permite fijar el reloj en las pruebas.
func NuevaMemoria(ahora func() time.Time) *Memoria {
	if ahora == nil {
		ahora = time.Now
	}
	return &Memoria{enlaces: map[string]Enlace{}, ahora: ahora}
}

func (m *Memoria) vigente(codigo string) (Enlace, bool) {
	e, ok := m.enlaces[codigo]
	if ok && !e.Expira.IsZero() && !m.ahora().Before(e.Expira) {
		delete(m.enlaces, codigo)
		return Enlace{}, false
	}
	return e, ok
}

func (m *Memoria) Guardar(_ context.Context, e Enlace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vigente(e.Codigo); ok {
		return ErrExiste
	}
	m.enlaces[e.Codigo] = e
	return nil
}

func (m *Memoria) Obtener(_ context.Context, codigo string) (Enlace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.vigente(codigo); ok {
		return e, nil
	}
	return Enlace{}, ErrNoEncontrado
}

func (m *Memoria) Visitar(_ context.Context, codigo string) (Enlace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vigente(codigo)
	if !ok {
		return Enlace{}, ErrNoEncontrado
	}
	e.Visitas++
	m.enlaces[codigo] = e
	return e, nil
}

func (m *Memoria) Ping(context.Context) error { return nil }
