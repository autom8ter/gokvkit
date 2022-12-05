package model

import (
	"context"
	"encoding/json"
	"github.com/autom8ter/gokvkit/internal/util"
	flat2 "github.com/nqd/flat"
	"github.com/palantir/stacktrace"
	"github.com/samber/lo"
	"github.com/spf13/cast"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io"
	"sort"
	"strings"
)

// Ref is a reference to a document
type Ref struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

// Document is a concurrency safe JSON document
type Document struct {
	result gjson.Result
}

// MarshalJSON satisfies the json Marshaler interface
func (d *Document) MarshalJSON() ([]byte, error) {
	return d.Bytes(), nil
}

// NewDocument creates a new json document
func NewDocument() *Document {
	parsed := gjson.Parse("{}")
	return &Document{
		result: parsed,
	}
}

// NewDocumentFromBytes creates a new document from the given json bytes
func NewDocumentFromBytes(json []byte) (*Document, error) {
	if !gjson.ValidBytes(json) {
		return nil, stacktrace.NewError("invalid json: %s", string(json))
	}
	d := &Document{
		result: gjson.ParseBytes(json),
	}
	if !d.Valid() {
		return nil, stacktrace.NewError("invalid document")
	}
	return d, nil
}

// NewDocumentFrom creates a new document from the given value - the value must be json compatible
func NewDocumentFrom(value any) (*Document, error) {
	var err error
	bits, err := json.Marshal(value)
	if err != nil {
		return nil, stacktrace.NewError("failed to json encode value: %#v", value)
	}
	return NewDocumentFromBytes(bits)
}

// Valid returns whether the document is valid
func (d *Document) Valid() bool {
	return gjson.ValidBytes(d.Bytes()) && !d.result.IsArray()
}

// String returns the document as a json string
func (d *Document) String() string {
	return d.result.Raw
}

// Bytes returns the document as json bytes
func (d *Document) Bytes() []byte {
	return []byte(d.result.Raw)
}

// Value returns the document as a map
func (d *Document) Value() map[string]any {
	return cast.ToStringMap(d.result.Value())
}

// Clone allocates a new document with identical values
func (d *Document) Clone() *Document {
	raw := d.result.Raw
	return &Document{result: gjson.Parse(raw)}
}

// Select returns the document with only the selected fields populated
func (d *Document) Select(fields []Select) error {
	if len(fields) == 0 || fields[0].Field == "*" {
		return nil
	}
	var (
		selected = NewDocument()
	)

	patch := map[string]interface{}{}
	for _, f := range fields {
		if !util.IsNil(f.As) && *f.As == "" {
			if !util.IsNil(f.Aggregate) {
				f.As = util.ToPtr(defaultAs(*f.Aggregate, f.Field))
			}
		}
		patch[*f.As] = d.Get(f.Field)
	}
	err := selected.SetAll(patch)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	d.result = selected.result
	return nil
}

// Get gets a field on the document. Get has GJSON syntax support and supports dot notation
func (d *Document) Get(field string) any {
	return d.result.Get(field).Value()
}

// GetString gets a string field value on the document. Get has GJSON syntax support and supports dot notation
func (d *Document) GetString(field string) string {
	return cast.ToString(d.result.Get(field).Value())
}

// GetBool gets a bool field value on the document. GetBool has GJSON syntax support and supports dot notation
func (d *Document) GetBool(field string) bool {
	return cast.ToBool(d.Get(field))
}

// GetFloat gets a bool field value on the document. GetFloat has GJSON syntax support and supports dot notation
func (d *Document) GetFloat(field string) float64 {
	return cast.ToFloat64(d.Get(field))
}

// GetArray gets an array field on the document. Get has GJSON syntax support and supports dot notation
func (d *Document) GetArray(field string) []any {
	return cast.ToSlice(d.Get(field))
}

// Set sets a field on the document. Dot notation is supported.
func (d *Document) Set(field string, val any) error {
	return d.SetAll(map[string]any{
		field: val,
	})
}

func (d *Document) set(field string, val any) error {
	var (
		result string
		err    error
	)
	switch val := val.(type) {
	case gjson.Result:
		result, err = sjson.Set(d.result.Raw, field, val.Value())
	case []byte:
		result, err = sjson.SetRaw(d.result.Raw, field, string(val))
	default:
		result, err = sjson.Set(d.result.Raw, field, val)
	}
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	if !gjson.Valid(result) {
		return stacktrace.NewError("invalid document")
	}
	d.result = gjson.Parse(result)
	return nil
}

