-- Evita pre-inscripciones duplicadas sin cambiar el formulario ni los datos.
-- Solo crea índices (no agrega columnas ni modifica/borra filas).
--
-- Clave del deportista: primera palabra de nombre_completo (primer nombre),
-- en minúsculas y sin tildes, dentro de la misma temporada, más:
--   a) el email de contacto (del deportista si es mayor, del apoderado si es menor), o
--   b) la fecha de nacimiento (misma persona aunque use otro correo).
--
-- Si la tabla ya tuviera filas que choquen con un índice, ese índice NO se crea
-- (queda un WARNING en el log) para no impedir que la API arranque. Aun así,
-- crearPreinscripcion revisa estos mismos duplicados antes de insertar.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM inscripciones_preinscripcion
        GROUP BY temporada,
                 lower(coalesce(email, apoderado_email)),
                 translate(lower(split_part(nombre_completo, ' ', 1)), 'áéíóúüñ', 'aeiouun')
        HAVING count(*) > 1
    ) THEN
        CREATE UNIQUE INDEX IF NOT EXISTS inscripciones_preinscripcion_unica_email
            ON inscripciones_preinscripcion (
                temporada,
                lower(coalesce(email, apoderado_email)),
                translate(lower(split_part(nombre_completo, ' ', 1)), 'áéíóúüñ', 'aeiouun')
            );
    ELSE
        RAISE WARNING 'Hay pre-inscripciones duplicadas por email + primer nombre; no se creó inscripciones_preinscripcion_unica_email';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM inscripciones_preinscripcion
        GROUP BY temporada,
                 fecha_nacimiento,
                 translate(lower(split_part(nombre_completo, ' ', 1)), 'áéíóúüñ', 'aeiouun')
        HAVING count(*) > 1
    ) THEN
        CREATE UNIQUE INDEX IF NOT EXISTS inscripciones_preinscripcion_unica_fecha
            ON inscripciones_preinscripcion (
                temporada,
                fecha_nacimiento,
                translate(lower(split_part(nombre_completo, ' ', 1)), 'áéíóúüñ', 'aeiouun')
            );
    ELSE
        RAISE WARNING 'Hay pre-inscripciones duplicadas por fecha de nacimiento + primer nombre; no se creó inscripciones_preinscripcion_unica_fecha';
    END IF;
END $$;
