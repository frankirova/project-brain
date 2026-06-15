// Package runtime builds the project-brain API runtime from a config:
// selects the UoW, wires the service layer, the HTTP server, the
// Telegram bot, and provides the pinned shutdown sequence.
//
// The package exists so cmd/api/main.go is reduced to a small
// composition shell: enforce auth invariant, enforce the
// production+in-memory UoW guard, build, run, shut down. Every
// observable behavior the composition root used to own — boot logs,
// shutdown order, typed-nil interface guards, HTTP server timeouts —
// lives here, with unit tests asserting byte-for-byte identity.
package runtime
