package wolverine

import (
	"context"
	"github.com/autom8ter/machine/v4"
	"github.com/autom8ter/wolverine/errors"
	"github.com/autom8ter/wolverine/internal/prefix"
	"github.com/autom8ter/wolverine/schema"
	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/search/query"
	"github.com/dgraph-io/badger/v3"
	"github.com/palantir/stacktrace"
	"github.com/samber/lo"
	"github.com/spf13/cast"
	"strings"
	"time"
)

type coreV1 struct {
	persist      PersistFunc
	aggregate    AggregateFunc
	search       SearchFunc
	query        QueryFunc
	get          GetFunc
	getAll       GetAllFunc
	changeStream ChangeStreamFunc
}

func getCoreV1() coreV1 {
	return coreV1{
		persist:      persistCollection,
		aggregate:    aggregateCollection,
		search:       searchCollection,
		query:        queryCollection,
		get:          getCollection,
		getAll:       getAllCollection,
		changeStream: changeStreamCollection,
	}
}

type Middleware struct {
	Persist      []PersistWare
	Aggregate    []AggregateWare
	Search       []SearchWare
	Query        []QueryWare
	Get          []GetWare
	GetAll       []GetAllWare
	ChangeStream []ChangeStreamWare
}

func (c coreV1) Apply(m Middleware) coreV1 {
	core := coreV1{
		persist:      c.persist,
		aggregate:    c.aggregate,
		search:       c.search,
		query:        c.query,
		get:          c.get,
		getAll:       c.getAll,
		changeStream: c.changeStream,
	}
	if m.Persist != nil {
		for _, m := range m.Persist {
			core.persist = m(core.persist)
		}
	}
	if m.Aggregate != nil {
		for _, m := range m.Aggregate {
			core.aggregate = m(core.aggregate)
		}
	}
	if m.Search != nil {
		for _, m := range m.Search {
			core.search = m(core.search)
		}
	}
	if m.Query != nil {
		for _, m := range m.Query {
			core.query = m(core.query)
		}
	}
	if m.Get != nil {
		for _, m := range m.Get {
			core.get = m(core.get)
		}
	}
	if m.GetAll != nil {
		for _, m := range m.GetAll {
			core.getAll = m(core.getAll)
		}
	}
	if m.ChangeStream != nil {
		for _, m := range m.ChangeStream {
			core.changeStream = m(core.changeStream)
		}
	}
	return core
}

// PersistFunc persists changes to a collection
type PersistFunc func(ctx context.Context, c *Collection, change schema.StateChange) error

// PersistWare wraps a PersistFunc and returns a new one
type PersistWare func(PersistFunc) PersistFunc

// AggregateFunc aggregates documents to a collection
type AggregateFunc func(ctx context.Context, c *Collection, query schema.AggregateQuery) (schema.Page, error)

// AggregateWare wraps a AggregateFunc and returns a new one
type AggregateWare func(AggregateFunc) AggregateFunc

// SearchFunc searches documents in a collection
type SearchFunc func(ctx context.Context, c *Collection, query schema.SearchQuery) (schema.Page, error)

// SearchWare wraps a SearchFunc and returns a new one
type SearchWare func(SearchFunc) SearchFunc

// QueryFunc queries documents in a collection
type QueryFunc func(ctx context.Context, c *Collection, query schema.Query) (schema.Page, error)

// QueryWare wraps a QueryFunc and returns a new one
type QueryWare func(QueryFunc) QueryFunc

// GetFunc gets documents in a collection
type GetFunc func(ctx context.Context, c *Collection, id string) (schema.Document, error)

// GetWare wraps a GetFunc and returns a new one
type GetWare func(GetFunc) GetFunc

// GetAllFunc gets multiple documents in a collection
type GetAllFunc func(ctx context.Context, c *Collection, ids []string) ([]schema.Document, error)

