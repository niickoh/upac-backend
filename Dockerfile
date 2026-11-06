# Imagen de producción de la API de inscripciones UPAC (binario estático, mismo esquema que sphere-go)
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/upac-inscripciones-api .

FROM alpine:3.20
# Certificados para HTTPS y zona horaria
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H upac
COPY --from=build /out/upac-inscripciones-api /usr/local/bin/upac-inscripciones-api
USER upac
ENV PORT=8090
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/upac-inscripciones-api"]
