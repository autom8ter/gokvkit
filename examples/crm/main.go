package main

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/autom8ter/gokvkit"
	_ "github.com/autom8ter/gokvkit/kv/badger"
	"github.com/autom8ter/gokvkit/model"
	"github.com/autom8ter/gokvkit/testutil"
	"net/http"
	"os"
)

var (
	//go:embed schemas/task.yaml
	taskSchema string
	//go:embed schemas/user.yaml
	userSchema string
)

func main() {
	os.MkdirAll("./tmp/crm", 0700)
	c, err := newCRM(context.Background(), "./tmp/crm")
	if err != nil {
		panic(err)
	}
	if err := c.Serve(context.Background(), 8080); err != nil {
		panic(err)
	}
}

type CRM struct {
	db *gokvkit.DB
}

func newCRM(ctx context.Context, dataDir string) (*CRM, error) {
	db, err := gokvkit.New(ctx, gokvkit.Config{
		KV: gokvkit.KVConfig{
			Provider: "badger",
			Params: map[string]any{
				"storage_path": dataDir,
			},
		},
	}, gokvkit.WithOnPersist(map[string][]gokvkit.OnPersist{
		"user": {
			{
				Name:   "cascade_delete_task",
				Before: true,
				Func:   cascadeDelete,
			},
		},
	}))
	if err != nil {
		return nil, err
	}
	if err := db.ConfigureCollection(ctx, []byte(userSchema)); err != nil {
		return nil, err
	}
	if err := db.ConfigureCollection(ctx, []byte(taskSchema)); err != nil {
		return nil, err
	}
	if err := setupDatabase(ctx, db); err != nil {
		return nil, err
	}
	return &CRM{
		db: db,
	}, nil
}

func (c *CRM) Serve(ctx context.Context, port int) error {
	return http.ListenAndServe(fmt.Sprintf(":%v", port), c.db.Handler())
}

func cascadeDelete(ctx context.Context, tx gokvkit.Tx, command *model.Command) error {
	if command.Action == model.Delete {
		results, err := tx.Query(ctx, gokvkit.NewQueryBuilder().From("task").Where(model.Where{
			Field: "user",
			Op:    "==",
			Value: command.DocID,
		}).Query())
		if err != nil {
			return err
		}
		for _, result := range results.Documents {
			err = tx.Delete(ctx, "task", result.GetString("_id"))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func setupDatabase(ctx context.Context, db *gokvkit.DB) error {
	fmt.Println("seeding database")
	for i := 0; i < 1000; i++ {
		if err := db.Tx(context.Background(), func(ctx context.Context, tx gokvkit.Tx) error {
			id, err := tx.Create(ctx, "user", testutil.NewUserDoc())
			if err != nil {
				return err
			}
			for i := 0; i < 5; i++ {
				if _, err := tx.Create(ctx, "task", testutil.NewTaskDoc(id)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	fmt.Println("finished seeding database")
	return nil
}