// SetAll sets all fields on the document. Dot notation is supported.
func (d *Document) SetAll(values map[string]any) error {
	var err error
	for k, v := range values {
		err = d.set(k, v)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
	return nil
}

// Merge merges the doument with the provided document. This is not an overwrite.
func (d *Document) Merge(with *Document) error {
	if !with.Valid() {
		return stacktrace.NewError("invalid document")
	}
	withMap := with.Value()
	flattened, err := flat2.Flatten(withMap, nil)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	return d.SetAll(flattened)
}

// Del deletes a field from the document
func (d *Document) Del(field string) error {
	return d.DelAll(field)
}

// Del deletes a field from the document
func (d *Document) DelAll(fields ...string) error {
	for _, field := range fields {
		result, err := sjson.Delete(d.result.Raw, field)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		d.result = gjson.Parse(result)
	}
	return nil
}

// Where executes the where clauses against the document and returns true if it passes the clauses
func (d *Document) Where(wheres []Where) (bool, error) {
	for _, w := range wheres {
		switch w.Op {
		case "==":
			if w.Value != d.Get(w.Field) {
				return false, nil
			}
		case "!=":
			if w.Value == d.Get(w.Field) {
				return false, nil
			}
		case ">":
			if d.GetFloat(w.Field) <= cast.ToFloat64(w.Value) {
				return false, nil
			}
		case ">=":
			if d.GetFloat(w.Field) < cast.ToFloat64(w.Value) {
				return false, nil
			}
		case "<":
			if d.GetFloat(w.Field) >= cast.ToFloat64(w.Value) {
				return false, nil
			}
		case "<=":
			if d.GetFloat(w.Field) > cast.ToFloat64(w.Value) {
				return false, nil
			}
		case "in":
			bits, _ := json.Marshal(w.Value)
			arr := gjson.ParseBytes(bits).Array()
			value := d.Get(w.Field)
			match := false
			for _, element := range arr {
				if element.Value() == value {
					match = true
				}
			}
			if !match {
				return false, nil
			}

		case "contains":
			if !strings.Contains(d.GetString(w.Field), cast.ToString(w.Value)) {
				return false, nil
			}
		default:
			return false, stacktrace.NewError("invalid operator: %s", w.Op)
		}
	}
	return true, nil
}

// Scan scans the json document into the value
func (d *Document) Scan(value any) error {
	return util.Decode(d.Value(), &value)
}

// Encode encodes the json document to the io writer
func (d *Document) Encode(w io.Writer) error {
	_, err := w.Write(d.Bytes())
	if err != nil {
		return stacktrace.Propagate(err, "failed to encode document")
	}
	return nil
}

// Documents is an array of documents
type Documents []*Document

// GroupBy groups the documents by the given fields
func (documents Documents) GroupBy(fields []string) map[string]Documents {
	var grouped = map[string]Documents{}
	for _, d := range documents {
		var values []string
		for _, g := range fields {
			values = append(values, cast.ToString(d.Get(g)))
		}
		group := strings.Join(values, ".")
		grouped[group] = append(grouped[group], d)
	}
	return grouped
}

// Slice slices the documents into a subarray of documents
func (documents Documents) Slice(start, end int) Documents {
	return lo.Slice[*Document](documents, start, end)
}

// Filter applies the filter function against the documents
func (documents Documents) Filter(predicate func(document *Document, i int) bool) Documents {
	return lo.Filter[*Document](documents, predicate)
}

// Map applies the mapper function against the documents
func (documents Documents) Map(mapper func(t *Document, i int) *Document) Documents {
	return lo.Map[*Document, *Document](documents, mapper)
}

// ForEach applies the function to each document in the documents
func (documents Documents) ForEach(fn func(next *Document, i int)) {
	lo.ForEach[*Document](documents, fn)
}

// OrderBy orders the documents by the OrderBy clause
func (d Documents) OrderBy(orderBys []OrderBy) Documents {
	if len(orderBys) == 0 {
		return d
	}
	// TODO: support more than one order by
	orderBy := orderBys[0]

	if orderBy.Direction == OrderByDirectionDesc {
		sort.Slice(d, func(i, j int) bool {
			index := 1
			if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
				return compareField(orderBy.Field, d[i], d[j])
			}
			for index < len(orderBys) {
				order := orderBys[index]
				if d[i].Get(order.Field) != d[j].Get(order.Field) {
					return compareField(order.Field, d[i], d[j])
				}
				if d[i].Get(order.Field) != d[j].Get(order.Field) {
					if order.Direction == OrderByDirectionDesc {
						if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
							return compareField(orderBy.Field, d[i], d[j])
						}
					} else {
						if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
							return !compareField(orderBy.Field, d[i], d[j])
						}
					}
				}
				index++
			}
			return false
		})
	} else {
		sort.Slice(d, func(i, j int) bool {
			index := 1
			if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
				return !compareField(orderBy.Field, d[i], d[j])
			}
			for index < len(orderBys) {
				order := orderBys[index]
				if d[i].Get(order.Field) != d[j].Get(order.Field) {
					return !compareField(order.Field, d[i], d[j])
				}
				if d[i].Get(order.Field) != d[j].Get(order.Field) {
					if order.Direction == OrderByDirectionDesc {
						if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
							return compareField(orderBy.Field, d[i], d[j])
						}
					} else {
						if d[i].Get(orderBy.Field) != d[j].Get(orderBy.Field) {
							return !compareField(orderBy.Field, d[i], d[j])
						}
					}
				}
				index++
			}
			return false

		})
	}
	return d
}

// Aggregate reduces the documents with the input aggregates
func (d Documents) Aggregate(ctx context.Context, aggregates []Select) (*Document, error) {
	var (
		aggregated *Document
	)
	for _, next := range d {
		if aggregated == nil || !aggregated.Valid() {
			aggregated = next
		}
		for _, agg := range aggregates {
			if util.IsNil(agg.As) {
				agg.As = util.ToPtr(defaultAs(*agg.Aggregate, agg.Field))
			}
			if agg.Aggregate == nil {
				if err := aggregated.Set(*agg.As, aggregated.Get(agg.Field)); err != nil {
					return nil, stacktrace.Propagate(err, "")
				}
				continue
			}
			current := aggregated.GetFloat(*agg.As)
			switch *agg.Aggregate {
			case SelectAggregateCount:
				current++
			case SelectAggregateMax:
				if value := next.GetFloat(agg.Field); value > current {
					current = value
				}
			case SelectAggregateMin:
				if value := next.GetFloat(agg.Field); value < current {
					current = value
				}
			case SelectAggregateSum:
				current += next.GetFloat(agg.Field)
			default:
				return nil, stacktrace.NewError("unsupported aggregate function: %s/%s", agg.Field, *agg.Aggregate)
			}
			if err := aggregated.Set(*agg.As, current); err != nil {
				return nil, stacktrace.Propagate(err, "")
			}
		}
	}
	return aggregated, nil
}
