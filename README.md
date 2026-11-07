# Acortador de URLs — Go, Redis y Kubernetes

Servicio para acortar URLs escrito en **Go 1.27** sólo con la biblioteca estándar para HTTP. Usa **Redis** para el estado compartido, expone métricas **Prometheus** y está pensado para desplegarse en **Kubernetes** con manifiestos Kustomize. La CI lo prueba de punta a punta en un clúster *kind* real.

## API

| Método | Ruta | |
|---|---|---|
| POST | `/api/enlaces` | `{"url": "https://…", "alias": "opcional", "vigenciaHoras": 48}` → `201` |
| GET | `/api/enlaces/{codigo}` | datos y contador de visitas |
| GET | `/{codigo}` | `302` hacia el destino |
| GET | `/salud` | sonda de vida: el proceso responde |
| GET | `/listo` | sonda de disponibilidad: Redis responde |
| GET | `/metricas` | formato Prometheus |

## Decisiones de diseño

- **Interfaz `Almacen` con dos implementaciones**, en memoria y en Redis, y **las mismas pruebas de contrato** corriendo contra ambas. Redis se prueba con *miniredis*, sin servidor externo. La expiración se avanza artificialmente en las pruebas.
- **Atomicidad en Redis**: crear un enlace (verificar que no exista, guardar el hash y fijar el TTL) y contar una visita son *scripts* Lua de una sola operación. Una prueba lanza 50 visitas concurrentes y verifica que no se pierde ninguna.
- **Códigos aleatorios** de 7 caracteres base62 con `crypto/rand` y muestreo por rechazo (sin sesgo de módulo). Si hay colisión se reintenta hasta 5 veces.
- **Validación de destinos**:
  - sólo `http` y `https`, sin credenciales en la URL;
  - no se aceptan IPs privadas, *loopback* ni `localhost`;
  - no se puede acortar el propio dominio, para evitar bucles.
- **302 y no 301**: un 301 queda en la caché del navegador y las visitas siguientes no se contarían.
- **Límite de creación por IP** (`x/time/rate`, cubeta de fichas) y cuerpo limitado a 4 KB, que además rechaza campos desconocidos.
- **Métricas con el patrón de ruta** (`r.Pattern`) y no la URL concreta, para que la cardinalidad de las etiquetas no crezca sin límite.
- **Apagado ordenado** ante `SIGTERM`, con tiempos de espera de lectura y escritura configurados en `http.Server`.

## Kubernetes (`deploy/k8s`)

| Recurso | Configuración |
|---|---|
| Namespace | Pod Security `restricted` |
| Deployment | 2 réplicas; actualización sin pérdida de servicio (`maxUnavailable: 0`); sondas separadas de vida y disponibilidad; límites de recursos; usuario no root, sistema de archivos de sólo lectura, sin *capabilities*; reparto entre nodos |
| StatefulSet de Redis | volumen persistente y AOF |
| HPA | 2 a 6 réplicas por CPU |
| PodDisruptionBudget | mínimo 1 réplica disponible |
| NetworkPolicy | sólo el acortador puede hablar con Redis |
| Ingress | expone el servicio hacia afuera |

```bash
kubectl apply -k deploy/k8s
```

La imagen se construye en dos etapas sobre `distroless/static` (sin shell) y ejecuta como el usuario 65532.

## Integración continua

1. **Pruebas**: `gofmt`, `go vet`, `staticcheck` y `go test -race` con cobertura.
2. **Manifiestos**: `kustomize` + `kubeconform -strict` contra el esquema de Kubernetes 1.34.
3. **Extremo a extremo en kind**: se construye la imagen, se despliega en un clúster kind, se crea un enlace a través del Service y se sigue la redirección. Después se reinician las réplicas y se verifica que el enlace y su contador siguen ahí, porque el estado vive en Redis.
4. **Imagen**: en `main` se publica en GitHub Container Registry.

## Uso local

```bash
go test ./...                       # 17 casos de prueba
docker compose up --build           # acortador + Redis en http://localhost:8080
curl -X POST localhost:8080/api/enlaces -d '{"url":"https://www.gub.uy","alias":"gubuy"}'
curl -i localhost:8080/gubuy
```

| Variable | Valor por defecto |
|---|---|
| `PUERTO` | `8080` |
| `URL_PUBLICA` | `http://localhost:8080` |
| `REDIS_DIRECCION` | vacío = memoria |
| `REDIS_CLAVE` | |
| `CREACIONES_POR_SEGUNDO` | `1` |
| `RAFAGA` | `10` |

## Autor

Matías Nicolás Echeveste Cardozo — licencia MIT.
