package gokvkit

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/autom8ter/gokvkit/errors"
	"github.com/autom8ter/gokvkit/kv"
	"github.com/autom8ter/gokvkit/model"
	"github.com/nqd/flat"
	"github.com/segmentio/ksuid"
)

const batchThreshold = 10

func (d *DB) updateDocument(ctx context.Context, mutator kv.Mutator, c *collectionSchema, command *model.Command) error {

	primaryIndex := d.primaryIndex(command.Collection)
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
	if err := c.validateCommand(ctx, command); err != nil {
		return err
	}
	if err := mutator.Set(primaryIndex.SeekPrefix(map[string]any{
		d.PrimaryKey(command.Collection): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}
	return nil
}

func (d *DB) deleteDocument(ctx context.Context, mutator kv.Mutator, c *collectionSchema, command *model.Command) error {
	if err := c.validateCommand(ctx, command); err != nil {
		return err
	}
	primaryIndex := d.primaryIndex(command.Collection)
	if err := mutator.Delete(primaryIndex.SeekPrefix(map[string]any{
		d.PrimaryKey(command.Collection): command.DocID,
	}).SetDocumentID(command.DocID).Path()); err != nil {
		return errors.Wrap(err, 0, "failed to batch delete documents")
	}
	return nil
}

func (d *DB) createDocument(ctx context.Context, mutator kv.Mutator, c *collectionSchema, command *model.Command) error {
	primaryIndex := d.primaryIndex(command.Collection)
	if command.DocID == "" {
		command.DocID = ksuid.New().String()
		if err := d.SetPrimaryKey(command.Collection, command.After, command.DocID); err != nil {
			return err
		}
	}
	if err := c.validateCommand(ctx, command); err != nil {
		return err
	}
	if err := mutator.Set(primaryIndex.SeekPrefix(map[string]any{
		d.PrimaryKey(command.Collection): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}

	return nil
}

func (d *DB) setDocument(ctx context.Context, mutator kv.Mutator, c *collectionSchema, command *model.Command) error {
	if err := c.validateCommand(ctx, command); err != nil {
		return err
	}
	primaryIndex := d.primaryIndex(command.Collection)
	if err := mutator.Set(primaryIndex.SeekPrefix(map[string]any{
		d.PrimaryKey(command.Collection): command.DocID,
	}).SetDocumentID(command.DocID).Path(), command.After.Bytes()); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to batch set documents to primary index")
	}
	return nil
}

func (d *DB) persistStateChange(ctx context.Context, mutator kv.Mutator, commands []*model.Command) error {
	for _, command := range commands {
		c := d.collections.Get(command.Collection)
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
		before, _ := d.Get(ctx, command.Collection, command.DocID)
		if before == nil || !before.Valid() {
			before = model.NewDocument()
		}
		command.Before = before
		switch command.Action {
		case model.Update:
			if err := d.updateDocument(ctx, mutator, c, command); err != nil {
				return err
			}
		case model.Create:
			if err := d.createDocument(ctx, mutator, c, command); err != nil {
				return err
			}
		case model.Delete:
			if err := d.deleteDocument(ctx, mutator, c, command); err != nil {
				return err
			}
		case model.Set:
			if err := d.setDocument(ctx, mutator, c, command); err != nil {
				return err
			}
		}
		for _, i := range d.collections.Get(command.Collection).indexing {
			if i.Primary {
				continue
			}
			if err := d.updateSecondaryIndex(ctx, mutator, i, command); err != nil {
				return errors.Wrap(err, errors.Internal, "")
			}
		}
	}
	return nil
}

func (d *DB) updateSecondaryIndex(ctx context.Context, mutator kv.Mutator, idx model.Index, command *model.Command) error {
	if idx.Primary {
		return nil
	}

	switch command.Action {
	case model.Delete:
		if err := mutator.Delete(idx.SeekPrefix(command.Before.Value()).SetDocumentID(command.DocID).Path()); err != nil {
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
			if err := mutator.Delete(idx.SeekPrefix(command.Before.Value()).SetDocumentID(command.DocID).Path()); err != nil {
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
			if err := d.kv.Tx(false, func(tx kv.Tx) error {
				it := tx.NewIterator(kv.IterOpts{
					Prefix: idx.SeekPrefix(command.After.Value()).Path(),
				})
				defer it.Close()
				for it.Valid() {
					item := it.Item()
					split := bytes.Split(item.Key(), []byte("\x00"))
					id := split[len(split)-1]
					if string(id) != command.DocID {
						return errors.Wrap(nil, errors.Internal, "duplicate value( %s ) found for unique index: %s", command.DocID, idx.Name)
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
		if err := mutator.Set(idx.SeekPrefix(command.After.Value()).SetDocumentID(command.DocID).Path(), []byte(command.DocID)); err != nil {
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

func (d *DB) getReadyIndexes(ctx context.Context, collection string) map[string]model.Index {
	var indexes = map[string]model.Index{}
	for _, i := range d.collections.Get(collection).indexing {
		if i.IsBuilding {
			continue
		}
		indexes[i.Name] = i
	}
	return indexes
}

func (d *DB) queryScan(ctx context.Context, scan model.Scan, handlerFunc model.ScanFunc) (model.OptimizerResult, error) {
	if handlerFunc == nil {
		return model.OptimizerResult{}, errors.Wrap(nil, 0, "empty scan handler")
	}
	var err error
	scan.Where, err = d.applyWhereHooks(ctx, scan.From, scan.Where)
	if err != nil {
		return model.OptimizerResult{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	index, err := d.optimizer.Optimize(d.getReadyIndexes(ctx, scan.From), scan.Where)
	if err != nil {
		return model.OptimizerResult{}, err
	}
	pfx := index.Ref.SeekPrefix(index.Values)
	if err := d.kv.Tx(false, func(txn kv.Tx) error {
		opts := kv.IterOpts{
			Prefix:  pfx.Path(),
			Seek:    nil,
			Reverse: false,
		}
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Valid() {
			item := it.Item()

			var document *model.Document
			if index.IsPrimaryIndex {
				bits, err := item.Value()
				if err != nil {
					return err
				}
				document, err = model.NewDocumentFromBytes(bits)
				if err != nil {
					return err
				}
			} else {
				split := bytes.Split(item.Key(), []byte("\x00"))
				id := split[len(split)-1]
				document, err = d.Get(ctx, scan.From, string(id))
				if err != nil {
					return err
				}
			}

			pass, err := document.Where(scan.Where)
			if err != nil {
				return err
			}
			if pass {
				document, err = d.applyReadHooks(ctx, scan.From, document)
				if err != nil {
					return err
				}
				shouldContinue, err := handlerFunc(document)
				if err != nil {
					return err
				}
				if !shouldContinue {
					return nil
				}
			}
			it.Next()
		}
		return nil
	}); err != nil {
		return index, err
	}
	return index, nil
}

func (d *DB) applyWhereHooks(ctx context.Context, collection string, where []model.Where) ([]model.Where, error) {
	var err error
	for _, whereHook := range d.whereHooks.Get(collection) {
		where, err = whereHook.Func(ctx, d, where)
		if err != nil {
			return nil, err
		}
	}
	return where, nil
}

func (d *DB) applyReadHooks(ctx context.Context, collection string, doc *model.Document) (*model.Document, error) {
	var err error
	for _, readHook := range d.readHooks.Get(collection) {
		doc, err = readHook.Func(ctx, d, doc)
		if err != nil {
			return nil, err
		}
	}
	return doc, nil
}
func (d *DB) applyPersistHooks(ctx context.Context, tx Tx, command *model.Command, before bool) error {
	for _, sideEffect := range d.persistHooks.Get(command.Collection) {
		if sideEffect.Before == before {
			if err := sideEffect.Func(ctx, tx, command); err != nil {
				return err
			}
		}
	}
	return nil
}