// GetAllWare wraps a GetAllFunc and returns a new one
type GetAllWare func(GetAllFunc) GetAllFunc

// ChangeStreamFunc listens to changes in a ccollection
type ChangeStreamFunc func(ctx context.Context, c *Collection, fn schema.ChangeStreamHandler) error

// ChangeStreamWare wraps a ChangeStreamFunc and returns a new one
type ChangeStreamWare func(ChangeStreamFunc) ChangeStreamFunc

func changeStreamCollection(ctx context.Context, c *Collection, fn schema.ChangeStreamHandler) error {
	return c.machine.Subscribe(ctx, c.schema.Collection(), func(ctx context.Context, msg machine.Message) (bool, error) {
		switch change := msg.Body.(type) {
		case *schema.StateChange:
			if err := fn(ctx, *change); err != nil {
				return false, stacktrace.Propagate(err, "")
			}
		case schema.StateChange:
			if err := fn(ctx, change); err != nil {
				return false, stacktrace.Propagate(err, "")
			}
		}
		return true, nil
	})
}

func aggregateCollection(ctx context.Context, c *Collection, query schema.AggregateQuery) (schema.Page, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	now := time.Now()
	index, err := c.schema.OptimizeQueryIndex(query.Where, query.OrderBy)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	var results []schema.Document
	if err := c.kv.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		opts.PrefetchSize = 10
		opts.Prefix = index.Ref.GetPrefix(schema.IndexableFields(query.Where, query.OrderBy), "")
		it := txn.NewIterator(opts)
		it.Seek(opts.Prefix)
		defer it.Close()
		for it.ValidForPrefix(opts.Prefix) {
			if ctx.Err() != nil {
				return nil
			}
			item := it.Item()
			err := item.Value(func(bits []byte) error {
				document, err := schema.NewDocumentFromBytes(bits)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				pass, err := document.Where(query.Where)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				if !pass {
					return nil
				}
				results = append(results, document)
				return nil
			})
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			it.Next()
		}
		return nil
	}); err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	grouped := lo.GroupBy[schema.Document](results, func(d schema.Document) string {
		var values []string
		for _, g := range query.GroupBy {
			values = append(values, cast.ToString(d.Get(g)))
		}
		return strings.Join(values, ".")
	})
	var reduced []schema.Document
	for _, values := range grouped {
		value, err := schema.ApplyReducers(ctx, query, values)
		if err != nil {
			return schema.Page{}, stacktrace.Propagate(err, "")
		}
		reduced = append(reduced, value)
	}
	reduced = schema.SortOrder(query.OrderBy, reduced)
	if query.Limit > 0 && query.Page > 0 {
		reduced = lo.Slice(reduced, query.Limit*query.Page, (query.Limit*query.Page)+query.Limit)
	}
	if query.Limit > 0 && len(reduced) > query.Limit {
		reduced = reduced[:query.Limit]
	}
	for i, r := range reduced {
		toSelect := query.GroupBy
		for _, a := range query.Aggregates {
			toSelect = append(toSelect, a.Alias)
		}
		selected, err := r.Select(toSelect)
		if err != nil {
			return schema.Page{}, stacktrace.Propagate(err, "")
		}
		reduced[i] = selected
	}
	return schema.Page{
		Documents: reduced,
		NextPage:  query.Page + 1,
		Count:     len(reduced),
		Stats: schema.PageStats{
			ExecutionTime: time.Since(now),
			IndexMatch:    index,
		},
	}, nil
}

func getAllCollection(ctx context.Context, c *Collection, ids []string) ([]schema.Document, error) {
	var documents []schema.Document
	if err := c.kv.View(func(txn *badger.Txn) error {
		for _, id := range ids {
			pkey, err := c.schema.GetPrimaryKeyRef(id)
			if err != nil {
				return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", c.schema.Collection(), id)
			}
			item, err := txn.Get(pkey)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if err := item.Value(func(val []byte) error {
				document, err := schema.NewDocumentFromBytes(val)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				documents = append(documents, document)
				return nil
			}); err != nil {
				return stacktrace.Propagate(err, "")
			}
		}
		return nil
	}); err != nil {
		return documents, err
	}
	return documents, nil
}

