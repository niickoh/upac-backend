package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Valores permitidos de cada pregunta de opción del formulario de Google.
var (
	opcionesExperiencia = map[string]string{
		"sin_experiencia": "Sin experiencia previa",
		"con_experiencia": "Con experiencia",
	}
	opcionesRol = map[string]string{
		"base":  "Base",
		"flyer": "Flyer",
		"ambos": "Ambos",
	}
	opcionesCondicion = map[string]string{
		"menor_18": "Menor de 18 años",
		"mayor_18": "18 años o más",
	}
)

// Mismo patrón que valida el formulario de Google: celular chileno, p. ej. +56912345678.
var celularChileno = regexp.MustCompile(`^(\+?56)?\s?9\s?\d{4}\s?\d{4}$`)

// Al menos dos palabras (primer nombre y apellido paterno), solo letras, apóstrofo o guion.
var nombreValido = regexp.MustCompile(`^\p{L}[\p{L}'’-]* \p{L}[\p{L}'’-]*( \p{L}[\p{L}'’-]*)*$`)

// primerNombreSQL normaliza la primera palabra del nombre igual que los índices de
// la migración 002 (minúsculas y sin tildes). %s es la columna o el parámetro.
const primerNombreSQL = `translate(lower(split_part(%s, ' ', 1)), 'áéíóúüñ', 'aeiouun')`

// preinscripcionInput es el JSON que envía el front (todas las claves en snake_case).
type preinscripcionInput struct {
	NombreCompleto  string          `json:"nombre_completo"`
	FechaNacimiento string          `json:"fecha_nacimiento"` // AAAA-MM-DD
	EdadTemporada   json.RawMessage `json:"edad_temporada"`   // número o texto, como en el form de Google
	Experiencia     string          `json:"experiencia"`
	UltimoEquipo    string          `json:"ultimo_equipo"`
	Rol             string          `json:"rol"`
	NivelGimnasia   string          `json:"nivel_gimnasia"`
	CondicionEdad   string          `json:"condicion_edad"`

	Telefono string `json:"telefono"`
	Email    string `json:"email"`

	ApoderadoNombre   string `json:"apoderado_nombre"`
	ApoderadoTelefono string `json:"apoderado_telefono"`
	ApoderadoEmail    string `json:"apoderado_email"`
}

// preinscripcion es la fila validada y normalizada que se guarda.
type preinscripcion struct {
	ID              int64     `json:"id"`
	Temporada       int       `json:"temporada"`
	NombreCompleto  string    `json:"nombre_completo"`
	FechaNacimiento time.Time `json:"-"`
	EdadTemporada   int       `json:"edad_temporada"`
	Experiencia     string    `json:"experiencia"`
	UltimoEquipo    string    `json:"ultimo_equipo"`
	Rol             string    `json:"rol"`
	NivelGimnasia   string    `json:"nivel_gimnasia"`
	CondicionEdad   string    `json:"condicion_edad"`

	Telefono          *string `json:"telefono"`
	Email             *string `json:"email"`
	ApoderadoNombre   *string `json:"apoderado_nombre"`
	ApoderadoTelefono *string `json:"apoderado_telefono"`
	ApoderadoEmail    *string `json:"apoderado_email"`

	Estado    string    `json:"estado"`
	CreatedAt time.Time `json:"created_at"`
}

// MarshalJSON agrega la fecha como AAAA-MM-DD y las etiquetas legibles de cada opción.
func (p preinscripcion) MarshalJSON() ([]byte, error) {
	type alias preinscripcion
	return json.Marshal(struct {
		alias
		FechaNacimiento    string `json:"fecha_nacimiento"`
		ExperienciaLabel   string `json:"experiencia_label"`
		RolLabel           string `json:"rol_label"`
		CondicionEdadLabel string `json:"condicion_edad_label"`
	}{
		alias:              alias(p),
		FechaNacimiento:    p.FechaNacimiento.Format(time.DateOnly),
		ExperienciaLabel:   opcionesExperiencia[p.Experiencia],
		RolLabel:           opcionesRol[p.Rol],
		CondicionEdadLabel: opcionesCondicion[p.CondicionEdad],
	})
}

// ---------- Validación ----------

func cleanText(s string) string { return strings.Join(strings.Fields(s), " ") }

