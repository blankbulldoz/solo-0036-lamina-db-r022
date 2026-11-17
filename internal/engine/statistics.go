package engine

import (
	"lamina/internal/journal"
	"lamina/internal/stats"
	"sort"
)

// StatisticsView reports stored planner statistics against the current head
// sequence so callers can judge how stale each table's estimates are: a
// table is stale when its as_of predates sequence.
type StatisticsView struct {
	Sequence  uint64        `json:"sequence"`
	Relations []stats.Table `json:"relations"`
}

// Analyze collects distribution statistics for a relation and publishes them
// as a durable journal record, replacing any previous statistics for the
// relation. An empty columns list covers every column. A sample of zero
// reads every visible row; a positive sample bounds the rows the column
// distributions are built from. Collection runs under the database lock and
// pauses commits and queries like a checkpoint does.
func (d *DB) Analyze(relation string, columns []string, sample int) (stats.Table, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.usable(); err != nil {
		return stats.Table{}, err
	}
	r, err := d.catalog.Relation(relation)
	if err != nil {
		return stats.Table{}, invalid(err)
	}
	if sample < 0 {
		return stats.Table{}, fault("invalid", "sample must be >= 0")
	}
	if len(columns) == 0 {
		for _, c := range r.Columns {
			columns = append(columns, c.Name)
		}
	} else {
		seen := map[string]bool{}
		for _, name := range columns {
			if _, ok := r.Column(name); !ok {
				return stats.Table{}, fault("invalid", "unknown column %s", name)
			}
			if seen[name] {
				return stats.Table{}, fault("invalid", "duplicate column %s", name)
			}
			seen[name] = true
		}
	}
	t := stats.Collect(r, d.store.Scan(relation, d.sequence), columns, sample, d.sequence)
	if err := d.publish(journal.Record{Kind: "analyze", Statistics: &t}); err != nil {
		return stats.Table{}, err
	}
	return t, nil
}

// Statistics returns the persisted planner statistics and the current head
// sequence. Estimates derived from these tables only influence access path
// choice; query results never depend on how fresh the statistics are.
func (d *DB) Statistics() (StatisticsView, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := d.usable(); err != nil {
		return StatisticsView{}, err
	}
	out := StatisticsView{Sequence: d.sequence, Relations: []stats.Table{}}
	names := make([]string, 0, len(d.statistics))
	for name := range d.statistics {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Relations = append(out.Relations, d.statistics[name].Clone())
	}
	return out, nil
}
