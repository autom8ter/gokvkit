package gokvkit

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/autom8ter/gokvkit/errors"
	"github.com/autom8ter/gokvkit/internal/indexing"
	"github.com/autom8ter/gokvkit/kv"
	"github.com/autom8ter/gokvkit/model"
	"github.com/nqd/flat"
	"github.com/segmentio/ksuid"
)

func (t *transaction) updateDocument(ctx context.Context, c CollectionSchema, command *model.Command) error {
	primaryIndex := c.PrimaryIndex()
	after := command.Before.Clone()
	flattened, err := flat.Flatten(command.After.Value(), nil)
	if err != nil {
		return err
	}
	err = after.SetAll(flattened)
	if err != nil {
		return err
	}
	command.After = after
	if err := c.ValidateCommand(ctx, command); err != nil {
		return err
	}
	if err := t.tx.Set(indexing.SeekPrefix(c.Collection(), primaryIndex, map[string]any{
		c.PrimaryKey(): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}
	return nil
}

func (t *transaction) deleteDocument(ctx context.Context, c CollectionSchema, command *model.Command) error {
	if err := c.ValidateCommand(ctx, command); err != nil {
		return err
	}
	primaryIndex := c.PrimaryIndex()
	if err := t.tx.Delete(indexing.SeekPrefix(c.Collection(), primaryIndex, map[string]any{
		c.PrimaryKey(): command.DocID,
	}).SetDocumentID(command.DocID).Path()); err != nil {
		return errors.Wrap(err, 0, "failed to batch delete documents")
	}
	return nil
}

func (t *transaction) createDocument(ctx context.Context, c CollectionSchema, command *model.Command) error {
	primaryIndex := c.PrimaryIndex()
	if command.DocID == "" {
		command.DocID = ksuid.New().String()
		if err := c.SetPrimaryKey(command.After, command.DocID); err != nil {
			return err
		}
	}
	if err := c.ValidateCommand(ctx, command); err != nil {
		return err
	}
	if err := t.tx.Set(indexing.SeekPrefix(c.Collection(), primaryIndex, map[string]any{
		c.PrimaryKey(): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}

	return nil
}

func (t *transaction) setDocument(ctx context.Context, c CollectionSchema, command *model.Command) error {
	if err := c.ValidateCommand(ctx, command); err != nil {
		return err
	}
	primaryIndex := c.PrimaryIndex()
	if err := t.tx.Set(indexing.SeekPrefix(c.Collection(), primaryIndex, map[string]any{
		c.PrimaryKey(): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}
	return nil
}

func (t *transaction) persistCommand(ctx context.Context, md *model.Metadata, command *model.Command) error {
	if md.Exists(string(isIndexingKey)) {
		for _, i := range t.db.collections.Get(command.Collection).Indexing() {
			if i.Primary {
				continue
			}
			if err := t.updateSecondaryIndex(ctx, i, command); err != nil {
				return errors.Wrap(err, errors.Internal, "")
			}
		}
		return nil
	}
	if err := t.applyPersistHooks(ctx, t, command, true); err != nil {
		return err
	}
	c := t.db.collections.Get(command.Collection)
	if c == nil {
		return fmt.Errorf("collection: %s does not exist", command.Collection)
	}
	if command.Timestamp.IsZero() {
		command.Timestamp = time.Now()
	}
	if command.Metadata == nil {
		md, _ := model.GetMetadata(ctx)
		command.Metadata = md
	}
	before, _ := t.db.Get(ctx, command.Collection, command.DocID)
	if before == nil || !before.Valid() {
		before = model.NewDocument()
	}
	command.Before = before
	switch command.Action {
	case model.Update:
		if err := t.updateDocument(ctx, c, command); err != nil {
			return err
		}
	case model.Create:
		if err := t.createDocument(ctx, c, command); err != nil {
			return err
		}
	case model.Delete:
		if err := t.deleteDocument(ctx, c, command); err != nil {
			return err
		}
	case model.Set:
		if err := t.setDocument(ctx, c, command); err != nil {
			return err
		}
	}
	for _, i := range t.db.collections.Get(command.Collection).Indexing() {
		if i.Primary {
			continue
		}
		if err := t.updateSecondaryIndex(ctx, i, command); err != nil {
			return errors.Wrap(err, errors.Internal, "")
		}
	}
	if err := t.applyPersistHooks(ctx, t, command, false); err != nil {
		return err
	}
	return nil
}

func (t *transaction) updateSecondaryIndex(ctx context.Context, idx model.Index, command *model.Command) error {
	if idx.Primary {
		return nil
	}
	switch command.Action {
	case model.Delete:
		if err := t.tx.Delete(indexing.SeekPrefix(command.Collection, idx, command.Before.Value()).SetDocumentID(command.DocID).Path()); err != nil {
			return errors.Wrap(
				err,
				errors.Internal,
				"failed to delete document %s/%s index references",
				command.Collection,
				command.DocID,
			)
		}
	case model.Set, model.Update, model.Create:
		if command.Before != nil {
			if err := t.tx.Delete(indexing.SeekPrefix(command.Collection, idx, command.Before.Value()).SetDocumentID(command.DocID).Path()); err != nil {
				return errors.Wrap(
					err,
					errors.Internal,
					"failed to delete document %s/%s index references",
					command.Collection,
					command.DocID,
				)
			}
		}
		if idx.Unique && !idx.Primary && command.After != nil {
			if err := t.db.kv.Tx(false, func(tx kv.Tx) error {
				it := tx.NewIterator(kv.IterOpts{
					Prefix: indexing.SeekPrefix(command.Collection, idx, command.After.Value()).Path(),
				})
				defer it.Close()
				for it.Valid() {
					item := it.Item()
					split := bytes.Split(item.Key(), []byte("\x00"))
					id := split[len(split)-1]
					if string(id) != command.DocID {
						return errors.New(errors.Internal, "duplicate value( %s ) found for unique index: %s", command.DocID, idx.Name)
					}
					it.Next()
				}
				return nil
			}); err != nil {
				return errors.Wrap(
					err,
					errors.Internal,
					"failed to set document %s/%s index references",
					command.Collection,
					command.DocID,
				)
			}
		}
		// only persist ids in secondary index - lookup full document in primary index
		if err := t.tx.Set(indexing.SeekPrefix(command.Collection, idx, command.After.Value()).SetDocumentID(command.DocID).Path(), []byte(command.DocID)); err != nil {
			return errors.Wrap(
				err,
				errors.Internal,
				"failed to set document %s/%s index references",
				command.Collection,
				command.DocID,
			)
		}
	}
	return nil
}

func (t *transaction) applyWhereHooks(ctx context.Context, collection string, where []model.Where) ([]model.Where, error) {
	var err error
	for _, whereHook := range t.db.whereHooks.Get(collection) {
		where, err = whereHook.Func(ctx, t, where)
		if err != nil {
			return nil, err
		}
	}
	return where, nil
}

func (t *transaction) applyReadHooks(ctx context.Context, collection string, doc *model.Document) (*model.Document, error) {
	var err error
	for _, readHook := range t.db.readHooks.Get(collection) {
		doc, err = readHook.Func(ctx, t, doc)
		if err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func (t *transaction) applyPersistHooks(ctx context.Context, tx Tx, command *model.Command, before bool) error {
	for _, sideEffect := range t.db.persistHooks.Get(command.Collection) {
		if sideEffect.Before == before {
			if err := sideEffect.Func(ctx, tx, command); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *transaction) queryScan(ctx context.Context, scan model.Scan, handlerFunc model.ScanFunc) (model.Optimization, error) {
	if handlerFunc == nil {
		return model.Optimization{}, errors.New(errors.Validation, "empty scan handler")
	}
	if !t.db.HasCollection(scan.Collection) {
		return model.Optimization{}, errors.New(errors.Validation, "unsupported collection: %s", scan.Collection)
	}
	var err error
	scan.Where, err = t.applyWhereHooks(ctx, scan.Collection, scan.Where)
	if err != nil {
		return model.Optimization{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	optimization, err := t.db.optimizer.Optimize(t.db.collections.Get(scan.Collection), scan.Where)
	if err != nil {
		return model.Optimization{}, err
	}

	pfx := indexing.SeekPrefix(scan.Collection, optimization.Index, optimization.MatchedValues)
	opts := kv.IterOpts{
		Prefix:  pfx.Path(),
		Seek:    nil,
		Reverse: false,
	}
	it := t.tx.NewIterator(opts)
	defer it.Close()
	for it.Valid() {
		item := it.Item()

		var document *model.Document
		if optimization.Index.Primary {
			bits, err := item.Value()
			if err != nil {
				return optimization, err
			}
			document, err = model.NewDocumentFromBytes(bits)
			if err != nil {
				return optimization, err
			}
		} else {
			split := bytes.Split(item.Key(), []byte("\x00"))
			id := split[len(split)-1]
			document, err = t.Get(ctx, scan.Collection, string(id))
			if err != nil {
				return optimization, err
			}
		}

		pass, err := document.Where(scan.Where)
		if err != nil {
			return optimization, err
		}
		if pass {
			document, err = t.applyReadHooks(ctx, scan.Collection, document)
			if err != nil {
				return optimization, err
			}
			shouldContinue, err := handlerFunc(document)
			if err != nil {
				return optimization, err
			}
			if !shouldContinue {
				return optimization, err
			}
		}
		it.Next()
	}
	return optimization, nil
}
