// Package triggers is the trigger engine and its CRUD surface (story S26; architecture §8).
//
// A trigger is data, evaluated in four stages — match → conditions → guard → actions — and
// every terminal outcome of stages 2–4 writes a trigger_firings row, including the outcomes
// that did nothing. "The rule fired but nothing happened" having a name is the point.
//
// The pieces:
//
//   - service.go / validate.go / http.go — CRUD per contracts §5, validated against the
//     registered event sources' catalogs and the action registry.
//   - engine.go — the bus subscriber and the pipeline, serialized per project.
//   - match.go — stage 1: kind, activity types, branch/path/label filters. Non-match = no row.
//   - conditions.go — stage 2: the {all}/{any}/{field,op,value} tree, every contracts §4.1
//     operator, total and nil-safe, no expression language.
//   - kernel/guard — stage 3: guard.Pass until S27 lands the loop-protection layers.
//   - interp.go — {{...}} interpolation: path lookup only, never control flow.
//
// One glob dialect everywhere: stdlib path.Match (see conditions.go's globMatch).
package triggers