func getCollection(ctx context.Context, c *Collection, id string) (schema.Document, error) {
	var (
		document schema.Document
	)
	pkey, err := c.schema.GetPrimaryKeyRef(id)
	if err != nil {
		return schema.Document{}, stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", c.schema.Collection(), id)
	}
	if err := c.kv.View(func(txn *badger.Txn) error {
		item, err := txn.Get(pkey)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		return item.Value(func(val []byte) error {
			document, err = schema.NewDocumentFromBytes(val)
			return stacktrace.Propagate(err, "")
		})
	}); err != nil {
		return document, err
	}
	return document, nil
}

func persistCollection(ctx context.Context, c *Collection, change schema.StateChange) error {
	txn := c.kv.NewWriteBatch()
	var batch *bleve.Batch
	if c.schema == nil {
		return stacktrace.NewErrorWithCode(errors.ErrTODO, "null collection schema")
	}
	if c.schema.Indexing().HasSearchIndex() {
		batch = c.fullText.NewBatch()
	}
	if change.Updates != nil {
		for id, edit := range change.Updates {
			before, _ := c.Get(ctx, id)
			after, err := before.SetAll(edit)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if err := indexDocument(ctx, c, txn, batch, schema.Update, id, before, after); err != nil {
				return stacktrace.Propagate(err, "")
			}
		}
	}
	for _, id := range change.Deletes {
		before, _ := c.Get(ctx, id)
		if err := indexDocument(ctx, c, txn, batch, schema.Delete, id, before, schema.NewDocument()); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
	for _, after := range change.Sets {
		if !after.Valid() {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "invalid json document")
		}
		docId := c.schema.GetDocumentID(after)
		if docId == "" {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "document missing primary key %s", c.schema.Indexing().PrimaryKey)
		}
		before, _ := c.Get(ctx, docId)
		if err := indexDocument(ctx, c, txn, batch, schema.Set, docId, before, after); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}

	if batch != nil {
		if err := c.fullText.Batch(batch); err != nil {
			c.db.errorHandler(c.schema.Collection(), err)
		}
	}
	if err := txn.Flush(); err != nil {
		return stacktrace.Propagate(err, "failed to batch collection documents")
	}
	c.machine.Publish(ctx, machine.Message{
		Channel: change.Collection,
		Body:    change,
	})
	return nil
}

func indexDocument(ctx context.Context, c *Collection, txn *badger.WriteBatch, batch *bleve.Batch, action schema.Action, docId string, before, after schema.Document) error {
	if docId == "" {
		return stacktrace.NewErrorWithCode(errors.ErrTODO, "empty document id")
	}
	pkey, err := c.schema.GetPrimaryKeyRef(docId)
	if err != nil {
		return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", c.schema.Collection(), docId)
	}
	switch action {
	case schema.Delete:
		if !before.Valid() {
			return nil
		}
		for _, i := range c.schema.Indexing().Query {
			pindex := c.schema.QueryIndexPrefix(*i)
			if err := txn.Delete(pindex.GetPrefix(before.Value(), docId)); err != nil {
				return stacktrace.Propagate(err, "failed to batch delete documents")
			}
		}
		if err := txn.Delete(pkey); err != nil {
			return stacktrace.Propagate(err, "failed to batch delete documents")
		}
		if batch != nil {
			batch.Delete(docId)
		}
	case schema.Set, schema.Update:
		if c.Schema().GetDocumentID(after) != docId {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "document id is immutable: %v -> %v", c.Schema().GetDocumentID(after), docId)
		}
		valid, err := c.schema.Validate(after)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		if !valid {
			return stacktrace.NewError("%s/%s document has invalid schema", c.schema.Collection(), docId)
		}
		for _, idx := range c.schema.Indexing().Query {
			pindex := c.schema.QueryIndexPrefix(*idx)
			if before.Valid() {
				if err := txn.Delete(pindex.GetPrefix(before.Value(), docId)); err != nil {
					return stacktrace.PropagateWithCode(
						err,
						errors.ErrTODO,
						"failed to delete document %s/%s index references",
						c.schema.Collection(),
						docId,
					)
				}
			}
			i := pindex.GetPrefix(after.Value(), docId)
			if err := txn.Set(i, after.Bytes()); err != nil {
				return stacktrace.PropagateWithCode(
					err,
					errors.ErrTODO,
					"failed to set document %s/%s index references",
					c.schema.Collection(),
					docId,
				)
			}
		}
		if err := txn.Set(pkey, after.Bytes()); err != nil {
			return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to batch set documents to primary index")
		}
		if batch != nil {
			if err := batch.Index(docId, after.Value()); err != nil {
				return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to batch set documents to search index")
			}
		}
	}
	return nil
}

