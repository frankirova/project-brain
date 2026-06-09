# Project Brain — Plataforma de conocimiento colaborativo

Project Brain es una plataforma de conocimiento personal y colaborativa pensada como un **Segundo Cerebro técnico** para capturar, estructurar, relacionar y reutilizar conocimiento proveniente de conversaciones, documentos, investigaciones, repositorios, links, PDFs, videos y decisiones.

La decisión central es clara: **Project Brain no debe construirse como un chatbot con memoria**, sino como una plataforma de conocimiento event-driven, multiagente, con RAG híbrido, grafo de conocimiento y almacenamiento auditable.

## 1. Visión del producto

Project Brain transforma información dispersa en conocimiento reutilizable.

Cada interacción puede convertirse en:

- una investigación
- una decisión
- una idea
- una tarea
- un documento
- un resumen
- un artefacto técnico
- una relación con conocimiento previo

El objetivo no es responder preguntas de forma aislada. El objetivo es que **cada conversación aumente el valor acumulado del sistema**.

## 2. Problema

El conocimiento hoy queda disperso entre:

- Telegram
- ChatGPT
- documentos
- Markdown
- repositorios
- links
- archivos PDF
- videos
- notas sueltas

El problema real no es solo la búsqueda. El problema es que el conocimiento:

- no está normalizado
- no tiene relaciones explícitas
- no tiene trazabilidad
- no tiene versionado
- no se convierte en decisiones reutilizables
- no se conecta automáticamente con proyectos, ideas o investigaciones anteriores

Project Brain debe resolver eso convirtiendo información informal en memoria estructurada.

## 3. Principios de diseño

Toda información debe poder ser:

- clasificada
- etiquetada
- resumida
- relacionada
- indexada
- auditada
- consultada
- reutilizada

Principios clave:

- Telegram es solo una interfaz.
- La lógica vive en la plataforma.
- No depender únicamente de embeddings.
- No guardar solo texto plano.
- No limitarse a una conversación.
- Modelar conocimiento como entidades y relaciones.
- Registrar decisiones con contexto, alternativas y consecuencias.

## 4. Casos de uso

### 4.1 Captura diaria

Ejemplos:

- “Guardá esta idea para el CRM.”
- “Esto puede servir para monitoreo.”
- “Compará Redis vs Valkey.”
- “Este link es importante.”
- “Resumí este PDF.”
- “Registrá que descartamos Prometheus por este motivo.”

Resultado esperado:

- clasificación automática
- resumen
- tags
- relaciones
- persistencia
- indexación
- disponibilidad futura

### 4.2 Investigación

Comando:

```text
/research Valkey vs Redis para cache distribuido
```

Debe producir:

- resumen ejecutivo
- análisis técnico
- ventajas
- desventajas
- comparación
- riesgos
- costos
- recomendaciones
- fuentes
- conclusiones

Todo debe almacenarse automáticamente como conocimiento reutilizable.

### 4.3 Arquitectura

Comando:

```text
/architect diseñar arquitectura para CRM multi-tenant
```

Debe poder generar:

- SDD
- ERD
- roadmap
- arquitectura
- diagramas
- APIs
- modelo de dominio
- riesgos
- trade-offs
- decisiones técnicas

### 4.4 Código

Comando:

```text
/coder analizá este repositorio y proponé refactors
```

Debe poder:

- analizar repositorios
- explicar código
- proponer refactors
- crear documentación
- modificar código bajo workflow controlado
- generar issues técnicos

### 4.5 Founder mode

Comando:

```text
/founder analizá si esta idea SaaS tiene mercado
```

Debe poder generar:

- business model
- pricing
- benchmark
- análisis de competencia
- roadmap
- MVP
- TAM/SAM/SOM
- landing draft
- pitch

## 5. Benchmarking de arquitecturas posibles

Antes de elegir una solución, conviene comparar alternativas.

### Arquitectura A — Chatbot + base vectorial simple

Flujo:

```text
Telegram → LLM → embeddings → vector DB → respuesta
```

Ventajas:

- rápida de construir
- buena demo inicial
- baja complejidad
- útil para preguntas simples

Desventajas:

