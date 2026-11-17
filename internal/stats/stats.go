// Package stats owns relation and column distribution statistics. The
// planner uses them to estimate access costs without enumerating index
// candidates; estimates only ever influence path choice, never results.
package stats

import (
	"fmt"
	"lamina/internal/catalog"
	"lamina/internal/datum"
	"math"
	"sort"
)

// MaxBuckets bounds the equi-depth histogram kept per column.
const MaxBuckets = 16

// Bucket is one equi-depth histogram step: Upper is the largest sampled
// value in the bucket and Count how many sampled non-null values it holds.
type Bucket struct {
	Upper datum.Value `json:"upper"`
	Count int         `json:"count"`
}

// Column describes the distribution of one column within the sampled rows.
// Counts are relative to Table.Sampled. Min and Max are null when every
// sampled value is null.
type Column struct {
	Nulls    int         `json:"nulls"`
	Distinct int         `json:"distinct"`
	Min      datum.Value `json:"min"`
	Max      datum.Value `json:"max"`
	Buckets  []Bucket    `json:"buckets,omitempty"`
}

// Table captures relation statistics as of a committed sequence. Rows is the
// exact number of visible rows at AsOf; Sampled is the number of rows the
// column distributions were built from, equal to Rows without sampling.
type Table struct {
	Relation string            `json:"relation"`
	AsOf     uint64            `json:"as_of"`
	Rows     int               `json:"rows"`
	Sampled  int               `json:"sampled"`
	Columns  map[string]Column `json:"columns"`
}

// Collect builds statistics for columns from the rows visible at sequence
// asOf. A sample of zero reads every row; a positive sample bounds the rows
// examined by striding the sorted primary keys deterministically, so repeated
// analyzes of the same data agree. Row count stays exact either way.
func Collect(r catalog.Relation, rows map[string]datum.Row, columns []string, sample int, asOf uint64) Table {
	t := Table{Relation: r.Name, AsOf: asOf, Rows: len(rows), Columns: map[string]Column{}}
	keys := datum.Keys(rows)
	if sample > 0 && sample < len(keys) {
		picked := make([]string, 0, sample)
		for i := 0; i < sample; i++ {
			picked = append(picked, keys[i*len(keys)/sample])
		}
		keys = picked
	}
	t.Sampled = len(keys)
	for _, name := range columns {
		t.Columns[name] = column(rows, keys, name)
	}
	return t
}

func column(rows map[string]datum.Row, keys []string, name string) Column {
	c := Column{}
	values := []datum.Value{}
	for _, key := range keys {
		v := rows[key][name]
		if v.Null() {
			c.Nulls++
			continue
		}
		values = append(values, v)
	}
	if len(values) == 0 {
		return c
	}
	sort.Slice(values, func(i, j int) bool { return datum.Order(values[i], values[j]) < 0 })
	c.Min, c.Max = values[0], values[len(values)-1]
	c.Distinct = 1
	for i := 1; i < len(values); i++ {
		if values[i].Key() != values[i-1].Key() {
			c.Distinct++
		}
	}
	k := len(values)
	if k > MaxBuckets {
		k = MaxBuckets
	}
	for j := 0; j < k; j++ {
		end := (j + 1) * len(values) / k
		c.Buckets = append(c.Buckets, Bucket{Upper: values[end-1], Count: end - j*len(values)/k})
	}
	return c
}

// Estimate returns the estimated candidate keys an index on column would
// produce for the predicate, scaled from the sample to the table rows
// recorded at AsOf. The second result is false when the column has no
// statistics and the planner must fall back to exact enumeration.
func (t Table) Estimate(column, op string, v datum.Value) (int, bool) {
	c, ok := t.Columns[column]
	if !ok || v.Null() {
		return 0, false
	}
	nonnull := t.Sampled - c.Nulls
	var est float64
	switch op {
	case "=":
		if nonnull > 0 && c.Distinct > 0 && datum.Order(v, c.Min) >= 0 && datum.Order(v, c.Max) <= 0 {
			est = float64(nonnull) / float64(c.Distinct)
		}
	case "<":
		est = c.fraction(v, false) * float64(nonnull)
	case "<=":
		est = c.fraction(v, true) * float64(nonnull)
	case ">":
		est = (1 - c.fraction(v, true)) * float64(nonnull)
	case ">=":
		est = (1 - c.fraction(v, false)) * float64(nonnull)
	default:
		return 0, false
	}
	if t.Sampled > 0 {
		est *= float64(t.Rows) / float64(t.Sampled)
	}
	if est < 0 {
		est = 0
	}
	return int(math.Round(est)), true
}