func ptr(s string) *string { return &s }

// edadAlCierre devuelve la edad cumplida al 31 de diciembre de la temporada.
func edadAlCierre(nacimiento time.Time, temporada int) int {
	return temporada - nacimiento.Year()
}

// normalizarCelular deja el número como +569XXXXXXXX. ok=false si no es un celular chileno válido.
func normalizarCelular(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !celularChileno.MatchString(s) {
		return "", false
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	return "+56" + digits[len(digits)-9:], true
}

func validarEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 || strings.ContainsAny(s, " <>") {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return "", false
	}
	_, domain, _ := strings.Cut(s, "@")
	if !strings.Contains(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return strings.ToLower(s), true
}

func parseEdad(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if unq, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(unq)
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func requiredText(errs map[string]string, field, value, msg string, maxLen int) string {
	value = cleanText(value)
	switch {
	case value == "":
		errs[field] = msg
	case utf8.RuneCountInString(value) > maxLen:
		errs[field] = fmt.Sprintf("Máximo %d caracteres.", maxLen)
	}
	return value
}

// validar aplica las mismas reglas del formulario de Google y devuelve los errores por campo.
func validar(in preinscripcionInput, temporada int, hoy time.Time) (preinscripcion, map[string]string) {
	errs := map[string]string{}
	p := preinscripcion{Temporada: temporada}

	// 1. Datos del deportista
	p.NombreCompleto = requiredText(errs, "nombre_completo", in.NombreCompleto, "Ingresa tu nombre completo.", 150)
	if _, bad := errs["nombre_completo"]; !bad && !nombreValido.MatchString(p.NombreCompleto) {
		errs["nombre_completo"] = "Ingresa al menos tu primer nombre y apellido paterno, solo con letras."
	}

	edadCalculada := 0
	if f := strings.TrimSpace(in.FechaNacimiento); f == "" {
		errs["fecha_nacimiento"] = "Indica tu fecha de nacimiento."
	} else if d, err := time.Parse(time.DateOnly, f); err != nil {
		errs["fecha_nacimiento"] = "Fecha inválida. Usa el formato AAAA-MM-DD."
	} else if !d.Before(hoy) {
		errs["fecha_nacimiento"] = "La fecha de nacimiento debe ser anterior a hoy."
	} else {
		p.FechaNacimiento = d
		edadCalculada = edadAlCierre(d, temporada)
		if edadCalculada < 1 || edadCalculada > 100 {
			errs["fecha_nacimiento"] = "Ingresa una fecha de nacimiento válida."
		}
	}

	if edad, ok := parseEdad(in.EdadTemporada); !ok {
		errs["edad_temporada"] = fmt.Sprintf("Indica tu edad cumplida al 31 de diciembre de %d.", temporada)
	} else if edad < 1 || edad > 100 {
		errs["edad_temporada"] = "Ingresa una edad válida entre 1 y 100 años."
	} else if edadCalculada > 0 && edad != edadCalculada {
		errs["edad_temporada"] = fmt.Sprintf("No coincide con tu fecha de nacimiento: al 31 de diciembre de %d tendrás %d años.", temporada, edadCalculada)
	} else {
		p.EdadTemporada = edad
	}

	// 2. Información deportiva
	p.Experiencia = strings.TrimSpace(in.Experiencia)
	if _, ok := opcionesExperiencia[p.Experiencia]; !ok {
		errs["experiencia"] = "Selecciona tu experiencia en cheerleading."
	}
	p.UltimoEquipo = requiredText(errs, "ultimo_equipo", in.UltimoEquipo, "Indica tu último equipo, año de participación y nivel.", 200)
	p.Rol = strings.TrimSpace(in.Rol)
	if _, ok := opcionesRol[p.Rol]; !ok {
		errs["rol"] = "Selecciona tu rol o posición."
	}
	p.NivelGimnasia = requiredText(errs, "nivel_gimnasia", in.NivelGimnasia, "Indica el nivel de gimnasia que has logrado.", 100)

	p.CondicionEdad = strings.TrimSpace(in.CondicionEdad)
	if _, ok := opcionesCondicion[p.CondicionEdad]; !ok {
		errs["condicion_edad"] = fmt.Sprintf("Indica tu condición de edad al 31 de diciembre de %d.", temporada)
	} else if p.EdadTemporada > 0 {
		esperada := "menor_18"
		if p.EdadTemporada >= 18 {
			esperada = "mayor_18"
		}
		if p.CondicionEdad != esperada {
			errs["condicion_edad"] = fmt.Sprintf("Con %d años al 31 de diciembre de %d corresponde: %s.", p.EdadTemporada, temporada, opcionesCondicion[esperada])
		}
	}

	// 3. Contacto: del deportista si es mayor de edad, del apoderado si es menor
	switch p.CondicionEdad {
	case "mayor_18":
		if tel, ok := normalizarCelular(in.Telefono); ok {
			p.Telefono = ptr(tel)
		} else {
			errs["telefono"] = "Ingresa un celular chileno válido. Ejemplo: +56912345678."
		}
		if email, ok := validarEmail(in.Email); ok {
			p.Email = ptr(email)
		} else {
			errs["email"] = "Ingresa una dirección de correo electrónico válida."
		}
	case "menor_18":
		nombre := requiredText(errs, "apoderado_nombre", in.ApoderadoNombre, "Ingresa el nombre y apellidos del apoderado.", 150)
		if _, bad := errs["apoderado_nombre"]; !bad {
			p.ApoderadoNombre = ptr(nombre)
		}
		if tel, ok := normalizarCelular(in.ApoderadoTelefono); ok {
			p.ApoderadoTelefono = ptr(tel)
		} else {
			errs["apoderado_telefono"] = "Ingresa un celular chileno válido. Ejemplo: +56912345678."
		}
		if email, ok := validarEmail(in.ApoderadoEmail); ok {
			p.ApoderadoEmail = ptr(email)
		} else {
			errs["apoderado_email"] = "Ingresa una dirección de correo electrónico válida."
		}
	}

	return p, errs
}

// ---------- Handlers ----------

// crearPreinscripcion: POST /inscripciones/ (público).
func (a *App) crearPreinscripcion(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "3600")
		writeJSON(w, http.StatusTooManyRequests, detail("Demasiados envíos desde esta conexión. Intenta nuevamente más tarde."))
		return
	}

	in, ok := decodeInput(w, r)
	if !ok {
		return
	}
	p, ok := validarRequest(w, in, a.cfg.Temporada)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	dup, err := a.guardarSinDuplicados(ctx, p, 0, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO inscripciones_preinscripcion (
				temporada, nombre_completo, fecha_nacimiento, edad_temporada,
				experiencia, ultimo_equipo, rol, nivel_gimnasia, condicion_edad,
				telefono, email, apoderado_nombre, apoderado_telefono, apoderado_email
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING id, estado, created_at`,
			p.Temporada, p.NombreCompleto, p.FechaNacimiento, p.EdadTemporada,
			p.Experiencia, p.UltimoEquipo, p.Rol, p.NivelGimnasia, p.CondicionEdad,
			p.Telefono, p.Email, p.ApoderadoNombre, p.ApoderadoTelefono, p.ApoderadoEmail,
		).Scan(&p.ID, &p.Estado, &p.CreatedAt)
	})
	if dup != sinDuplicado {
		writeDuplicado(w, dup, p)
		return
	}
	if err != nil {
		log.Printf("no se pudo guardar la pre-inscripción: %v", err)
		writeJSON(w, http.StatusInternalServerError, detail("No pudimos guardar tu pre-inscripción. Intenta nuevamente."))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"detail": "¡Pre-inscripción recibida! Te contactaremos con todos los detalles.",
		"id":     p.ID,
	})
}

// ---------- Duplicados ----------

type duplicado int

const (
	sinDuplicado duplicado = iota
	duplicadoEmail
	duplicadoFecha
	duplicadoGenerico // lo rechazó un índice único de la base
)

// decodeInput lee el JSON del formulario (un único objeto, máx. 64 KB).
func decodeInput(w http.ResponseWriter, r *http.Request) (preinscripcionInput, bool) {
	var in preinscripcionInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, detail("El cuerpo de la petición debe ser un JSON válido."))
		return in, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, detail("El cuerpo de la petición debe ser un único objeto JSON."))
		return in, false
	}
	return in, true
}

// validarRequest valida contra "hoy" en Chile y responde 400 con los errores por campo.
func validarRequest(w http.ResponseWriter, in preinscripcionInput, temporada int) (preinscripcion, bool) {
	hoy := time.Now().In(chileTZ())
	hoyFecha := time.Date(hoy.Year(), hoy.Month(), hoy.Day(), 0, 0, 0, 0, time.UTC)
	p, errs := validar(in, temporada, hoyFecha)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail":  "Revisa los campos marcados.",
			"errores": errs,
		})
		return p, false
	}
	return p, true
}

// guardarSinDuplicados ejecuta write en una transacción solo si p no duplica a otra
// pre-inscripción (excluyendo excluirID, para las ediciones). Los envíos de una
// misma temporada se serializan para que dos iguales no pasen a la vez.
func (a *App) guardarSinDuplicados(ctx context.Context, p preinscripcion, excluirID int64, write func(pgx.Tx) error) (duplicado, error) {
	emailContacto := p.Email
	if emailContacto == nil {
		emailContacto = p.ApoderadoEmail
	}
	dup := sinDuplicado
	err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('inscripciones_preinscripcion'), $1)`, p.Temporada); err != nil {
			return err
		}
		var err error
		if dup, err = buscarDuplicado(ctx, tx, p, *emailContacto, excluirID); err != nil || dup != sinDuplicado {
			return err
		}
		return write(tx)
	})
	if dup == sinDuplicado && isUniqueViolation(err) {
		dup = duplicadoGenerico
	}
	return dup, err
}

