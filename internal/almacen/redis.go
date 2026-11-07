// Acortador de URLs — Matías Nicolás Echeveste Cardozo

package almacen

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis guarda cada enlace como un hash "enlace:<código>" con expiración nativa.
// Es el almacén que se usa en Kubernetes: todas las réplicas comparten el estado.
type Redis struct {
	cliente *redis.Client
}

func NuevoRedis(cliente *redis.Client) *Redis { return &Redis{cliente: cliente} }

func clave(codigo string) string { return "enlace:" + codigo }

// crearSiNoExiste crea el hash y fija su expiración en una sola operación atómica.
var crearSiNoExiste = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'url', ARGV[1], 'creado', ARGV[2], 'expira', ARGV[3], 'visitas', 0)
if tonumber(ARGV[4]) > 0 then redis.call('PEXPIRE', KEYS[1], ARGV[4]) end
return 1
`)

func (r *Redis) Guardar(ctx context.Context, e Enlace) error {
	var expira int64
	var ttl int64
	if !e.Expira.IsZero() {
		expira = e.Expira.UnixMilli()
		ttl = time.Until(e.Expira).Milliseconds()
		if ttl <= 0 {
			ttl = 1
		}
	}
	creado, err := crearSiNoExiste.Run(ctx, r.cliente, []string{clave(e.Codigo)},
		e.URL, e.Creado.UnixMilli(), expira, ttl).Int()
	if err != nil {
		return err
	}
	if creado == 0 {
		return ErrExiste
	}
	return nil
}

func desdeHash(codigo string, campos map[string]string) (Enlace, error) {
	if len(campos) == 0 {
		return Enlace{}, ErrNoEncontrado
	}
	creado, _ := strconv.ParseInt(campos["creado"], 10, 64)
	expira, _ := strconv.ParseInt(campos["expira"], 10, 64)
	visitas, _ := strconv.ParseInt(campos["visitas"], 10, 64)
	e := Enlace{Codigo: codigo, URL: campos["url"], Creado: time.UnixMilli(creado).UTC(), Visitas: visitas}
	if expira > 0 {
		e.Expira = time.UnixMilli(expira).UTC()
	}
	return e, nil
}

func (r *Redis) Obtener(ctx context.Context, codigo string) (Enlace, error) {
	campos, err := r.cliente.HGetAll(ctx, clave(codigo)).Result()
	if err != nil {
		return Enlace{}, err
	}
	return desdeHash(codigo, campos)
}

// visitar incrementa el contador sólo si el enlace existe (evita recrear claves expiradas).
var visitar = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return nil end
redis.call('HINCRBY', KEYS[1], 'visitas', 1)
return redis.call('HGETALL', KEYS[1])
`)

func (r *Redis) Visitar(ctx context.Context, codigo string) (Enlace, error) {
	plano, err := visitar.Run(ctx, r.cliente, []string{clave(codigo)}).StringSlice()
	if errors.Is(err, redis.Nil) {
		return Enlace{}, ErrNoEncontrado
	}
	if err != nil {
		return Enlace{}, err
	}
	campos := make(map[string]string, len(plano)/2)
	for i := 0; i+1 < len(plano); i += 2 {
		campos[plano[i]] = plano[i+1]
	}
	return desdeHash(codigo, campos)
}

func (r *Redis) Ping(ctx context.Context) error { return r.cliente.Ping(ctx).Err() }
