package query

import "lamina/internal/datum"

type Predicate struct {
	Column string      `json:"column"`
	Op     string      `json:"op"`
	Value  datum.Value `json:"value"`
}
type Ordering struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}
type Aggregate struct {
	Function string `json:"function"`
	Column   string `json:"column,omitempty"`
}
type Request struct {
	Relation    string      `json:"relation"`
	Columns     []string    `json:"columns,omitempty"`
	Where       []Predicate `json:"where,omitempty"`
	Order       []Ordering  `json:"order,omitempty"`
	Limit       int         `json:"limit,omitempty"`
	At          *uint64     `json:"at,omitempty"`
	Transaction string      `json:"transaction,omitempty"`
	Aggregate   *Aggregate  `json:"aggregate,omitempty"`
	ForceScan   bool        `json:"force_scan,omitempty"`
}

// Estimate is the planner's predicted candidate count for one considered
// access path. Estimates explain why a path was chosen; they are never
// execution facts and never influence the rows a query returns.
type Estimate struct {
	Access     string `json:"access"`
	Index      string `json:"index,omitempty"`
	Column     string `json:"column,omitempty"`
	Op         string `json:"op,omitempty"`
	Candidates int    `json:"candidates"`
}
type Plan struct {
	Access     string     `json:"access"`
	Index      string     `json:"index,omitempty"`
	Candidates int        `json:"candidates"`
	Snapshot   uint64     `json:"snapshot"`
	Residuals  int        `json:"residuals"`
	Basis      string     `json:"basis"`
	StatsAt    uint64     `json:"stats_at,omitempty"`
	Estimates  []Estimate `json:"estimates,omitempty"`
}
type Result struct {
	Rows      []datum.Row `json:"rows"`
	Plan      Plan        `json:"plan"`
	Scanned   int         `json:"scanned"`
	Matched   int         `json:"matched"`
	Truncated bool        `json:"truncated"`
}