- no modela conocimiento real
- difícil auditar decisiones
- relaciones pobres
- depende demasiado de embeddings
- no escala como memoria institucional

Veredicto: **no recomendada como arquitectura principal**.

### Arquitectura B — Notion/Markdown-first con IA encima

Todo se guarda como documentos Markdown o páginas tipo Notion, y la IA resume y busca sobre esos documentos.

Ventajas:

- legible por humanos
- fácil de exportar
- buena colaboración inicial
- baja barrera de entrada

Desventajas:

- estructura débil
- relaciones difíciles de mantener
- consultas complejas limitadas
- versionado semántico pobre
- automatización limitada

Veredicto: útil como capa de documentación/exportación, pero no como núcleo.

### Arquitectura C — Graph DB-first

Ejemplos:

- Neo4j
- Memgraph
- Amazon Neptune

Ventajas:

- excelente para relaciones
- consultas navegacionales potentes
- buena representación de conocimiento
- útil para explicar conexiones

Desventajas:

- mayor complejidad operativa
- peor encaje para datos transaccionales
- búsqueda semántica requiere integración adicional
- más difícil para MVP
- duplicación probable con PostgreSQL

Veredicto: interesante para fases futuras, pero no como primera base principal.

### Arquitectura D — PostgreSQL-first + pgvector + grafo relacional

PostgreSQL almacena entidades, relaciones, auditoría, documentos, metadata, embeddings y búsqueda full-text.

Ventajas:

- una sola base principal
- transaccional
- auditable
- buena para MVP y producción
- pgvector integrado
- full-text search nativo
- JSONB para metadata flexible
- relaciones modelables
- menor complejidad operativa

Desventajas:

- consultas de grafo profundas pueden ser complejas
- requiere buen diseño de índices
- no es tan expresivo como Neo4j para graph traversal
- hay que cuidar performance de pgvector a escala

Veredicto: **arquitectura base recomendada**.

### Arquitectura E — Event-driven knowledge platform

Cada mensaje o documento genera eventos procesados por workers especializados.

Flujo:

```text
Telegram Message Received
↓
Knowledge Ingestion Event
↓
Classification Worker
↓
Entity Extraction Worker
↓
Embedding Worker
↓
Relationship Worker
↓
Persistence
↓
Search Index Update
```

Ventajas:

- escalable
- extensible
- permite reprocesamiento
- desacopla agentes
- ideal para aprendizaje continuo
- soporta pipelines largos

Desventajas:

- más compleja que CRUD tradicional
- requiere observabilidad
- riesgo de sobreingeniería en MVP
- necesita idempotencia fuerte

Veredicto: recomendada, pero implementada incrementalmente.

### Arquitectura F — Multi-store avanzada

Componentes posibles:

- PostgreSQL para core
- pgvector o Qdrant para vectores
- Neo4j para grafo
- OpenSearch para texto
- S3 para documentos
- Redis/Valkey para cache
- NATS/Kafka para eventos

Ventajas:

- especializada
- potente
- escalable

Desventajas:

- mucha complejidad operativa
- demasiados servicios para el inicio
- alto costo de mantenimiento
- difícil para dos personas en etapa temprana

Veredicto: arquitectura futura, no MVP.

## 6. Arquitectura recomendada

La recomendación es una arquitectura híbrida incremental:

```text
Telegram / API / CLI / Web
        ↓
Interface Gateway
        ↓
Command Router
        ↓
Orchestrator Agent
        ↓
Knowledge Pipeline
        ↓
PostgreSQL Knowledge Core
        ↓
Hybrid Retrieval Engine
        ↓
Agents / Documents / Tasks / Decisions
```

Stack recomendado inicial:

| Componente | Tecnología recomendada |
|---|---|
| Backend | Go |
| Base principal | PostgreSQL |
| Vector search | pgvector |
| Full-text search | PostgreSQL FTS |
| Cache | Valkey o Redis |
| Object storage | S3 compatible |
| Mensajería | NATS |
| Workers | Go |
| Interfaz inicial | Telegram Bot API |
| Observabilidad | OpenTelemetry + Prometheus/Grafana |
| Auth futura | OIDC / OAuth2 |