func queryCollection(ctx context.Context, c *Collection, query schema.Query) (schema.Page, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	now := time.Now()
	index, err := c.schema.OptimizeQueryIndex(query.Where, query.OrderBy)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	var results []schema.Document
	if err := c.kv.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		opts.PrefetchSize = 10
		opts.Prefix = index.Ref.GetPrefix(schema.IndexableFields(query.Where, query.OrderBy), "")
		seek := opts.Prefix

		if query.OrderBy.Direction == schema.DESC {
			opts.Reverse = true
			seek = prefix.PrefixNextKey(opts.Prefix)
		}
		it := txn.NewIterator(opts)
		it.Seek(seek)
		defer it.Close()
		for it.ValidForPrefix(opts.Prefix) {
			item := it.Item()
			err := item.Value(func(bits []byte) error {
				document, err := schema.NewDocumentFromBytes(bits)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				pass, err := document.Where(query.Where)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				if !pass {
					return nil
				}
				results = append(results, document)
				return nil
			})
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			it.Next()
		}
		return nil
	}); err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	results = schema.SortOrder(query.OrderBy, results)

	if query.Limit > 0 && query.Page > 0 {
		results = lo.Slice(results, query.Limit*query.Page, (query.Limit*query.Page)+query.Limit)
	}
	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}

	if len(query.Select) > 0 && query.Select[0] != "*" {
		for i, result := range results {
			selected, err := result.Select(query.Select)
			if err != nil {
				return schema.Page{}, stacktrace.Propagate(err, "")
			}
			results[i] = selected
		}
	}

	return schema.Page{
		Documents: results,
		NextPage:  query.Page + 1,
		Count:     len(results),
		Stats: schema.PageStats{
			ExecutionTime: time.Since(now),
			IndexMatch:    index,
		},
	}, nil
}

