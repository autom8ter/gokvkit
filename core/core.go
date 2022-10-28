package core

import (
	"context"
)

// CoreAPI is the core api powering database functionality
type CoreAPI interface {
	//  Persist persists changes to a collection
	Persist(ctx context.Context, collection *Collection, change StateChange) error
	// Aggregate aggregates documents in the database
	Aggregate(ctx context.Context, collection *Collection, query AggregateQuery) (Page, error)
	// Query queries for documents
	Query(ctx context.Context, collection *Collection, query Query) (Page, error)
	// Scan scans the collection applying the scanner function to each matching document
	// it is less memory intensive that Query, which doesn't returns the full list of matching documents
	Scan(ctx context.Context, collection *Collection, scan Scan, scanner ScanFunc) error
	// ChangeStream streams state changes to the given function
	ChangeStream(ctx context.Context, collection *Collection, fn ChangeStreamHandler) error
	// Close closes the database
	Close(ctx context.Context) error
}