## 7. Arquitectura general

```text
┌─────────────────────────────┐
│ Interfaces                  │
│ Telegram / Web / CLI / API  │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Interface Gateway           │
│ Auth, rate limit, sessions  │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Command Router              │
│ /research /architect /coder │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Orchestrator Agent          │
│ Plans, delegates, composes  │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Agent Layer                 │
│ Research / Architect / KM   │
│ Coder / Founder / Docs      │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Knowledge Processing        │
│ classify, extract, relate   │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Knowledge Core              │
│ PostgreSQL + pgvector + FTS │
└──────────────┬──────────────┘
               ↓
┌─────────────────────────────┐
│ Retrieval Engine            │
│ semantic + keyword + graph  │
└─────────────────────────────┘
```

## 8. Arquitectura multiagente

### 8.1 Orchestrator Agent

Responsabilidades:

- entender intención
- elegir modo
- crear plan
- delegar a agentes
- controlar calidad
- consolidar resultados
- decidir qué persistir
- evitar respuestas superficiales

El Orchestrator coordina. No debería hacer todo.

### 8.2 Knowledge Processor Agent

Responsabilidades:

- clasificar mensajes
- extraer entidades
- detectar decisiones
- detectar tareas
- detectar ideas
- crear relaciones
- generar metadata
- decidir persistencia
- versionar conocimiento

Este es uno de los agentes más importantes: convierte conversación en memoria.

### 8.3 Research Agent

Responsabilidades:

- investigar temas
- comparar alternativas
- buscar fuentes
- sintetizar
- detectar riesgos
- generar recomendaciones
- vincular con conocimiento existente

Outputs principales:

- `Research`
- `Benchmark`
- `Source`
- `DecisionCandidate`
- `Artifact`

### 8.4 Architect Agent

Responsabilidades:

- generar arquitectura
- crear SDD
- crear ERD
- definir APIs
- modelar dominio
- evaluar trade-offs
- registrar decisiones técnicas
- detectar riesgos

Outputs principales:

- `Architecture`
- `Decision`
- `Requirement`
- `Roadmap`
- `Artifact`

### 8.5 Coder Agent

Responsabilidades:

- analizar repositorios
- explicar código
- proponer refactors
- generar documentación
- crear patches bajo control
- vincular hallazgos técnicos con Project Brain

Outputs principales:

- `CodeAnalysis`
- `Issue`
- `Task`
- `Snippet`
- `Artifact`

### 8.6 Founder Agent

Responsabilidades:

- analizar negocio
- pricing
- competencia
- mercado
- propuesta de valor
- MVP
- go-to-market
- pitch

Outputs principales:

- `BusinessModel`
- `Benchmark`
- `Roadmap`
- `Idea`
- `Artifact`

### 8.7 Documentation Agent

Responsabilidades:

- transformar conocimiento en documentos
- generar READMEs
- generar ADRs
- generar SDDs
- crear specs
- producir reportes

### 8.8 Retrieval Agent

Responsabilidades:

- decidir cómo buscar
- combinar búsqueda semántica, keyword, estructurada y grafo
- explicar de dónde sale una respuesta
- citar fuentes internas

## 9. Modelo de dominio

Entidades principales:

```text
Workspace
Project
Conversation
Message
Source
Document
Chunk
Entity
Relation
Research
Decision
Idea
Task
Requirement
Architecture
Artifact
Prompt
Snippet
Benchmark
Roadmap
Issue
Meeting
Tag
Embedding
Memory
AuditEvent
```

### 9.1 Workspace

Agrupa usuarios y conocimiento compartido.

Campos conceptuales:

- id
- name
- owner_id
- created_at

### 9.2 Project

Ejemplos:

- CRM
- ERP
- Monitoring
- AI
- Infra
- SaaS Ideas

Campos conceptuales:

- id
- workspace_id
- name
- description
- status
- metadata

### 9.3 Source

Representa el origen del conocimiento.

Ejemplos:

- Telegram message
- PDF
- YouTube video
- GitHub repo
- Markdown file
- URL
- ChatGPT export

### 9.4 Knowledge Object

Abstracción base para objetos como:

