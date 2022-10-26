package store

import (
	"context"
	"github.com/autom8ter/machine/v4"
	"github.com/autom8ter/wolverine/core"
	"github.com/autom8ter/wolverine/errors"
	"github.com/autom8ter/wolverine/internal/prefix"
	"github.com/autom8ter/wolverine/schema"
	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/search/query"
	"github.com/dgraph-io/badger/v3"
	"github.com/palantir/stacktrace"
	"github.com/samber/lo"
	"github.com/spf13/cast"
	"io"
	"strings"
	"time"
)

type defaultStore struct {
	kv       *badger.DB
	fullText map[string]bleve.Index
	machine  machine.Machine
}

func Core(kv *badger.DB, fullText map[string]bleve.Index, machine machine.Machine) core.Core {
	d := defaultStore{
		kv:       kv,
		fullText: fullText,
		machine:  machine,
	}
	return core.Core{
		Persist:      d.persistCollection,
		Aggregate:    d.aggregateCollection,
		Search:       d.searchCollection,
		Query:        d.queryCollection,
		Get:          d.getCollection,
		GetAll:       d.getAllCollection,
		ChangeStream: d.changeStreamCollection,
		Close:        d.closeAll,
		Backup:       d.backup,
		Restore:      d.restore,
	}
}

func (d defaultStore) changeStreamCollection(ctx context.Context, collection *schema.Collection, fn schema.ChangeStreamHandler) error {
	return d.machine.Subscribe(ctx, collection.Collection(), func(ctx context.Context, msg machine.Message) (bool, error) {
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

func (d defaultStore) aggregateCollection(ctx context.Context, collection *schema.Collection, query schema.AggregateQuery) (schema.Page, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	now := time.Now()
	index, err := collection.OptimizeQueryIndex(query.Where, query.OrderBy)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	var results []schema.Document
	if err := d.kv.View(func(txn *badger.Txn) error {
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

func (d defaultStore) getAllCollection(ctx context.Context, collection *schema.Collection, ids []string) ([]schema.Document, error) {
	var documents []schema.Document
	if err := d.kv.View(func(txn *badger.Txn) error {
		for _, id := range ids {
			pkey, err := collection.GetPrimaryKeyRef(id)
			if err != nil {
				return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", collection.Collection(), id)
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

func (d defaultStore) getCollection(ctx context.Context, collection *schema.Collection, id string) (schema.Document, error) {
	var (
		document schema.Document
	)
	pkey, err := collection.GetPrimaryKeyRef(id)
	if err != nil {
		return schema.Document{}, stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", collection.Collection(), id)
	}
	if err := d.kv.View(func(txn *badger.Txn) error {
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

func (d defaultStore) persistCollection(ctx context.Context, collection *schema.Collection, change schema.StateChange) error {
	txn := d.kv.NewWriteBatch()
	var batch *bleve.Batch
	if collection == nil {
		return stacktrace.NewErrorWithCode(errors.ErrTODO, "null collection schema")
	}
	if collection.Indexing().HasSearchIndex() {
		batch = d.fullText[collection.Collection()].NewBatch()
	}
	if change.Updates != nil {
		for id, edit := range change.Updates {
			before, _ := d.getCollection(ctx, collection, id)
			after, err := before.SetAll(edit)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if err := d.indexDocument(ctx, collection, txn, batch, schema.Update, id, before, after); err != nil {
				return stacktrace.Propagate(err, "")
			}
		}
	}
	for _, id := range change.Deletes {
		before, _ := d.getCollection(ctx, collection, id)
		if err := d.indexDocument(ctx, collection, txn, batch, schema.Delete, id, before, schema.NewDocument()); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
	for _, after := range change.Sets {
		if !after.Valid() {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "invalid json document")
		}
		docId := collection.GetDocumentID(after)
		if docId == "" {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "document missing primary key %s", collection.Indexing().PrimaryKey)
		}
		before, _ := d.getCollection(ctx, collection, docId)
		if err := d.indexDocument(ctx, collection, txn, batch, schema.Set, docId, before, after); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}

	if batch != nil {
		if err := d.fullText[collection.Collection()].Batch(batch); err != nil {
			return stacktrace.Propagate(err, "failed to batch collection documents")
		}
	}
	if err := txn.Flush(); err != nil {
		return stacktrace.Propagate(err, "failed to batch collection documents")
	}
	d.machine.Publish(ctx, machine.Message{
		Channel: change.Collection,
		Body:    change,
	})
	return nil
}

func (d defaultStore) indexDocument(ctx context.Context, collection *schema.Collection, txn *badger.WriteBatch, batch *bleve.Batch, action schema.Action, docId string, before, after schema.Document) error {
	if docId == "" {
		return stacktrace.NewErrorWithCode(errors.ErrTODO, "empty document id")
	}
	pkey, err := collection.GetPrimaryKeyRef(docId)
	if err != nil {
		return stacktrace.PropagateWithCode(err, errors.ErrTODO, "failed to get document %s/%s primary key ref", collection.Collection(), docId)
	}
	switch action {
	case schema.Delete:
		if !before.Valid() {
			return nil
		}
		for _, i := range collection.Indexing().Query {
			pindex := collection.QueryIndexPrefix(*i)
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
		if collection.GetDocumentID(after) != docId {
			return stacktrace.NewErrorWithCode(errors.ErrTODO, "document id is immutable: %v -> %v", collection.GetDocumentID(after), docId)
		}
		valid, err := collection.Validate(after)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		if !valid {
			return stacktrace.NewError("%s/%s document has invalid schema", collection.Collection(), docId)
		}
		for _, idx := range collection.Indexing().Query {
			pindex := collection.QueryIndexPrefix(*idx)
			if before.Valid() {
				if err := txn.Delete(pindex.GetPrefix(before.Value(), docId)); err != nil {
					return stacktrace.PropagateWithCode(
						err,
						errors.ErrTODO,
						"failed to delete document %s/%s index references",
						collection.Collection(),
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
					collection.Collection(),
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

func (d defaultStore) queryCollection(ctx context.Context, collection *schema.Collection, query schema.Query) (schema.Page, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	now := time.Now()
	index, err := collection.OptimizeQueryIndex(query.Where, query.OrderBy)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "")
	}
	var results []schema.Document
	if err := d.kv.View(func(txn *badger.Txn) error {
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

func (d defaultStore) searchCollection(ctx context.Context, collection *schema.Collection, q schema.SearchQuery) (schema.Page, error) {
	if !collection.Indexing().HasSearchIndex() {
		return schema.Page{}, stacktrace.NewErrorWithCode(
			errors.ErrTODO,
			"%s does not have a search index",
			collection.Collection(),
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
	results, err := d.fullText[collection.Collection()].Search(searchRequest)
	if err != nil {
		return schema.Page{}, stacktrace.Propagate(err, "failed to search index: %s", collection.Collection())
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
			return schema.Page{}, stacktrace.Propagate(err, "failed to search index: %s", collection.Collection())
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

func (d defaultStore) closeAll(ctx context.Context) error {
	for _, i := range d.fullText {
		if err := i.Close(); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
	if err := d.kv.Sync(); err != nil {
		return stacktrace.Propagate(err, "")
	}
	return stacktrace.Propagate(d.kv.Close(), "")
}

func (d defaultStore) backup(ctx context.Context, w io.Writer, since uint64) error {
	_, err := d.kv.Backup(w, since)
	if err != nil {
		return stacktrace.Propagate(err, "failed backup")
	}
	return nil
}

func (d defaultStore) restore(ctx context.Context, r io.Reader) error {
	if err := d.kv.Load(r, 256); err != nil {
		return stacktrace.Propagate(err, "")
	}
	return nil
}
