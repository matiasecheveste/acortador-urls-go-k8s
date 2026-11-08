// Acortador de URLs — Matías Nicolás Echeveste Cardozo

// Package enlaces contiene las reglas para crear y resolver enlaces cortos.
package enlaces

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/matiasecheveste/acortador-urls-go-k8s/internal/almacen"
)

const (
	alfabeto    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	largoCodigo = 7
	maxIntentos = 5
	largoMaximo = 2048
	ttlMaximo   = 365 * 24 * time.Hour
)

var aliasValido = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// Reservados no pueden usarse como alias porque chocan con rutas de la aplicación.
var reservados = map[string]bool{"api": true, "salud": true, "listo": true, "metricas": true}

// ErrInvalido envuelve los errores de validación de la entrada.
var ErrInvalido = errors.New("pedido inválido")

func invalido(formato string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalido, fmt.Sprintf(formato, args...))
}

// Pedido es lo que envía el cliente para crear un enlace.
type Pedido struct {
	URL   string        `json:"url"`
	Alias string        `json:"alias,omitempty"`
	TTL   time.Duration `json:"-"`
}

type Servicio struct {
	almacen     almacen.Almacen
	dominioBase string
	ahora       func() time.Time
}

// Nuevo crea el servicio. dominioBase es el host público del acortador: no se permite
// acortar URLs que apunten a él para evitar bucles de redirección.
func Nuevo(a almacen.Almacen, dominioBase string, ahora func() time.Time) *Servicio {
	if ahora == nil {
		ahora = time.Now
	}
	return &Servicio{almacen: a, dominioBase: strings.ToLower(dominioBase), ahora: ahora}
}

// ValidarURL acepta sólo http(s) hacia hosts públicos con nombre o IP no privada.
func (s *Servicio) ValidarURL(crudo string) (string, error) {
	if len(crudo) > largoMaximo {
		return "", invalido("la URL supera los %d caracteres", largoMaximo)
	}
	u, err := url.Parse(strings.TrimSpace(crudo))
	if err != nil || u.Host == "" {
		return "", invalido("la URL no es válida")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", invalido("sólo se aceptan URLs http o https")
	}
	if u.User != nil {
		return "", invalido("la URL no puede incluir credenciales")
	}
	host := strings.ToLower(u.Hostname())
	if host == s.dominioBase || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", invalido("el destino no está permitido")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return "", invalido("no se permiten direcciones IP privadas")
	}
	return u.String(), nil
}

func codigoAleatorio() string {
	// rand.Text no permite elegir largo y alfabeto, así que se usa rechazo para evitar sesgo.
	b := make([]byte, 0, largoCodigo)
	buf := make([]byte, 16)
	for len(b) < largoCodigo {
		_, _ = rand.Read(buf)
		for _, x := range buf {
			if int(x) < 256-256%len(alfabeto) && len(b) < largoCodigo {
				b = append(b, alfabeto[int(x)%len(alfabeto)])
			}
		}
	}
	return string(b)
}

// Crear valida el pedido y guarda el enlace con un alias propio o un código aleatorio.
func (s *Servicio) Crear(ctx context.Context, p Pedido) (almacen.Enlace, error) {
	destino, err := s.ValidarURL(p.URL)
	if err != nil {
		return almacen.Enlace{}, err
	}
	if p.TTL < 0 || p.TTL > ttlMaximo {
		return almacen.Enlace{}, invalido("la vigencia debe estar entre 0 y 365 días")
	}
	ahora := s.ahora().UTC().Truncate(time.Millisecond)
	e := almacen.Enlace{URL: destino, Creado: ahora}
	if p.TTL > 0 {
		e.Expira = ahora.Add(p.TTL)
	}

	if p.Alias != "" {
		if !aliasValido.MatchString(p.Alias) {
			return almacen.Enlace{}, invalido("el alias debe tener de 3 a 32 letras, números, guiones o guiones bajos")
		}
		if reservados[strings.ToLower(p.Alias)] {
			return almacen.Enlace{}, invalido("el alias está reservado por la aplicación")
		}
		e.Codigo = p.Alias
		return e, s.almacen.Guardar(ctx, e)
	}

	for range maxIntentos {
		e.Codigo = codigoAleatorio()
		err := s.almacen.Guardar(ctx, e)
		if !errors.Is(err, almacen.ErrExiste) {
			return e, err
		}
	}
	return almacen.Enlace{}, errors.New("no se pudo generar un código libre")
}

func (s *Servicio) Resolver(ctx context.Context, codigo string) (almacen.Enlace, error) {
	return s.almacen.Visitar(ctx, codigo)
}

func (s *Servicio) Estadisticas(ctx context.Context, codigo string) (almacen.Enlace, error) {
	return s.almacen.Obtener(ctx, codigo)
}