func searchCollection(ctx context.Context, c *Collection, q schema.SearchQuery) (schema.Page, error) {
	if !c.schema.Indexing().HasSearchIndex() {
		return schema.Page{}, stacktrace.NewErrorWithCode(
			errors.ErrTODO,
			"%s does not have a search index",
			c.schema.Collection(),
		)
	}

	now := time.Now()
	var (
		fields []string
		limit  = q.Limit
	)
	for _, w := range q.Where {
		fields = append(fields, w.Field)
	}
	if limit == 0 {
		limit = 1000
	}
	var queries []query.Query
	for _, where := range q.Where {
		if where.Value == nil {
			return schema.Page{}, stacktrace.NewError("empty where clause value")
		}
		switch where.Op {
		case schema.Basic:
			switch where.Value.(type) {
			case bool:
				qry := bleve.NewBoolFieldQuery(cast.ToBool(where.Value))
				if where.Boost > 0 {
					qry.SetBoost(where.Boost)
				}
				qry.SetField(where.Field)
				queries = append(queries, qry)
			case float64, int, int32, int64, float32, uint64, uint, uint8, uint16, uint32:
				qry := bleve.NewNumericRangeQuery(lo.ToPtr(cast.ToFloat64(where.Value)), nil)
				if where.Boost > 0 {
					qry.SetBoost(where.Boost)
				}
				qry.SetField(where.Field)
				queries = append(queries, qry)
			default:
				qry := bleve.NewMatchQuery(cast.ToString(where.Value))
				if where.Boost > 0 {
					qry.SetBoost(where.Boost)
				}
				qry.SetField(where.Field)
				queries = append(queries, qry)
			}
		case schema.DateRange:
			var (
				from time.Time
				to   time.Time
			)
			split := strings.Split(cast.ToString(where.Value), ",")
			from = cast.ToTime(split[0])
			if len(split) == 2 {
				to = cast.ToTime(split[1])
			}
			qry := bleve.NewDateRangeQuery(from, to)
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.TermRange:
			var (
				from string
				to   string
			)
			split := strings.Split(cast.ToString(where.Value), ",")
			from = split[0]
			if len(split) == 2 {
				to = split[1]
			}
			qry := bleve.NewTermRangeQuery(from, to)
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.GeoDistance:
			var (
				from     float64
				to       float64
				distance string
			)
			split := strings.Split(cast.ToString(where.Value), ",")
			if len(split) < 3 {
				return schema.Page{}, stacktrace.NewError("geo distance where clause requires 3 comma separated values: lat(float), lng(float), distance(string)")
			}
			from = cast.ToFloat64(split[0])
			to = cast.ToFloat64(split[1])
			distance = cast.ToString(split[2])
			qry := bleve.NewGeoDistanceQuery(from, to, distance)
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.Prefix:
			qry := bleve.NewPrefixQuery(cast.ToString(where.Value))
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.Fuzzy:
			qry := bleve.NewFuzzyQuery(cast.ToString(where.Value))
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.Regex:
			qry := bleve.NewRegexpQuery(cast.ToString(where.Value))
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		case schema.Wildcard:
			qry := bleve.NewWildcardQuery(cast.ToString(where.Value))
			if where.Boost > 0 {
				qry.SetBoost(where.Boost)
			}
			qry.SetField(where.Field)
			queries = append(queries, qry)
		}
	}
	if len(queries) == 0 {
		queries = []query.Query{bleve.NewMatchAllQuery()}
	}
	var searchRequest *bleve.SearchRequest
	if len(queries) > 1 {
		searchRequest = bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(queries...), limit, q.Page*limit, false)
	} else {
		searchRequest = bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(queries[0]), limit, q.Page*limit, false)
	}
	searchRequest.Fields = []string{"*"}
	results, err := c.fullText.Search(searchRequest)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "failed to search index: %s", c.schema.Collection())
	}

	var data []schema.Document
	if len(results.Hits) == 0 {
		return schema.Page{}, stacktrace.NewError("zero results")
	}
	for _, h := range results.Hits {
		if len(h.Fields) == 0 {
			continue
		}
		record, err := schema.NewDocumentFrom(h.Fields)
		if err != nil {
			return schema.Page{}, stacktrace.Propagate(err, "failed to search index: %s", c.schema.Collection())
		}
		data = append(data, record)
	}

	if len(q.Select) > 0 && q.Select[0] != "*" {
		for i, r := range data {
			selected, err := r.Select(q.Select)
			if err != nil {
				return schema.Page{}, stacktrace.Propagate(err, "")
			}
			data[i] = selected
		}
	}
	return schema.Page{
		Documents: data,
		NextPage:  q.Page + 1,
		Count:     len(data),
		Stats: schema.PageStats{
			ExecutionTime: time.Since(now),
		},
	}, nil
}
