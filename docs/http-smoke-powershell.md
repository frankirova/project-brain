# Project Brain — Manual HTTP smoke tests (PowerShell)

Ready-to-paste requests for testing the running API from PowerShell using
`Invoke-RestMethod` (the PowerShell-native equivalent of `curl`).

- **Base URL:** `http://localhost:8050`
- **Auth:** none required while `PROJECT_BRAIN_AUTH_TOKEN` is unset. If you set
  it, add `-Headers @{ Authorization = "Bearer $env:PROJECT_BRAIN_AUTH_TOKEN" }`
  to every protected request (everything except `/v1/health`).
- **Workspace:** examples use `demo-gemini`.

> The `search`, `objects` and `check-collision` endpoints only exist when the
> API runs against Postgres **with** a Gemini key. Start it with both env vars
> set, otherwise you'll get `404`:
>
> ```powershell
> $env:PROJECT_BRAIN_DATABASE_DSN   = "postgres://postgres:postgres@localhost:5433/project_brain?sslmode=disable"
> $env:PROJECT_BRAIN_GEMINI_API_KEY = "<your-key>"   # never commit this
> go run ./cmd/api
> ```

---

## Setup — base URL variable

Run this once per PowerShell session so the rest of the snippets are short:

```powershell
$base = "http://localhost:8050"
```

---

## 1. Health — `GET /v1/health`

Public, no auth. Quick "is it up?" check.

```powershell
Invoke-RestMethod -Uri "$base/v1/health"
```

---

## 2. Ingest — `POST /v1/ingest-text`

Stores a knowledge object and embeds it automatically (post-ingest hook).

```powershell
$body = @{
    workspace_id = "demo-gemini"
    type         = "decision"
    content      = "Vamos a usar Redis para cachear sesiones"
} | ConvertTo-Json

Invoke-RestMethod -Method Post -Uri "$base/v1/ingest-text" `
    -ContentType "application/json" -Body $body
```

---

## 3. Semantic search — `GET /v1/search`

Hybrid FTS + vector search. Finds by **meaning**, not keyword overlap.

```powershell
$q = [uri]::EscapeDataString("donde guardamos datos rapido")
Invoke-RestMethod -Uri "$base/v1/search?workspace_id=demo-gemini&q=$q"
```

Optional `limit` (default 10, max 100):

```powershell
$q = [uri]::EscapeDataString("cobramos plata")
Invoke-RestMethod -Uri "$base/v1/search?workspace_id=demo-gemini&q=$q&limit=5"
```

---

## 4. Collision detection — `POST /v1/check-collision`

The headline feature: given candidate text, return existing knowledge it
semantically collides with, **without storing anything**.

```powershell
$body = @{
    workspace_id = "demo-gemini"
    content      = "Propongo que usemos Python en vez de Go para el backend"
} | ConvertTo-Json

Invoke-RestMethod -Method Post -Uri "$base/v1/check-collision" `
    -ContentType "application/json" -Body $body | ConvertTo-Json -Depth 6
```

**Verdict bands** (calibrated to real Gemini cosine similarities on Spanish):

| similarity      | verdict          | meaning                          |
|-----------------|------------------|----------------------------------|
| `>= 0.90`       | `duplicate`      | near-identical restatement       |
| `0.78 – 0.90`   | `strong_overlap` | same topic / direct contradiction|
| `0.75 – 0.78`   | `related`        | adjacent, worth a look           |
| `< 0.75`        | (filtered)       | unrelated noise, not returned    |

> If a clear collision comes back as `related`, the running process still has
> the **old** compiled bands — restart with `go run ./cmd/api`.

---

## 5. Object by ID — `GET /v1/objects/{id}`

Grab an `ID` from a search or collision response and fetch the full object.
`workspace_id` is required (multi-tenant scope).

```powershell
$id  = "4b4250a8-f59f-439d-b774-74b1f2cbbeff"   # replace with a real ID
$uri = "$base/v1/objects/$id" + "?workspace_id=demo-gemini"
Invoke-RestMethod -Uri $uri
```

> **PowerShell gotcha:** don't write `"$base/v1/objects/$id?workspace_id=..."`
> inline — PowerShell mis-parses the boundary after `$id` and drops the query
> string, so the server sees no `workspace_id`. Build the URI by concatenation
> (above) or delimit the variable with braces: `"...objects/${id}?workspace_id=demo-gemini"`.

---

## Demo combo — semantic magic in two requests

Ingest a fact, then find it with words that share **nothing** with the original:

```powershell
# 1) Ingest
$body = @{
    workspace_id = "demo-gemini"
    type         = "decision"
    content      = "Vamos a usar Redis para cachear sesiones"
} | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri "$base/v1/ingest-text" -ContentType "application/json" -Body $body

# 2) Search with unrelated wording -> still finds the Redis decision
$q = [uri]::EscapeDataString("donde guardamos datos rapido")
Invoke-RestMethod -Uri "$base/v1/search?workspace_id=demo-gemini&q=$q"
```

---

## Troubleshooting

| Symptom                              | Likely cause                                              |
|--------------------------------------|----------------------------------------------------------|
| `404` on search/objects/collision    | API started without Postgres + Gemini key (in-memory UOW)|
| `401`                                 | `PROJECT_BRAIN_AUTH_TOKEN` set — add the Bearer header   |
| collision returns `related` not `strong_overlap` | running binary has old verdict bands — restart |
| connection refused / no response     | API not running, or wrong port (default `8050`)          |