- Research
- Decision
- Idea
- Task
- Document
- Architecture
- Artifact

Campos comunes:

- id
- workspace_id
- project_id nullable
- type
- title
- summary
- content
- status
- confidence
- importance
- created_by
- created_at
- updated_at
- metadata

## 10. Modelo de datos físico

### 10.1 `knowledge_objects`

```sql
CREATE TABLE knowledge_objects (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    project_id UUID NULL,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT,
    content TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    confidence NUMERIC(4,3),
    importance INT DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_by UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ NULL
);
```

Tipos posibles:

```text
research
decision
idea
task
meeting
source
document
architecture
prompt
snippet
benchmark
roadmap
issue
requirement
artifact
```

### 10.2 `relations`

```sql
CREATE TABLE relations (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    source_object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    target_object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    relation_type TEXT NOT NULL,
    confidence NUMERIC(4,3),
    evidence TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}'
);
```

Tipos de relación:

```text
relates_to
depends_on
contradicts
supersedes
supports
derived_from
mentions
decides
implements
compares_with
replaces
blocks
references
part_of
```

### 10.3 `sources`

```sql
CREATE TABLE sources (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    type TEXT NOT NULL,
    uri TEXT,
    title TEXT,
    checksum TEXT,
    raw_content_location TEXT,
    metadata JSONB NOT NULL DEFAULT '{}',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 10.4 `object_sources`

```sql
CREATE TABLE object_sources (
    object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    source_id UUID NOT NULL REFERENCES sources(id),
    relevance NUMERIC(4,3),
    PRIMARY KEY (object_id, source_id)
);
```

### 10.5 `chunks`

```sql
CREATE TABLE chunks (
    id UUID PRIMARY KEY,
    source_id UUID NOT NULL REFERENCES sources(id),
    object_id UUID NULL REFERENCES knowledge_objects(id),
    chunk_index INT NOT NULL,
    content TEXT NOT NULL,
    token_count INT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 10.6 `embeddings`

```sql
CREATE TABLE embeddings (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    target_type TEXT NOT NULL,
    target_id UUID NOT NULL,
    model TEXT NOT NULL,
    dimensions INT NOT NULL,
    embedding vector(1536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 10.7 `audit_events`

```sql
CREATE TABLE audit_events (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    actor_id UUID NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id UUID NOT NULL,
    before JSONB,
    after JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 10.8 `object_versions`

```sql
CREATE TABLE object_versions (
    id UUID PRIMARY KEY,
    object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    version INT NOT NULL,
    title TEXT,
    summary TEXT,
    content TEXT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_by UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## 11. Estrategia de indexación

### Full-text search

```sql
CREATE INDEX idx_knowledge_fts
ON knowledge_objects
USING GIN (
    to_tsvector(
        'spanish',
        coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(content,'')
    )
);
```

Para contenido mixto español/inglés, evaluar configuración `simple`, columnas por idioma o generación de `tsvector` normalizado.

### Metadata

```sql
CREATE INDEX idx_knowledge_metadata
ON knowledge_objects
USING GIN (metadata);
```

### Relaciones

```sql
CREATE INDEX idx_relations_source
ON relations(source_object_id);

CREATE INDEX idx_relations_target
ON relations(target_object_id);

CREATE INDEX idx_relations_type
ON relations(relation_type);
```

### Vector search

```sql
CREATE INDEX idx_embeddings_vector
ON embeddings
USING ivfflat (embedding vector_cosine_ops);
```

Para producción avanzada, evaluar HNSW si el volumen crece.

## 12. Estrategia de memoria

### 12.1 Memoria inmediata

Contexto de la conversación actual.

Uso:

- continuidad conversacional
- resolución de referencias
- “eso”, “lo anterior”, “comparalo con esto”

Almacenamiento recomendado:

- Redis/Valkey para sesión activa
- persistencia posterior en PostgreSQL como conversación y mensajes

### 12.2 Memoria de proyecto

Todo lo relacionado con un proyecto específico.

Ejemplos:

- CRM
- Monitoring
- AI
- Infra

Incluye:

- decisiones
- investigaciones
- issues
- documentos
- requisitos
- benchmarks
- arquitectura
- tareas

### 12.3 Memoria global

Conocimiento transversal reutilizable entre proyectos.

Ejemplos:

- Go
- PostgreSQL
- Kubernetes
- RAG
- Prometheus
- VictoriaMetrics
- SaaS pricing

### 12.4 Memoria permanente

Decisiones importantes.

Ejemplo:

```text
Decision: Se eligió Go como backend principal.
Motivo: performance, simplicidad operativa, experiencia del equipo.
Alternativas: Node.js, Python, Rust.
Consecuencias: menor velocidad para prototipos IA, mayor robustez backend.
Fecha: 2026-06-09.
Estado: active.
```

Las decisiones deben registrar:

- fecha
- contexto
- motivo
- alternativas
- consecuencias
- fuentes
- relaciones
- estado
- versión

## 13. Pipeline de conocimiento

Pipeline completo:

```text
Input
↓
Normalize
↓
Classify
↓
Extract entities
↓
Extract claims
↓
Detect object type
↓
Summarize
↓
Generate tags
↓
Detect project/global scope
↓
Find related memories
↓
Create relations
↓
Generate embeddings
↓
Persist
↓
Index
↓
Audit
```

Entradas soportadas:

- Telegram
- API
- Web app
- CLI
- Markdown
- PDF
- repositorios
- URLs
- YouTube transcripts
- ChatGPT exports

## 14. Pipeline RAG híbrido

No alcanza con embeddings. La búsqueda debe combinar varios mecanismos.

### 14.1 Búsqueda semántica

Útil cuando el usuario pregunta conceptos similares aunque no use las mismas palabras.

Ejemplo:

```text
¿Qué investigamos sobre alternativas a Redis?
```

Puede traer Valkey aunque Redis no aparezca literalmente en el texto.

### 14.2 Búsqueda keyword / full-text

Útil cuando importa la palabra exacta.

Ejemplo:

```text
Mostrame todo donde aparezca Docker.
```

### 14.3 Búsqueda estructurada

Útil cuando hay filtros claros.

Ejemplo:

```text
¿Qué decisiones tomamos para el CRM?
```

Consulta conceptual:

```text
type = decision
project = CRM
```

### 14.4 Búsqueda por relaciones

Útil cuando importa el camino de conocimiento.

Ejemplo:

```text
¿Cuándo descartamos Prometheus?
```

Debe buscar decisiones donde Prometheus aparezca como alternativa descartada, relación reemplazada o tecnología comparada.

### 14.5 Flujo de retrieval

```text
User question
↓
Query understanding
↓
Intent detection
↓
Search plan
↓
Structured filters
↓
FTS search
↓
Vector search
↓
Graph expansion
↓
Reranking
↓
Context assembly
↓
Answer with citations
↓
Optional new memory
```

### 14.6 Reranking

El reranker debe considerar:

- similitud semántica
- coincidencia exacta
- relación con proyecto
- importancia
- fecha
- autoridad de fuente
- tipo de objeto
- decisiones permanentes
- confianza

## 15. Modos principales

### 15.1 `/research`

Output obligatorio:

```markdown
# Research: <tema>

## Resumen ejecutivo
## Contexto
## Análisis técnico
## Alternativas
## Comparación
## Ventajas
## Desventajas
## Riesgos
## Costos
## Recomendación
## Fuentes
## Conclusión
## Objetos relacionados
```

Persistencia:

- `Research`
- `Source`
- `Benchmark`, si aplica
- `DecisionCandidate`, si aplica
- relaciones con proyectos y tecnologías

### 15.2 `/architect`

Debe producir:

- SDD
- ERD
- arquitectura general
- modelo de dominio
- APIs
- riesgos
- trade-offs
- roadmap
- tareas
- decisiones sugeridas

Persistencia:

- `Architecture`
- `Requirement`
- `Decision`
- `Roadmap`
- `Task`
- `Artifact`

### 15.3 `/coder`

Debe producir:

- análisis técnico
- diff propuesto, si corresponde
- riesgos
- tests sugeridos
- documentación
- relaciones con decisiones técnicas

### 15.4 `/founder`

Debe producir:

- idea refinada
- problema
- ICP
- competidores
- pricing
- benchmark
- MVP
- roadmap
- riesgos
- go-to-market
- pitch
- landing draft

## 16. Roadmap MVP

Objetivo del MVP: capturar conocimiento desde Telegram y consultarlo con RAG híbrido básico.

### Fase 1 — Foundation

- backend Go
- PostgreSQL
- migraciones
- modelo `knowledge_objects`
- modelo `relations`
- modelo `sources`
- Telegram bot básico
- ingestion API
- object storage S3 compatible

### Fase 2 — Knowledge Pipeline básico

- clasificación de mensajes
- resumen
- tags
- detección de proyecto
- persistencia
- FTS
- embeddings con pgvector

### Fase 3 — Retrieval básico

- búsqueda semántica
- búsqueda keyword
- filtros por proyecto/tipo
- respuestas con fuentes internas

### Fase 4 — Modos iniciales

- `/save`
- `/research`
- `/ask`
- `/decision`
- `/project`

### Fase 5 — Memoria permanente

- registrar decisiones
- registrar alternativas
- registrar consecuencias
- crear relaciones
- auditar cambios

## 17. Roadmap versión 1.0

- web dashboard
- graph explorer
- editor de objetos
- revisión humana de conocimiento extraído
- importadores Markdown/PDF/URL
- procesamiento de videos/transcripts
- workspace multiusuario
- permisos
- API pública
- sistema de tareas
- conectores GitHub
- análisis de repositorios
- agentes especializados
- observabilidad completa
- workflows de aprobación
- exportación Markdown/JSON
- versionado semántico

## 18. Tecnologías recomendadas y justificación

### Go

Justificación:

- robusto
- eficiente
- simple de desplegar
- excelente para workers
- buen soporte de concurrencia
- ideal para servicios de larga vida

Trade-off:

- menos flexible que Python para prototipos IA
- algunas librerías LLM están más maduras en Python

Recomendación: Go para la plataforma; Python opcional para workers especializados si hiciera falta.

### PostgreSQL

Justificación:

- transaccional
- confiable
- soporta JSONB
- soporta FTS
- soporta pgvector
- excelente para auditoría
- reduce complejidad inicial

### pgvector

Justificación:

- suficiente para MVP y producción inicial
- evita otra base
- mantiene embeddings cerca de los datos
- simple de operar

Cuándo evaluar alternativa:

- millones de chunks
- latencia vectorial crítica
- necesidad avanzada de HNSW/quantization

Alternativas futuras:

- Qdrant
- Weaviate
- Milvus

### Valkey o Redis

Uso:

- sesión inmediata
- cache de retrieval
- rate limits
- locks livianos
- colas simples si NATS aún no existe

Recomendación:

- Valkey si se quiere evitar dependencia del ecosistema Redis comercial.
- Redis si ya forma parte del stack operativo.

### NATS

Justificación:

- simple
- liviano
- bueno para eventos internos
- menos pesado que Kafka
- ideal para equipos chicos

Kafka sería excesivo para el MVP.

### S3 compatible

Uso:

- PDFs
- documentos originales
- archivos adjuntos
- snapshots
- exports
- raw sources

Opciones:

- MinIO
- Cloudflare R2
- AWS S3
- Backblaze B2

## 19. Riesgos técnicos

### 19.1 Basura semántica

Si el sistema guarda todo sin criterio, la memoria se contamina.

Mitigación:

- confidence score
- importance score
- revisión humana
- deduplicación
- objetos descartables
- archivado

### 19.2 Relaciones incorrectas

Los LLM pueden inventar conexiones.

Mitigación:

- guardar evidencia
- confidence por relación
- revisión manual
- relaciones reversibles
- auditoría

### 19.3 Dependencia excesiva de embeddings

Embeddings no entienden estado, decisiones ni intención histórica.

Mitigación:

- RAG híbrido
- búsqueda estructurada
- grafo
- FTS
- metadata

### 19.4 Costos de LLM

Procesar todo puede ser caro.

Mitigación:

- clasificación barata
- batching
- modelos pequeños para extracción
- modelos grandes solo para síntesis
- cache
- procesamiento async

### 19.5 Complejidad multiagente

Muchos agentes sin contratos claros producen caos.

Mitigación:

- outputs estructurados
- orchestrator fuerte
- auditoría
- trazabilidad
- responsabilidades explícitas

## 20. Riesgos de producto

### 20.1 Convertirse en otro lugar donde tirar notas

Mitigación:

- workflows concretos
- decisiones explícitas
- resúmenes automáticos
- relaciones visibles
- revisiones semanales

### 20.2 Falta de confianza

Si responde sin citar fuentes, el usuario no confía.

Mitigación:

- respuestas con referencias internas
- mostrar objetos relacionados
- mostrar fecha y origen
- distinguir dato, inferencia y recomendación

### 20.3 Fricción de uso

Si guardar conocimiento requiere esfuerzo, no se usa.

Mitigación:

- Telegram primero
- comandos simples
- auto-clasificación
- confirmaciones livianas
- digest diario/semanal

## 21. Decisiones clave

### Decisión 1 — PostgreSQL como núcleo

Motivo:

- reduce complejidad
- soporta datos estructurados, texto, JSONB, FTS y vectores
- permite auditoría fuerte

Alternativas:

- Neo4j-first
- vector DB-first
- document DB

Consecuencia:

- graph traversal avanzado queda limitado inicialmente, pero el MVP es más sólido.

### Decisión 2 — RAG híbrido, no solo embeddings

Motivo:

- las preguntas reales combinan significado, palabras exactas, filtros y relaciones.

Consecuencia:

- más complejidad de retrieval, pero respuestas mucho mejores.

### Decisión 3 — Telegram como interfaz, no como núcleo

Motivo:

- evitar acoplar el producto a un canal.

Consecuencia:

- después se puede agregar web, CLI, Slack, API o extensión browser.

### Decisión 4 — Event-driven incremental

Motivo:

- el conocimiento necesita reprocesamiento, workers y pipelines.

Consecuencia:

- más diseño inicial, pero permite crecer sin reescribir todo.

### Decisión 5 — Knowledge Processor como pieza central

Motivo:

- el valor no está en responder, sino en transformar mensajes en conocimiento.

Consecuencia:

- este agente debe tener prioridad sobre la experiencia superficial de chat.

## 22. Mejoras futuras

- graph database dedicado
- graph visualization
- workflows de aprobación
- inbox de conocimiento pendiente
- revisión semanal automática
- detección de contradicciones
- comparación histórica de decisiones
- generación automática de ADRs
- integración con GitHub issues
- extensión de navegador
- importador de ChatGPT exports
- importador de Telegram history
- digest diario/semanal
- recomendaciones proactivas
- mapas de conocimiento
- scoring de confiabilidad
- personal knowledge graph por usuario
- team knowledge graph compartido

## 23. Plan de implementación incremental

### Paso 1 — Núcleo

- PostgreSQL
- modelo de objetos
- relaciones
- fuentes
- Telegram ingestion
- comandos básicos

### Paso 2 — Procesamiento

- clasificación
- resúmenes
- tags
- embeddings
- FTS

### Paso 3 — Retrieval híbrido

- keyword
- semantic
- structured
- graph expansion básico

### Paso 4 — Modos

- `/research`
- `/decision`
- `/architect`
- `/ask`

### Paso 5 — Dashboard web

- objetos
- relaciones
- proyectos
- fuentes
- revisión manual

### Paso 6 — Escala

- NATS
- workers
- reprocesamiento
- graph explorer
- conectores externos

## 24. Conclusión

La arquitectura correcta para Project Brain no es:

```text
Telegram + LLM + embeddings
```

Eso sería frágil y corto de miras.

La arquitectura correcta es:

```text
Interfaces
→ Orchestrator
→ Multi-agent knowledge processing
→ PostgreSQL knowledge core
→ Hybrid RAG
→ Graph relationships
→ Persistent memory
→ Auditable knowledge artifacts
```

El objetivo no es contestar mejor.

El objetivo es que cada conversación haga crecer una memoria compartida, estructurada, reutilizable y confiable.

Eso es lo que convierte Project Brain en un activo.
