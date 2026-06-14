package postgres

import (
	"github.com/frankirova/project-brain/internal/app"
	"github.com/jackc/pgx/v5"
)

// Package postgres — uow_bundles: the three Unit-of-Work bundles exposed to
// the application layer. Moved from per-bundle files (ingestion_repo.go,
// object_validation_repo.go, object_debate_repo.go) in change-18 PR3 cleanup
// — one concept (UoW composition), one file.
//
// Each bundle is created exclusively from within a `*DB.Within<Name>Tx`
// callback in db.go and is the multi-repo surface the service consumes
// inside the BeginTx → Commit/Rollback boundary. The bundle fields are the
// per-table repos from this package — no new repository structs are
// introduced here; this file is the assembly point for the three UoWs.
//
// `debateRepositories` deliberately mirrors `objectValidationRepositories`
// shape (Objects + AuditEvents) but is a distinct type so the debate
// service cannot accidentally pick up a future method added only to the
// validation side. The underlying repository structs
// (`knowledgeObjectRepository`, `auditEventRepository`) are reused by
// value composition — the debate bundle does NOT introduce a parallel
// set of write-path repository structs.

// --- ingestion UoW ----------------------------------------------------------

type repositories struct {
	sources          *sourceRepository
	knowledgeObjects *knowledgeObjectRepository
	objectSources    *objectSourceRepository
	auditEvents      *auditEventRepository
}

func newRepositories(tx pgx.Tx) *repositories {
	return &repositories{
		sources:          &sourceRepository{tx: tx},
		knowledgeObjects: &knowledgeObjectRepository{tx: tx},
		objectSources:    &objectSourceRepository{tx: tx},
		auditEvents:      &auditEventRepository{tx: tx},
	}
}

func (r *repositories) Sources() app.SourceRepository                   { return r.sources }
func (r *repositories) KnowledgeObjects() app.KnowledgeObjectRepository { return r.knowledgeObjects }
func (r *repositories) ObjectSources() app.ObjectSourceRepository       { return r.objectSources }
func (r *repositories) AuditEvents() app.AuditEventRepository           { return r.auditEvents }

var _ app.IngestionRepositories = (*repositories)(nil)

// --- object-validation UoW --------------------------------------------------

type objectValidationRepositories struct {
	objects     *knowledgeObjectRepository
	auditEvents *auditEventRepository
}

func newObjectValidationRepositories(tx pgx.Tx) *objectValidationRepositories {
	return &objectValidationRepositories{
		objects:     &knowledgeObjectRepository{tx: tx},
		auditEvents: &auditEventRepository{tx: tx},
	}
}

func (r *objectValidationRepositories) Objects() app.ObjectValidationObjectRepository {
	return r.objects
}
func (r *objectValidationRepositories) AuditEvents() app.AuditEventRepository {
	return r.auditEvents
}

var _ app.ObjectValidationRepositories = (*objectValidationRepositories)(nil)

// --- object-debate UoW ------------------------------------------------------

type debateRepositories struct {
	objects     *knowledgeObjectRepository
	auditEvents *auditEventRepository
}

func newDebateRepositories(tx pgx.Tx) *debateRepositories {
	return &debateRepositories{
		objects:     &knowledgeObjectRepository{tx: tx},
		auditEvents: &auditEventRepository{tx: tx},
	}
}

func (r *debateRepositories) Objects() app.ObjectDebateObjectRepository { return r.objects }
func (r *debateRepositories) AuditEvents() app.AuditEventRepository     { return r.auditEvents }

var _ app.ObjectDebateRepositories = (*debateRepositories)(nil)
