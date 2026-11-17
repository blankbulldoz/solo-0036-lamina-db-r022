package query

import (
	"lamina/internal/catalog"
	"lamina/internal/index"
	"lamina/internal/stats"
	"sort"
)

// candidate pairs an index with a predicate the index can serve.
type candidate struct {
	index string
	pred  Predicate
}

func candidates(c *catalog.Catalog, q Request, at uint64) []candidate {
	names := make([]string, 0, len(c.Indexes))
	for name := range c.Indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []candidate
	for _, name := range names {
		definition := c.Indexes[name]
		if definition.Relation != q.Relation || definition.Created > at {
			continue
		}
		for _, p := range q.Where {
			if p.Column != definition.Column || p.Value.Null() {
				continue
			}
			switch p.Op {
			case "=", "<", ">", "<=", ">=":
				out = append(out, candidate{index: name, pred: p})
			}
		}
	}
	return out
}

// Choose picks an access path. When statistics cover every candidate index
// column it compares estimated candidate counts and only enumerates the
// chosen path; otherwise it enumerates every candidate set exactly. Either
// way the executed candidates are re-interpreted against real versions and
// all predicates are re-checked, so an estimate can never change results.
func Choose(c *catalog.Catalog, registry *index.Registry, q Request, at uint64, total int, st *stats.Table) (Plan, []string) {
	plan := Plan{Access: "scan", Candidates: total, Snapshot: at, Residuals: len(q.Where), Basis: "exact"}
	if q.ForceScan {
		plan.Basis = "forced"
		return plan, nil
	}
	pairs := candidates(c, q, at)
	if len(pairs) == 0 {
		return plan, nil
	}
	if st != nil {
		if plan, keys, ok := estimate(registry, pairs, *st, plan); ok {
			return plan, keys
		}
	}
	var chosen []string
	for _, pair := range pairs {
		keys := registry.Trees[pair.index].Candidates(pair.pred.Op, pair.pred.Value)
		if len(keys) < plan.Candidates || plan.Access == "scan" && len(keys) == 0 {
			plan.Access = "index"
			plan.Index = pair.index
			plan.Candidates = len(keys)
			chosen = keys
		}
	}
	return plan, chosen
}

// estimate prices every candidate path from statistics. The third result is
// false when a candidate column lacks statistics and the caller must fall
// back to exact enumeration for the whole query.
func estimate(registry *index.Registry, pairs []candidate, st stats.Table, plan Plan) (Plan, []string, bool) {
	plan.Basis = "estimate"
	plan.StatsAt = st.AsOf
	plan.Estimates = []Estimate{{Access: "scan", Candidates: st.Rows}}
	estimates := make([]int, len(pairs))
	for i, pair := range pairs {
		n, ok := st.Estimate(pair.pred.Column, pair.pred.Op, pair.pred.Value)
		if !ok {
			return Plan{}, nil, false
		}
		estimates[i] = n
		plan.Estimates = append(plan.Estimates, Estimate{Access: "index", Index: pair.index, Column: pair.pred.Column, Op: pair.pred.Op, Candidates: n})
	}
	best, chosen := st.Rows, -1
	for i, n := range estimates {
		if n < best || chosen < 0 && n == 0 {
			best, chosen = n, i
		}
	}
	if chosen < 0 {
		return plan, nil, true
	}
	pair := pairs[chosen]
	keys := registry.Trees[pair.index].Candidates(pair.pred.Op, pair.pred.Value)
	plan.Access = "index"
	plan.Index = pair.index
	plan.Candidates = len(keys)
	return plan, keys, true
}
