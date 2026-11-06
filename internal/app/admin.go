package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// ---------- Filtros ----------

// filtros son los query params que comparten el listado y la descarga Excel.
type filtros struct {
	Temporada   int
	Q           string // busca en nombres, correos, teléfonos y último equipo
	Experiencia string
	Rol         string
	Condicion   string
	EdadMin     int
	EdadMax     int
	Desde       time.Time // fecha de inscripción (día en hora de Chile), inclusive
	Hasta       time.Time // inclusive
}

func (a *App) parseFiltros(r *http.Request) (filtros, string) {
	q := r.URL.Query()
	f := filtros{Temporada: a.cfg.Temporada}

	if v := q.Get("temporada"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return f, "El parámetro temporada debe ser un año."
		}
		f.Temporada = n
	}
	f.Q = cleanText(q.Get("q"))
	if len([]rune(f.Q)) > 100 {
		return f, "La búsqueda admite hasta 100 caracteres."
	}

	enOpciones := func(param string, opciones map[string]string) (string, bool) {
		v := strings.TrimSpace(q.Get(param))
		if v == "" {
			return "", true
		}
		_, ok := opciones[v]
		return v, ok
	}
	var ok bool
	if f.Experiencia, ok = enOpciones("experiencia", opcionesExperiencia); !ok {
		return f, "Valor de experiencia inválido."
	}
	if f.Rol, ok = enOpciones("rol", opcionesRol); !ok {
		return f, "Valor de rol inválido."
	}
	if f.Condicion, ok = enOpciones("condicion_edad", opcionesCondicion); !ok {
		return f, "Valor de condición de edad inválido."
	}

	edad := func(param string) (int, bool) {
		v := strings.TrimSpace(q.Get(param))
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 1 && n <= 100
	}
	if f.EdadMin, ok = edad("edad_min"); !ok {
		return f, "La edad mínima debe estar entre 1 y 100."
	}
	if f.EdadMax, ok = edad("edad_max"); !ok {
		return f, "La edad máxima debe estar entre 1 y 100."
	}

	fecha := func(param string) (time.Time, bool) {
		v := strings.TrimSpace(q.Get(param))
		if v == "" {
			return time.Time{}, true
		}
		t, err := time.ParseInLocation(time.DateOnly, v, chileTZ())
		return t, err == nil
	}
	if f.Desde, ok = fecha("desde"); !ok {
		return f, "La fecha desde debe tener formato AAAA-MM-DD."
	}
	if f.Hasta, ok = fecha("hasta"); !ok {
		return f, "La fecha hasta debe tener formato AAAA-MM-DD."
	}
	return f, ""
}

// likeEscape escapa los comodines de ILIKE para buscar el texto tal cual.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// where arma la condición SQL con parámetros posicionales (nunca concatena valores).
func (f filtros) where() (string, []any) {
	conds := []string{"temporada = $1"}
	args := []any{f.Temporada}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}

	if f.Q != "" {
		add(`(nombre_completo ILIKE ? OR apoderado_nombre ILIKE ? OR email ILIKE ?
			OR apoderado_email ILIKE ? OR telefono ILIKE ? OR apoderado_telefono ILIKE ?
			OR ultimo_equipo ILIKE ?)`, "%"+likeEscape(f.Q)+"%")
	}
	if f.Experiencia != "" {
		add("experiencia = ?", f.Experiencia)
	}
	if f.Rol != "" {
		add("rol = ?", f.Rol)
	}
	if f.Condicion != "" {
		add("condicion_edad = ?", f.Condicion)
	}
	if f.EdadMin > 0 {
		add("edad_temporada >= ?", f.EdadMin)
	}
	if f.EdadMax > 0 {
		add("edad_temporada <= ?", f.EdadMax)
	}
	if !f.Desde.IsZero() {
		add("created_at >= ?", f.Desde)
	}
	if !f.Hasta.IsZero() {
		add("created_at < ?", f.Hasta.AddDate(0, 0, 1))
	}
	return strings.Join(conds, " AND "), args
}

// Paginación del listado: ?pagina=1&por_pagina=25.
const (
	porPaginaDefault = 25
	porPaginaMax     = 100
)

type paginacion struct {
	Pagina    int
	PorPagina int
}

func parsePaginacion(r *http.Request) (paginacion, string) {
	q := r.URL.Query()
	p := paginacion{Pagina: 1, PorPagina: porPaginaDefault}
	if v := strings.TrimSpace(q.Get("pagina")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, "El parámetro pagina debe ser un número mayor o igual a 1."
		}
		p.Pagina = n
	}
	if v := strings.TrimSpace(q.Get("por_pagina")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > porPaginaMax {
			return p, fmt.Sprintf("El parámetro por_pagina debe estar entre 1 y %d.", porPaginaMax)
		}
		p.PorPagina = n
	}
	return p, ""
}

