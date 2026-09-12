// Package handlers holds request/response types whose Go type name is observable
// on the wire. The reference daemon decodes plugins.prune params into a
// handlers.PruneParams, and encoding/json embeds that type name in the
// -32602 "Invalid params" body on a type mismatch (for example
// "cannot unmarshal array into Go value of type handlers.PruneParams"). Go renders
// the SHORT package name, so this file lives in a package literally named
// "handlers" to reproduce that byte exactly. It is the only reason this package
// exists; keep it to the types whose name reaches the wire.
package handlers

// PruneParams is the params object for plugins.prune (reference build 19f30c46).
// Keep is a caller-supplied list accepted for parity but not consulted by the
// handler — the reference pruned a valid old plugin whose hash was in Keep, so the
// result's "kept" count stays 0 (see methods_plugins.go). MinAgeDays is the
// retention window in days;
// the handler clamps it to [7, 3650] and an absent value reads as 0, which clamps
// up to 7. The field order matches the reference so the field-mismatch form of the
// -32602 error ("PruneParams.minAgeDays") also matches.
type PruneParams struct {
	Keep       []string `json:"keep"`
	MinAgeDays int      `json:"minAgeDays"`
}
