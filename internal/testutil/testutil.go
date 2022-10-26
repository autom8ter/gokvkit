package testutil

import (
	"context"
	"github.com/autom8ter/wolverine/schema"
	"io/ioutil"
	"os"
	"time"

	"github.com/brianvoe/gofakeit/v6"

	_ "embed"
	"github.com/autom8ter/wolverine"
)

func init() {
	var err error
	UserCollection, err = schema.NewCollectionFromBytes([]byte(userSchema))
	if err != nil {
		panic(err)
	}
	TaskCollection, err = schema.NewCollectionFromBytes([]byte(taskSchema))
	if err != nil {
		panic(err)
	}
}

var (
	//go:embed user.json
	userSchema string
	//go:embed task.json
	taskSchema     string
	TaskCollection = schema.NewCollectionFromBytesP([]byte(taskSchema))
	UserCollection = schema.NewCollectionFromBytesP([]byte(userSchema))
	AllCollections = []*schema.Collection{UserCollection, TaskCollection}
)

func NewUserDoc() *schema.Document {
	doc, err := schema.NewDocumentFrom(map[string]interface{}{
		"_id":  gofakeit.UUID(),
		"name": gofakeit.Name(),
		"contact": map[string]interface{}{
			"email": gofakeit.Email(),
		},
		"account_id":      gofakeit.IntRange(0, 100),
		"language":        gofakeit.Language(),
		"birthday_month":  gofakeit.Month(),
		"favorite_number": gofakeit.Second(),
		"gender":          gofakeit.Gender(),
		"age":             gofakeit.IntRange(0, 100),
		"timestamp":       gofakeit.DateRange(time.Now().Truncate(7200*time.Hour), time.Now()),
		"annotations":     gofakeit.Map(),
	})
	if err != nil {
		panic(err)
	}
	return doc
}

func NewTaskDoc(usrID string) *schema.Document {
	doc, err := schema.NewDocumentFrom(map[string]interface{}{
		"_id":     gofakeit.UUID(),
		"user":    usrID,
		"content": gofakeit.LoremIpsumSentence(5),
	})
	if err != nil {
		panic(err)
	}
	return doc
}

const MyEmail = "colemanword@gmail.com"

func TestDB(fn func(ctx context.Context, db *wolverine.DB), collections ...*schema.Collection) error {
	if len(collections) == 0 {
		collections = append(collections, AllCollections...)
	}
	dir, err := ioutil.TempDir(".", "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := wolverine.New(ctx, wolverine.Config{
		StoragePath: dir,
		Collections: collections,
	})
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	fn(ctx, db)
	return nil
}