// resumen cuenta, sobre TODO el conjunto filtrado (no solo la página), cuántos hay de cada tipo.
func (a *App) resumen(ctx context.Context, f filtros) (map[string]int, error) {
	where, args := f.where()
	var total, menor, mayor, sinExp, conExp, base, flyer, ambos int
	err := a.db.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE condicion_edad = 'menor_18'),
		       count(*) FILTER (WHERE condicion_edad = 'mayor_18'),
		       count(*) FILTER (WHERE experiencia = 'sin_experiencia'),
		       count(*) FILTER (WHERE experiencia = 'con_experiencia'),
		       count(*) FILTER (WHERE rol = 'base'),
		       count(*) FILTER (WHERE rol = 'flyer'),
		       count(*) FILTER (WHERE rol = 'ambos')
		FROM inscripciones_preinscripcion
		WHERE `+where, args...).Scan(&total, &menor, &mayor, &sinExp, &conExp, &base, &flyer, &ambos)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"total": total, "menor_18": menor, "mayor_18": mayor,
		"sin_experiencia": sinExp, "con_experiencia": conExp,
		"base": base, "flyer": flyer, "ambos": ambos,
	}, nil
}

// buscar devuelve las pre-inscripciones que cumplen los filtros, la más reciente primero.
// Con limit > 0 devuelve solo esa página; con limit = 0 devuelve todas (Excel).
func (a *App) buscar(ctx context.Context, f filtros, limit, offset int) ([]preinscripcion, error) {
	where, args := f.where()
	pagina := ""
	if limit > 0 {
		args = append(args, limit, offset)
		pagina = fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := a.db.Query(ctx, `
		SELECT id, temporada, nombre_completo, fecha_nacimiento, edad_temporada,
		       experiencia, ultimo_equipo, rol, nivel_gimnasia, condicion_edad,
		       telefono, email, apoderado_nombre, apoderado_telefono, apoderado_email,
		       estado, created_at
		FROM inscripciones_preinscripcion
		WHERE `+where+`
		ORDER BY created_at DESC, id DESC`+pagina, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []preinscripcion{}
	for rows.Next() {
		var p preinscripcion
		var temp, edad int16
		if err := rows.Scan(&p.ID, &temp, &p.NombreCompleto, &p.FechaNacimiento, &edad,
			&p.Experiencia, &p.UltimoEquipo, &p.Rol, &p.NivelGimnasia, &p.CondicionEdad,
			&p.Telefono, &p.Email, &p.ApoderadoNombre, &p.ApoderadoTelefono, &p.ApoderadoEmail,
			&p.Estado, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Temporada, p.EdadTemporada = int(temp), int(edad)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- Handlers ----------

// adminListar: GET /admin/inscripciones/?pagina=&por_pagina=&q=&experiencia=&rol=&condicion_edad=&edad_min=&edad_max=&desde=&hasta=
func (a *App) adminListar(w http.ResponseWriter, r *http.Request) {
	f, msg := a.parseFiltros(r)
	if msg == "" {
		var pag paginacion
		if pag, msg = parsePaginacion(r); msg == "" {
			a.listarPagina(w, r, f, pag)
			return
		}
	}
	writeJSON(w, http.StatusBadRequest, detail(msg))
}

func (a *App) listarPagina(w http.ResponseWriter, r *http.Request, f filtros, pag paginacion) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	resumen, err := a.resumen(ctx, f)
	if err != nil {
		log.Printf("no se pudo contar pre-inscripciones: %v", err)
		writeJSON(w, http.StatusInternalServerError, detail("Error interno del servidor."))
		return
	}
	total := resumen["total"]
	totalPaginas := max(1, (total+pag.PorPagina-1)/pag.PorPagina)
	// Si la página pedida ya no existe (p. ej. al filtrar), se entrega la última
	pag.Pagina = min(pag.Pagina, totalPaginas)

	lista, err := a.buscar(ctx, f, pag.PorPagina, (pag.Pagina-1)*pag.PorPagina)
	if err != nil {
		log.Printf("no se pudo listar pre-inscripciones: %v", err)
		writeJSON(w, http.StatusInternalServerError, detail("Error interno del servidor."))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"temporada":     f.Temporada,
		"resumen":       resumen,
		"total":         total,
		"pagina":        pag.Pagina,
		"por_pagina":    pag.PorPagina,
		"total_paginas": totalPaginas,
		"resultados":    lista,
	})
}

// adminExcel: GET /admin/inscripciones/excel/ con los mismos filtros del listado.
func (a *App) adminExcel(w http.ResponseWriter, r *http.Request) {
	f, msg := a.parseFiltros(r)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, detail(msg))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	lista, err := a.buscar(ctx, f, 0, 0) // el Excel lleva todas las filas filtradas
	if err != nil {
		log.Printf("no se pudo exportar pre-inscripciones: %v", err)
		writeJSON(w, http.StatusInternalServerError, detail("Error interno del servidor."))
		return
	}

	xlsx, err := excelInscripciones(lista, f.Temporada)
	if err != nil {
		log.Printf("no se pudo generar el Excel: %v", err)
		writeJSON(w, http.StatusInternalServerError, detail("No se pudo generar el Excel."))
		return
	}
	defer xlsx.Close()

	nombre := fmt.Sprintf("preinscripciones-%d-%s.xlsx", f.Temporada, time.Now().In(chileTZ()).Format("20060102-1504"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+nombre+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := xlsx.Write(w); err != nil {
		log.Printf("error enviando el Excel: %v", err)
	}
}

// ---------- Excel ----------

// enChile pasa un instante a la hora de pared de Chile (Excel no guarda zona horaria).
func enChile(t time.Time) time.Time {
	c := t.In(chileTZ())
	return time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), c.Second(), 0, time.UTC)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func excelInscripciones(lista []preinscripcion, temporada int) (*excelize.File, error) {
	const hoja = "Inscripciones"
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", hoja); err != nil {
		f.Close()
		return nil, err
	}

	columnas := []struct {
		titulo string
		ancho  float64
	}{
		{"N°", 6}, {"Fecha inscripción", 18}, {"Nombres y Apellidos", 30}, {"Fecha de nacimiento", 14},
		{fmt.Sprintf("Edad al 31/12/%d", temporada), 11}, {"Condición de edad", 18}, {"Experiencia", 20},
		{"Último equipo", 34}, {"Rol o posición", 13}, {"Nivel de gimnasia", 18},
		{"Teléfono deportista", 16}, {"Correo deportista", 28},
		{"Apoderado", 28}, {"Teléfono apoderado", 16}, {"Correo apoderado", 28}, {"Estado", 12},
	}

	header := make([]any, len(columnas))
	for i, c := range columnas {
		header[i] = c.titulo
		col, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetColWidth(hoja, col, col, c.ancho); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := f.SetSheetRow(hoja, "A1", &header); err != nil {
		f.Close()
		return nil, err
	}

	ultimaCol, _ := excelize.ColumnNumberToName(len(columnas))
	estiloHeader, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"E3121B"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	if err != nil {
		f.Close()
		return nil, err
	}
	fmtFecha, fmtFechaHora := "dd-mm-yyyy", "dd-mm-yyyy hh:mm"
	estiloFecha, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtFecha})
	estiloFechaHora, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtFechaHora})
	_ = f.SetCellStyle(hoja, "A1", ultimaCol+"1", estiloHeader)
	_ = f.SetRowHeight(hoja, 1, 30)

	for i, p := range lista {
		fila := i + 2
		valores := []any{
			i + 1,
			enChile(p.CreatedAt),
			p.NombreCompleto,
			p.FechaNacimiento,
			p.EdadTemporada,
			opcionesCondicion[p.CondicionEdad],
			opcionesExperiencia[p.Experiencia],
			p.UltimoEquipo,
			opcionesRol[p.Rol],
			p.NivelGimnasia,
			deref(p.Telefono),
			deref(p.Email),
			deref(p.ApoderadoNombre),
			deref(p.ApoderadoTelefono),
			deref(p.ApoderadoEmail),
			p.Estado,
		}
		celda, _ := excelize.CoordinatesToCellName(1, fila)
		if err := f.SetSheetRow(hoja, celda, &valores); err != nil {
			f.Close()
			return nil, err
		}
		_ = f.SetCellStyle(hoja, fmt.Sprintf("B%d", fila), fmt.Sprintf("B%d", fila), estiloFechaHora)
		_ = f.SetCellStyle(hoja, fmt.Sprintf("D%d", fila), fmt.Sprintf("D%d", fila), estiloFecha)
	}

	ultimaFila := len(lista) + 1
	if err := f.AutoFilter(hoja, fmt.Sprintf("A1:%s%d", ultimaCol, ultimaFila), nil); err != nil {
		f.Close()
		return nil, err
	}
	_ = f.SetPanes(hoja, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return f, nil
}
