package myjson

import (
	"context"
)

type internalMetaKey string

const (
	internalKey   internalMetaKey = "_internal"
	isIndexingKey internalMetaKey = "_is_indexing"
)

func isInternal(ctx context.Context) bool {
	return ctx.Value(string(internalKey)) == true
}

func isIndexing(ctx context.Context) bool {
	return ctx.Value(string(isIndexingKey)) == true
}