func writeDuplicado(w http.ResponseWriter, dup duplicado, p preinscripcion) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"detail":  mensajeDuplicado(dup, p.NombreCompleto, p.Temporada),
		"errores": camposDuplicado(dup),
	})
}

// buscarDuplicado revisa, en la misma temporada y con el mismo primer nombre,
// si ya hay otra pre-inscripción con el mismo email de contacto o la misma
// fecha de nacimiento. Usa las mismas expresiones que los índices de la migración 002.
func buscarDuplicado(ctx context.Context, tx pgx.Tx, p preinscripcion, emailContacto string, excluirID int64) (duplicado, error) {
	var porEmail, porFecha bool
	err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			coalesce(bool_or(lower(coalesce(email, apoderado_email)) = lower($3::text)), false),
			coalesce(bool_or(fecha_nacimiento = $4::date), false)
		FROM inscripciones_preinscripcion
		WHERE temporada = $1
		  AND id <> $5
		  AND %s = %s
		  AND (lower(coalesce(email, apoderado_email)) = lower($3::text) OR fecha_nacimiento = $4::date)`,
		fmt.Sprintf(primerNombreSQL, "nombre_completo"), fmt.Sprintf(primerNombreSQL, "$2::text")),
		p.Temporada, p.NombreCompleto, emailContacto, p.FechaNacimiento, excluirID,
	).Scan(&porEmail, &porFecha)
	switch {
	case err != nil:
		return sinDuplicado, err
	case porEmail:
		return duplicadoEmail, nil
	case porFecha:
		return duplicadoFecha, nil
	}
	return sinDuplicado, nil
}

func mensajeDuplicado(d duplicado, nombre string, temporada int) string {
	switch d {
	case duplicadoEmail:
		return fmt.Sprintf("Ya existe una pre-inscripción de %s con este correo para la temporada %d.", nombre, temporada)
	case duplicadoFecha:
		return fmt.Sprintf("Ya existe una pre-inscripción de %s con esta fecha de nacimiento para la temporada %d.", nombre, temporada)
	}
	return fmt.Sprintf("Ya existe una pre-inscripción de %s para la temporada %d.", nombre, temporada)
}

func camposDuplicado(d duplicado) map[string]string {
	msg := "Ya existe una pre-inscripción con estos datos."
	switch d {
	case duplicadoEmail:
		return map[string]string{"nombre_completo": msg, "email": msg, "apoderado_email": msg}
	case duplicadoFecha:
		return map[string]string{"nombre_completo": msg, "fecha_nacimiento": msg}
	}
	return map[string]string{"nombre_completo": msg}
}

// chileTZ se usa para decidir qué día es "hoy" al validar la fecha de nacimiento.
func chileTZ() *time.Location {
	if loc, err := time.LoadLocation("America/Santiago"); err == nil {
		return loc
	}
	return time.UTC
}
