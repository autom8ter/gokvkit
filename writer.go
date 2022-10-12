package wolverine

import (
	"context"

	"github.com/autom8ter/machine/v4"
	"github.com/blevesearch/bleve"
	"github.com/dgraph-io/badger/v3"
	"github.com/palantir/stacktrace"

	"github.com/autom8ter/wolverine/internal/prefix"
)

func (d *db) saveBatch(ctx context.Context, event *Event) error {
	if len(event.Documents) == 0 {
		return nil
	}
	if len(event.Documents) == 1 {
		return d.saveDocument(ctx, event)
	}
	collect, ok := d.getInmemCollection(event.Collection)
	if !ok {
		return stacktrace.Propagate(stacktrace.NewError("unsupported collection: %s must be one of: %v", event.Collection, d.collectionNames()), "")
	}
	for _, document := range event.Documents {
		document.Set("_collection", event.Collection)
		if err := document.Validate(); err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
	txn := d.kv.NewWriteBatch()
	var batch *bleve.Batch
	if collect.FullText() {
		batch = d.getFullText(collect.Collection()).NewBatch()
	}
	for _, document := range event.Documents {
		current, _ := d.Get(ctx, event.Collection, document.GetID())
		if current == nil {
			current = NewDocument()
		}
		for _, c := range d.config.Triggers {
			if err := c(ctx, event.Action, Before, current, document); err != nil {
				return stacktrace.Propagate(err, "trigger failure")
			}
		}
		var bits []byte
		switch event.Action {
		case Set:
			valid, err := collect.Validate(document)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if !valid {
				return stacktrace.NewError("%s/%s document has invalid schema", event.Collection, document.GetID())
			}
			bits = document.Bytes()
		case Update:
			currentClone := current.Clone()
			currentClone.Merge(document)
			valid, err := collect.Validate(currentClone)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if !valid {
				return stacktrace.NewError("%s/%s document has invalid schema", event.Collection, currentClone.GetID())
			}
			bits = currentClone.Bytes()
		}

		switch event.Action {
		case Set, Update:
			pkey := prefix.PrimaryKey(event.Collection, document.GetID())
			if err := txn.SetEntry(&badger.Entry{
				Key:   []byte(pkey),
				Value: bits,
			}); err != nil {
				return stacktrace.Propagate(err, "failed to batch save documents")
			}
			for _, idx := range collect.Indexes() {
				pindex := idx.prefix(event.Collection)
				if current != nil {
					if err := txn.Delete([]byte(pindex.GetIndex(current.GetID(), current.Value()))); err != nil {
						return stacktrace.Propagate(err, "failed to batch save documents")
					}
				}
				i := pindex.GetIndex(document.GetID(), document.Value())
				if err := txn.SetEntry(&badger.Entry{
					Key:   []byte(i),
					Value: bits,
				}); err != nil {
					return stacktrace.Propagate(err, "failed to batch save documents")
				}
			}
			if batch != nil {
				if err := batch.Index(document.GetID(), document.Value()); err != nil {
					return stacktrace.Propagate(err, "failed to batch save documents")
				}
			}
		case Delete:
			for _, i := range collect.Indexes() {
				pindex := i.prefix(event.Collection)
				if err := txn.Delete([]byte(pindex.GetIndex(current.GetID(), current.Value()))); err != nil {
					return stacktrace.Propagate(err, "failed to batch delete documents")
				}
			}
			if err := txn.Delete([]byte(prefix.PrimaryKey(event.Collection, current.GetID()))); err != nil {
				return stacktrace.Propagate(err, "failed to batch delete documents")
			}
			if batch != nil {
				batch.Delete(document.GetID())
			}
		}
		for _, t := range d.config.Triggers {
			if err := t(ctx, event.Action, After, current, document); err != nil {
				return stacktrace.Propagate(err, "trigger failure")
			}
		}
	}
	if batch != nil {
		if err := d.getFullText(collect.Collection()).Batch(batch); err != nil {
			return stacktrace.Propagate(err, "failed to batch documents")
		}
	}
	if err := txn.Flush(); err != nil {
		return stacktrace.Propagate(err, "failed to batch documents")
	}
	d.machine.Publish(ctx, machine.Message{
		Channel: event.Collection,
		Body:    event,
	})
	return nil
}

func (d *db) saveDocument(ctx context.Context, event *Event) error {
	collect, ok := d.getInmemCollection(event.Collection)
	if !ok {
		return stacktrace.Propagate(stacktrace.NewError("unsupported collection: %s must be one of: %v", event.Collection, d.collectionNames()), "")
	}
	if len(event.Documents) == 0 {
		return nil
	}
	if len(event.Documents) > 1 {
		return d.saveBatch(ctx, event)
	}
	document := event.Documents[0]
	if err := document.Validate(); err != nil {
		return stacktrace.Propagate(err, "")
	}
	document.Set("_collection", event.Collection)
	current, _ := d.Get(ctx, event.Collection, document.GetID())
	if current == nil {
		current = NewDocument()
	}
	for _, t := range d.config.Triggers {
		if err := t(ctx, event.Action, Before, current, document); err != nil {
			return stacktrace.Propagate(err, "trigger failure")
		}
	}
	var bits []byte
	switch event.Action {
	case Set:
		valid, err := collect.Validate(document)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		if !valid {
			return stacktrace.NewError("%s/%s document has invalid schema", event.Collection, document.GetID())
		}
		bits = document.Bytes()
	case Update:
		currentClone := current.Clone()
		currentClone.Merge(document)
		valid, err := collect.Validate(currentClone)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
		if !valid {
			return stacktrace.NewError("%s/%s document has invalid schema", event.Collection, document.GetID())
		}
		bits = currentClone.Bytes()
	}

	return d.kv.Update(func(txn *badger.Txn) error {
		switch event.Action {
		case Set, Update:
			if err := txn.SetEntry(&badger.Entry{
				Key:   []byte(prefix.PrimaryKey(event.Collection, document.GetID())),
				Value: bits,
			}); err != nil {
				return stacktrace.Propagate(err, "failed to save document")
			}
			for _, index := range collect.Indexes() {
				pindex := index.prefix(event.Collection)
				if current != nil {
					if err := txn.Delete([]byte(pindex.GetIndex(current.GetID(), current.Value()))); err != nil {
						return stacktrace.Propagate(err, "failed to save document")
					}
				}
				i := pindex.GetIndex(document.GetID(), document.Value())
				if err := txn.SetEntry(&badger.Entry{
					Key:   []byte(i),
					Value: bits,
				}); err != nil {
					return stacktrace.Propagate(err, "failed to save document")
				}
			}
			if collect.FullText() {
				if err := d.getFullText(collect.Collection()).Index(document.GetID(), document.Value()); err != nil {
					return stacktrace.Propagate(err, "failed to save document")
				}
			}
		case Delete:
			for _, index := range collect.Indexes() {
				pindex := index.prefix(event.Collection)
				if err := txn.Delete([]byte(pindex.GetIndex(current.GetID(), current.Value()))); err != nil {
					return stacktrace.Propagate(err, "failed to delete document")
				}
			}
			if err := txn.Delete([]byte(prefix.PrimaryKey(event.Collection, current.GetID()))); err != nil {
				return stacktrace.Propagate(err, "failed to delete document")
			}
			if collect.FullText() {
				if err := d.getFullText(collect.Collection()).Delete(document.GetID()); err != nil {
					return stacktrace.Propagate(err, "failed to delete document")
				}
			}
		}
		for _, t := range d.config.Triggers {
			if err := t(ctx, event.Action, After, current, document); err != nil {
				return stacktrace.Propagate(err, "trigger failure")
			}
		}
		d.machine.Publish(ctx, machine.Message{
			Channel: event.Collection,
			Body:    event,
		})
		return nil
	})
}

func (d *db) Set(ctx context.Context, collection string, document *Document) error {
	return stacktrace.Propagate(d.saveDocument(ctx, &Event{
		Collection: collection,
		Action:     Set,
		Documents:  []*Document{document},
	}), "")
}

func (d *db) BatchSet(ctx context.Context, collection string, batch []*Document) error {
	return stacktrace.Propagate(d.saveBatch(ctx, &Event{
		Collection: collection,
		Action:     Set,
		Documents:  batch,
	}), "")
}

func (d *db) Update(ctx context.Context, collection string, document *Document) error {
	return stacktrace.Propagate(d.saveDocument(ctx, &Event{
		Collection: collection,
		Action:     Update,
		Documents:  []*Document{document},
	}), "")
}

func (d *db) BatchUpdate(ctx context.Context, collection string, batch []*Document) error {
	return d.saveBatch(ctx, &Event{
		Collection: collection,
		Action:     Update,
		Documents:  batch,
	})
}

func (d *db) Delete(ctx context.Context, collection, id string) error {
	doc, err := d.Get(ctx, collection, id)
	if err != nil {
		return stacktrace.Propagate(err, "failed to delete %s/%s", collection, id)
	}
	return d.saveDocument(ctx, &Event{
		Collection: collection,
		Action:     Delete,
		Documents:  []*Document{doc},
	})
}

func (d *db) BatchDelete(ctx context.Context, collection string, ids []string) error {
	var documents []*Document
	for _, id := range ids {
		doc, err := d.Get(ctx, collection, id)
		if err != nil {
			return stacktrace.Propagate(err, "failed to batch delete %s/%s", collection, id)
		}
		documents = append(documents, doc)
	}

	return d.saveBatch(ctx, &Event{
		Collection: collection,
		Action:     Delete,
		Documents:  documents,
	})
}

func (d *db) QueryUpdate(ctx context.Context, update *Document, collection string, query Query) error {
	results, err := d.Query(ctx, collection, query)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	for _, document := range results.Documents {
		document.Merge(update)
	}
	return stacktrace.Propagate(d.BatchSet(ctx, collection, results.Documents), "")
}

func (d *db) QueryDelete(ctx context.Context, collection string, query Query) error {
	results, err := d.Query(ctx, collection, query)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	var ids []string
	for _, document := range results.Documents {
		ids = append(ids, document.GetID())
	}
	return stacktrace.Propagate(d.BatchDelete(ctx, collection, ids), "")
}
