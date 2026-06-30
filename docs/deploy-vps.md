# Desplegar project-brain en un VPS

Guía paso a paso para correr el backend (API + Postgres + pgvector) en un
VPS Linux y conectar su servidor MCP a un agente Hermes local (o a
cualquier agente MCP).

El stack corre en Docker. Lo único que se compila fuera de Docker es el
pequeño binario MCP que el agente lanza como subproceso.

---

## 0. Requisitos previos

En el VPS (se asume Ubuntu/Debian):

- **Docker + plugin de Compose**
  ```bash
  docker --version
  docker compose version
  ```
  Si falta: `curl -fsSL https://get.docker.com | sh`
- Una **API key de Gemini** (https://aistudio.google.com/apikey). Requerida
  para la búsqueda semántica y la detección de colisiones.
- Tu **agente Hermes** corriendo en el mismo VPS (para el transporte MCP
  por stdio).

---

## 1. Obtener el código

Primera vez:
```bash
git clone https://github.com/frankirova/project-brain.git
cd project-brain
git checkout main
```

Actualizar un checkout existente: consultá `docs/updating-on-vps.md` para
el procedimiento completo.

---

## 2. Crear el archivo de secretos

La API lee los secretos desde un archivo `.env` (ignorado por git — nunca
lo commitees). Crealo en la raíz del repo:

```bash
cat > .env <<'EOF'
PROJECT_BRAIN_GEMINI_API_KEY=YOUR_GEMINI_KEY_HERE
PROJECT_BRAIN_AUTH_TOKEN=pick-a-long-random-token
EOF
```

- `PROJECT_BRAIN_GEMINI_API_KEY` — sin ella, el ingest sigue funcionando
  pero los endpoints de búsqueda / colisión quedan deshabilitados (y las
  herramientas MCP fallan).
- `PROJECT_BRAIN_AUTH_TOKEN` — protege todos los endpoints excepto
  `/v1/health`. Muy recomendado en un VPS público. Usá el **mismo** valor
  en la config MCP de Hermes (paso 5).

Generá un token rápido:
```bash
openssl rand -hex 32
```

---

## 3. Levantar el stack

```bash
docker compose up -d --build
```

Esto construye la imagen de la API, levanta Postgres (pgvector), aplica
automáticamente todas las migraciones en la **primera** ejecución
(volumen de datos vacío) y lanza la API en el puerto `8050`.

Esperá el health check:
```bash
docker compose ps
curl -s http://localhost:8050/v1/health
# {"status":"ok"}
```

Verificá en los logs que la búsqueda híbrida esté activa:
```bash
docker compose logs api | grep -i "hybrid search"
# ... "hybrid search + collision detection enabled" provider=gemini ...
```
Si en cambio ves `search enabled (fts only)`, la key de Gemini no se
cargó — revisá el `.env` y volvé a correr `docker compose up -d`.

---

## 4. Smoke test de la API

Reemplazá `$TOKEN` con tu `PROJECT_BRAIN_AUTH_TOKEN`.

```bash
TOKEN=pick-a-long-random-token
WS=default

# 1) Guardar una decisión
curl -s -X POST http://localhost:8050/v1/ingest-text \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"type\":\"decision\",\"content\":\"El equipo usa Go para el backend\"}"

# 2) Buscar por significado
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8050/v1/search?workspace_id=$WS&q=lenguaje%20del%20servidor"

# 3) Chequeo de colisión (el plato fuerte)
curl -s -X POST http://localhost:8050/v1/check-collision \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"content\":\"Propongo usar Python en vez de Go\"}"
```

El paso 3 debería devolver la decisión sobre Go como colisión.

---

## 5. Compilar el binario MCP y conectar Hermes

El servidor MCP es un binario chico que el agente lanza por stdio.
Reenvía las llamadas a herramientas a la API en `localhost:8050`, así que
solo necesita la URL de la API y el token — nunca las credenciales de la
base de datos.

**Compilarlo (no necesitás instalar Go — usá el toolchain de Docker):**
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.25-alpine \
  go build -o bin/project-brain-mcp ./cmd/mcp
```
Esto produce `bin/project-brain-mcp` (un binario de Linux). Si ya tenés
Go 1.25 en el VPS, `go build -o bin/project-brain-mcp ./cmd/mcp` también
funciona.

**Registralo en tu config MCP de Hermes** (forma estándar de servidor
MCP — adaptá los nombres de las claves si Hermes difiere):
```json
{
  "mcpServers": {
    "project-brain": {
      "command": "/absolute/path/to/project-brain/bin/project-brain-mcp",
      "env": {
        "PROJECT_BRAIN_API_URL": "http://localhost:8050",
        "PROJECT_BRAIN_AUTH_TOKEN": "pick-a-long-random-token",
        "PROJECT_BRAIN_MCP_WORKSPACE": "default"
      }
    }
  }
}
```

Reiniciá Hermes. Ahora debería exponer cuatro herramientas:
`search_knowledge`, `check_collision`, `save_knowledge`,
`get_sdd_document`.

---

## 6. Hardening (VPS público)

- **Auth token**: configurá `PROJECT_BRAIN_AUTH_TOKEN` (paso 2). Listo =
  todos los endpoints excepto `/v1/health` requieren el token Bearer.
- **TLS / HSTS**: si la API está detrás de un reverse proxy que termina
  TLS (Nginx, Caddy, Cloudflare Tunnel, etc.), configurá
  `PROJECT_BRAIN_TLS=1` en `.env`. La API entonces emite
  `Strict-Transport-Security: max-age=63072000; includeSubDomains` en
  cada respuesta. Apagado por defecto para que las instancias de dev /
  test corriendo en HTTP plano no publiciten upgrades a HTTPS.
- **Security headers**: activados por defecto
  (`PROJECT_BRAIN_SECURITY_HEADERS=true`). 6 headers base de OWASP 2025
  (X-Content-Type-Options, X-Frame-Options, Referrer-Policy,
  Permissions-Policy, Cross-Origin-Resource-Policy,
  Cache-Control: no-store) se agregan a cada respuesta, incluyendo
  `/v1/health`, `/v1/liveness` y `/v1/readiness` (que usa tu kubelet /
  load balancer — estos endpoints también están bindeados a localhost en
  el siguiente bullet).
- **No expongas Postgres / API públicamente.** Bindeá la API a localhost
  editando `docker-compose.yml`:
  ```yaml
  api:
    ports:
      - "127.0.0.1:8050:8050"   # antes era "8050:8050"
  ```
  Misma idea para el puerto de `postgres` si no lo necesitás desde
  afuera. Después `docker compose up -d`.
- O poné un firewall (ufw) delante y permití solo lo que necesitás.

---

## 7. Actualizar más adelante

Consultá `docs/updating-on-vps.md` para el procedimiento completo
(3 rebuilds, gotcha de migraciones, verificación, rollback).

---

## Solución de problemas

| Síntoma | Causa / solución |
|---------|------------------|
| `/v1/search` o `/v1/check-collision` → 404 | No se cargó la key de Gemini → revisá el `.env`, reiniciá. |
| Cualquier endpoint protegido → 401 | Falta o es incorrecto el `Authorization: Bearer <token>`. |
| Los logs dicen `running with in-memory uow` | `PROJECT_BRAIN_DATABASE_DSN` no está seteada — compose la setea; probablemente corriste la API fuera de compose. |
| `relation "embeddings" does not exist` | Volumen de datos viejo de antes de la migración. `docker compose down -v` para borrarlo (destruye los datos) o aplicá la migración a mano. |
| Las herramientas MCP tiran "connection refused" | La API no está corriendo, o `PROJECT_BRAIN_API_URL` está mal en la config de Hermes. |
| El veredicto de colisión parece raro | Los umbrales están calibrados para Gemini en español (~0.6 no relacionado, ~0.78 colisión, ~0.90 duplicado). |
