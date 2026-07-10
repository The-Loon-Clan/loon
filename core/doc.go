// Package core is the Phase-0 scaffolding for the indexer-site
// plugin architecture described in PLUGIN-ARCHITECTURE.md at the
// repository root.
//
// At this point in the codebase the package is INERT: it defines
// the Plugin interface, the Core mediator struct, the global
// registry, the topo-sort + migration runner, and a tiny
// /admin/plugins admin page. It does NOT — and must not, in this
// phase — move any existing storage, handler, service, or model
// code. The mediator is a FACADE over what already exists; every
// concrete adapter is constructed in cmd/main.go from the live
// services and injected via core.Boot.
//
// Phase 0 success criteria (see § 13 of the design doc):
//
//   - With zero plugins registered, the binary boots, serves every
//     existing route, and passes every existing test exactly as it
//     does today.
//   - cmd/main.go has a single new call (core.Boot) near the end of
//     its wiring. The rest of main.go is untouched.
//   - A new core.plugin_migrations table is created by the
//     numbered migration runner; the runner inside this package
//     never writes to it until a plugin is registered.
//
// Future phases (Phase 1+) will extract the first real plugin
// (wiki — see § 12 of the design doc), at which point this
// package becomes load-bearing rather than ornamental.
//
// CALLER CONTRACT:
//
//   - Plugins are registered via RegisterPlugin from an init()
//     function in plugins/<name>/plugin.go. The blank-import in
//     cmd/main.go is the manifest of what's compiled into a given
//     binary — Caddy/xcaddy style.
//   - cmd/main.go calls Boot exactly once, after the legacy
//     wiring is complete and before HTTP serving begins.
//   - All inter-plugin coupling flows through Core. Plugins MUST
//     NOT import each other's packages except through interfaces
//     defined on Core or on a peer plugin's exported root API.
package core
