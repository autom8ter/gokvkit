package e2e_test

import (
	"testing"
)

func TestConcurrency(t *testing.T) {
	//t.Run("1", func(t *testing.T) {
	//	assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
	//		egp, ctx := errgroup.WithContext(ctx)
	//
	//		for i := 0; i < 100; i++ {
	//			var usrEmail string
	//			err := db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
	//				doc := testutil.NewUserDoc()
	//				usrEmail = doc.GetString("contact.email")
	//				tx.Set(ctx, "user", doc)
	//				return nil
	//			})
	//			assert.Nil(t, err)
	//			egp.Go(func() error {
	//				db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
	//					results, _ := tx.Query(ctx, "user", gokvkit.Q().
	//						Select(gokvkit.Select{Field: "*"}).
	//						Where(gokvkit.Where{
	//							Field: "contact.email",
	//							Op:    gokvkit.WhereOpEq,
	//							Value: usrEmail,
	//						}).Query())
	//					assert.Equal(t, 1, results.Count)
	//					assert.Equal(t, usrEmail, results.Documents[0].Get("contact.email"))
	//					fmt.Println(results.Stats)
	//					return nil
	//				})
	//				return nil
	//			})
	//			time.Sleep(50 * time.Millisecond)
	//		}
	//		for i := 0; i < 5; i++ {
	//			egp.Go(func() error {
	//				{
	//					schema := db.GetSchema(ctx, "user")
	//					bytes, err := schema.MarshalJSON()
	//					assert.Nil(t, err)
	//					newSchema, err := sjson.Set(string(bytes), "properties.contact.properties.email.x-unique", false)
	//					assert.Nil(t, err)
	//					assert.Nil(t, err)
	//					db.ConfigureCollection(ctx, []byte(newSchema))
	//				}
	//				{
	//					schema := db.GetSchema(ctx, "user")
	//					bytes, err := schema.MarshalJSON()
	//					assert.Nil(t, err)
	//					newSchema, err := sjson.Set(string(bytes), "properties.contact.properties.email.x-unique", true)
	//					assert.Nil(t, err)
	//					db.ConfigureCollection(ctx, []byte(newSchema))
	//				}
	//				return nil
	//			})
	//		}
	//		assert.Nil(t, egp.Wait())
	//	}))
	//})
}
