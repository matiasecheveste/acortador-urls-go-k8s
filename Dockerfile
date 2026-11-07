# Acortador de URLs — Matías Nicolás Echeveste Cardozo
FROM golang:1.27 AS compilacion
WORKDIR /fuente
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /acortador ./cmd/acortador

# Imagen mínima sin shell ni gestor de paquetes, con usuario sin privilegios.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=compilacion /acortador /acortador
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/acortador"]
