# API de Inscripciones UPAC Allstars (Go)

Backend del formulario de **pre-inscripción temporada 2027** del `front-landing`.
Tiene los mismos campos y validaciones que el formulario de Google
"Pre-Inscripciones UPAC Allstars 2027", y guarda en PostgreSQL con la misma
configuración de base de datos que `sphere-go` (pgx, variables `DB_*`, sesión en UTC).

## Levantar en local con Docker (recomendado)

```bash
docker compose up --build
```

- API: http://localhost:8090
- Swagger: http://localhost:8090/swagger/ (activo por defecto en el compose)
- PostgreSQL: `localhost:5433` (usuario, clave y base: `upac`)
- Panel admin del front (`/admin`): usuario `admin`, clave `upac2027` (solo en el compose local)

La tabla se crea sola al iniciar (`DB_AUTO_MIGRATE=True`). Para borrar los datos locales:
`docker compose down -v`.

## Sin Docker

```bash
cp .env.example .env      # completar DB_* (puede ser la misma base de sphere)
go run .
```

Requiere Go 1.25 o superior.

## Compilar para el VPS

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o upac-inscripciones-api .
# o
docker build -t upac-inscripciones-api .
```

## Base de datos

- Misma conexión que sphere-go: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASS`, `DB_SSLMODE`.
- Puede vivir en la **misma base de sphere**: solo agrega la tabla `inscripciones_preinscripcion`
  (convención `<app>_<modelo>`) y la tabla de control `inscripciones_migraciones`. No toca ninguna tabla de sphere.
- Las migraciones están en `internal/app/migrations/*.sql`, van embebidas en el binario y se aplican en orden
  una sola vez. Si prefieres aplicarlas a mano, usa `DB_AUTO_MIGRATE=False` y ejecuta los `.sql`.
- Un deportista no puede pre-inscribirse dos veces en la misma temporada (mismo nombre y fecha de nacimiento): responde 409.

## Endpoints

| Método | Ruta | Acceso | Descripción |
|---|---|---|---|
| `GET` | `/health/` | público | Estado de la API y de la base |
| `POST` | `/inscripciones/` | público | Crea una pre-inscripción |
| `POST` | `/admin/login/` | público | `{usuario, password}` → JWT del admin |
| `GET` | `/admin/inscripciones/` | admin (JWT) | Lista paginada (`pagina`, `por_pagina` 1-100, por defecto 25) con filtros y resumen |
| `GET` | `/admin/inscripciones/excel/` | admin (JWT) | Descarga .xlsx con los mismos filtros |

Filtros (query params, todos opcionales): `q` (busca en nombres, correos, teléfonos y último equipo),
`experiencia`, `rol`, `condicion_edad`, `edad_min`, `edad_max`, `desde` y `hasta` (fecha de inscripción
`AAAA-MM-DD`, hora de Chile) y `temporada` (por defecto `TEMPORADA`).

### Campos (`POST /inscripciones/`)

| Sección del form de Google | Campo JSON | Regla |
|---|---|---|
| 1. Nombres y Apellidos | `nombre_completo` | obligatorio |
| 1. Fecha de Nacimiento | `fecha_nacimiento` | `AAAA-MM-DD`, anterior a hoy |
| 1. Edad cumplida en 2027 | `edad_temporada` | 1 a 100, debe coincidir con la fecha |
| 2. Experiencia en Cheerleading | `experiencia` | `sin_experiencia` \| `con_experiencia` |
| 2. Último Equipo | `ultimo_equipo` | obligatorio |
| 2. Rol o Posición | `rol` | `base` \| `flyer` \| `ambos` |
| 2. Nivel de Gimnasia Logrado | `nivel_gimnasia` | obligatorio |
| 2. Condición de edad al 31/12/2027 | `condicion_edad` | `menor_18` \| `mayor_18`, debe coincidir con la edad |
| 3. Contacto (18 o más) | `telefono`, `email` | obligatorios si `mayor_18` |
| 3. Apoderado (menores) | `apoderado_nombre`, `apoderado_telefono`, `apoderado_email` | obligatorios si `menor_18` |

Los teléfonos usan la misma regla del form de Google (celular chileno, `+56912345678`) y se guardan
normalizados como `+569XXXXXXXX`. Los errores de validación vuelven así:

```json
{ "detail": "Revisa los campos marcados.", "errores": { "telefono": "Ingresa un celular chileno válido. Ejemplo: +56912345678." } }
```

## Seguridad

- CORS: solo `localhost:4200`, `localhost:4300` y los dominios de `CORS_EXTRA_ORIGINS` (pon ahí el dominio publicado del front).
- Límite de `RATE_LIMIT_POR_HORA` envíos por IP (por defecto 20). Detrás de nginx usa `TRUST_PROXY=True`.
- Panel admin: usuario y clave en `ADMIN_USER` / `ADMIN_PASSWORD`; entrega un JWT HS256 que dura `JWT_HORAS`.
  Sin `ADMIN_PASSWORD` el login queda apagado. Máximo 10 intentos de login por IP cada 15 minutos.
  En producción define un `JWT_SECRET` propio (32+ caracteres) y una clave fuerte.
- Swagger viene apagado fuera del compose: déjalo así en producción.