// fraction estimates the share of non-null sampled values below v, or at
// most v when inclusive, assuming values spread uniformly inside a bucket.
func (c Column) fraction(v datum.Value, inclusive bool) float64 {
	total := 0
	for _, b := range c.Buckets {
		total += b.Count
	}
	if total == 0 {
		return 0
	}
	counted := 0.0
	lower := c.Min
	for _, b := range c.Buckets {
		n := datum.Order(b.Upper, v)
		if n < 0 || (inclusive && n == 0) {
			counted += float64(b.Count)
		} else {
			counted += within(lower, b.Upper, v, inclusive) * float64(b.Count)
			break
		}
		lower = b.Upper
	}
	return counted / float64(total)
}

// within interpolates the share of a bucket spanning (lower, upper] that a
// predicate at v covers; non-numeric values split the bucket evenly.
func within(lower, upper, v datum.Value, inclusive bool) float64 {
	if datum.Order(v, lower) <= 0 {
		return 0
	}
	if datum.Order(v, upper) >= 0 {
		return 1
	}
	if lower.Kind == "int" && upper.Kind == "int" {
		if inclusive {
			return float64(v.Int-lower.Int+1) / float64(upper.Int-lower.Int+1)
		}
		return float64(v.Int-lower.Int) / float64(upper.Int-lower.Int)
	}
	return 0.5
}

// Validate checks statistics against the relation they describe so replayed
// or restored records cannot reference unknown columns or impossible counts.
func (t Table) Validate(r catalog.Relation) error {
	if t.Relation != r.Name {
		return fmt.Errorf("statistics relation mismatch")
	}
	if t.AsOf < r.Created {
		return fmt.Errorf("statistics predate relation")
	}
	if t.Rows < 0 || t.Sampled < 0 || t.Sampled > t.Rows {
		return fmt.Errorf("invalid statistics sample counts")
	}
	for name, c := range t.Columns {
		if _, ok := r.Column(name); !ok {
			return fmt.Errorf("statistics for unknown column %s", name)
		}
		nonnull := t.Sampled - c.Nulls
		if c.Nulls < 0 || nonnull < 0 || c.Distinct < 0 || c.Distinct > nonnull {
			return fmt.Errorf("invalid column statistics for %s", name)
		}
		if len(c.Buckets) > MaxBuckets {
			return fmt.Errorf("too many buckets for %s", name)
		}
		sum := 0
		for i, b := range c.Buckets {
			if b.Count <= 0 || b.Upper.Null() {
				return fmt.Errorf("invalid bucket for %s", name)
			}
			if i > 0 && datum.Order(c.Buckets[i-1].Upper, b.Upper) > 0 {
				return fmt.Errorf("unordered buckets for %s", name)
			}
			sum += b.Count
		}
		if sum != nonnull {
			return fmt.Errorf("bucket counts inconsistent for %s", name)
		}
		if nonnull == 0 {
			if !c.Min.Null() || !c.Max.Null() {
				return fmt.Errorf("invalid bounds for %s", name)
			}
		} else if c.Min.Null() || c.Max.Null() || datum.Order(c.Min, c.Max) > 0 {
			return fmt.Errorf("invalid bounds for %s", name)
		}
	}
	return nil
}

// Clone deep-copies the table so stored statistics never alias mutable input.
func (t Table) Clone() Table {
	out := t
	out.Columns = make(map[string]Column, len(t.Columns))
	for name, c := range t.Columns {
		c.Buckets = append([]Bucket(nil), c.Buckets...)
		out.Columns[name] = c
	}
	return out
}
