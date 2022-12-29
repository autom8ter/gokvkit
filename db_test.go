package gokvkit_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/autom8ter/gokvkit"
	"github.com/autom8ter/gokvkit/testutil"
	"github.com/brianvoe/gofakeit/v6"
	"github.com/segmentio/ksuid"
	"github.com/stretchr/testify/assert"
)

func timer() func(t *testing.T) {
	now := time.Now()
	return func(t *testing.T) {
		t.Logf("duration: %s", time.Since(now))
	}
}

func Test(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var (
				id  string
				err error
			)
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				id, err = tx.Create(ctx, "user", testutil.NewUserDoc())
				assert.NoError(t, err)
				_, err := tx.Get(ctx, "user", id)
				return err
			}))
			u, err := db.Get(ctx, "user", id)
			assert.NoError(t, err)
			assert.NotNil(t, u)
			assert.Equal(t, id, u.GetString("_id"))
		}))
	})
	t.Run("create & stream", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wg := sync.WaitGroup{}
			wg.Add(1)
			var received = make(chan struct{}, 1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ch, err := db.ChangeStream(ctx, "user")
				assert.NoError(t, err)
				for {
					select {
					case <-ctx.Done():
						return
					case <-ch:
						received <- struct{}{}
					}
				}
			}()
			var (
				id  string
				err error
			)
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				id, err = tx.Create(ctx, "user", testutil.NewUserDoc())
				_, err := tx.Get(ctx, "user", id)
				return err
			}))
			u, err := db.Get(ctx, "user", id)
			assert.NoError(t, err)
			assert.NotNil(t, u)
			assert.Equal(t, id, u.GetString("_id"))
			<-received
		}))
	})
	t.Run("set", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			timer := timer()
			defer timer(t)
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					assert.Nil(t, tx.Set(ctx, "user", testutil.NewUserDoc()))
				}
				return nil
			}))
		}))
	})
	assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
		var usrs []*gokvkit.Document
		var ids []string
		t.Run("set all", func(t *testing.T) {
			timer := timer()
			defer timer(t)

			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 100; i++ {
					usr := testutil.NewUserDoc()
					ids = append(ids, usr.GetString("_id"))
					usrs = append(usrs, usr)
					assert.Nil(t, tx.Set(ctx, "user", usr))
				}
				return nil
			}))
		})
		t.Run("get each", func(t *testing.T) {
			timer := timer()
			defer timer(t)
			for _, u := range usrs {
				usr, err := db.Get(ctx, "user", u.GetString("_id"))
				if err != nil {
					t.Fatal(err)
				}
				assert.Equal(t, u.String(), usr.String())
			}
		})
		t.Run("query users account_id > 50", func(t *testing.T) {
			timer := timer()
			defer timer(t)
			results, err := db.Query(ctx, "user", gokvkit.Query{
				Select: []gokvkit.Select{{Field: "account_id"}},
				Where: []gokvkit.Where{
					{
						Field: "account_id",
						Op:    gokvkit.WhereOpGt,
						Value: 50,
					},
				},
			})
			assert.NoError(t, err)
			assert.Greater(t, len(results.Documents), 1)
			for _, result := range results.Documents {
				assert.Greater(t, result.GetFloat("account_id"), float64(50))
			}
			t.Logf("found %v documents in %s", results.Count, results.Stats.ExecutionTime)
		})
		t.Run("query users account_id in 51-60", func(t *testing.T) {
			timer := timer()
			defer timer(t)
			results, err := db.Query(ctx, "user", gokvkit.Query{
				Select: []gokvkit.Select{{Field: "account_id"}},
				Where: []gokvkit.Where{
					{
						Field: "account_id",
						Op:    gokvkit.WhereOpIn,
						Value: []string{"51", "52", "53", "54", "55", "56", "57", "58", "59", "60"},
					},
				},
				Limit: 10,
			})
			assert.NoError(t, err)
			assert.Greater(t, len(results.Documents), 1)
			for _, result := range results.Documents {
				assert.Greater(t, result.GetFloat("account_id"), float64(50))
			}
			t.Logf("found %v documents in %s", results.Count, results.Stats.ExecutionTime)
		})
		t.Run("query all", func(t *testing.T) {
			timer := timer()
			defer timer(t)
			results, err := db.Query(ctx, "user", gokvkit.Query{
				Select: []gokvkit.Select{{Field: "*"}},
			})
			assert.NoError(t, err)
			assert.Equal(t, 100, len(results.Documents))
			t.Logf("found %v documents in %s", results.Count, results.Stats.ExecutionTime)
		})
		t.Run("update contact.email", func(t *testing.T) {
			for _, u := range usrs {
				id := u.GetString("_id")
				email := gofakeit.Email()
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					assert.Nil(t, tx.Update(ctx, "user", id, map[string]any{
						"contact.email": email,
					}))
					return nil
				}))
				doc, err := db.Get(ctx, "user", id)
				assert.NoError(t, err)
				assert.Equal(t, email, doc.GetString("contact.email"))
				assert.Equal(t, u.GetString("name"), doc.GetString("name"))
			}
		})
		t.Run("delete first 50", func(t *testing.T) {
			for _, id := range ids[:50] {
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					assert.Nil(t, tx.Delete(ctx, "user", id))
					return nil
				}))

			}
			for _, id := range ids[:50] {
				_, err := db.Get(ctx, "user", id)
				assert.NotNil(t, err)
			}
		})
		t.Run("query delete all", func(t *testing.T) {
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				res, err := db.Query(ctx, "user", gokvkit.Query{
					Select: []gokvkit.Select{{Field: "*"}},
				})
				if err != nil {
					return err
				}
				for _, res := range res.Documents {
					if err := tx.Delete(ctx, "user", res.GetString("_id")); err != nil {
						return err
					}
				}
				return nil
			}))

			for _, id := range ids[50:] {
				d, err := db.Get(ctx, "user", id)
				assert.NotNil(t, err, d)
			}
		})
	}))
	time.Sleep(1 * time.Second)
	t.Log(runtime.NumGoroutine())
}

func Benchmark(b *testing.B) {
	// Benchmark/set-12         	    5662	    330875 ns/op	  288072 B/op	    2191 allocs/op
	b.Run("set", func(b *testing.B) {
		b.ReportAllocs()
		doc := testutil.NewUserDoc()
		assert.Nil(b, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					return tx.Set(ctx, "user", doc)
				}))
			}
		}))
	})
	// Benchmark/get-12         	   52730	     19125 ns/op	   13022 B/op	      98 allocs/op
	b.Run("get", func(b *testing.B) {
		b.ReportAllocs()
		doc := testutil.NewUserDoc()
		assert.Nil(b, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				return tx.Set(ctx, "user", doc)
			}))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := db.Get(ctx, "user", doc.GetString("_id"))
				assert.Nil(b, err)
			}
		}))
	})
	// Benchmark/query-12       	   44590	     25061 ns/op	   18920 B/op	     131 allocs/op
	b.Run("query with index", func(b *testing.B) {
		b.ReportAllocs()
		doc := testutil.NewUserDoc()
		assert.Nil(b, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				return tx.Set(ctx, "user", doc)
			}))
			var docs []*gokvkit.Document
			assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 100000; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				results, err := db.Query(ctx, "user", gokvkit.Query{
					Select: []gokvkit.Select{{Field: "*"}},
					Where: []gokvkit.Where{
						{
							Field: "contact.email",
							Op:    gokvkit.WhereOpEq,
							Value: doc.GetString("contact.email"),
						},
					},
					Limit: 10,
				})
				assert.Nil(b, err)
				assert.Equal(b, 1, len(results.Documents))
				assert.Equal(b, "contact.email", results.Stats.Optimization.MatchedFields[0])
			}
		}))
	})
	// Benchmark/query_without_index-12         	   10780	     98709 ns/op	   49977 B/op	     216 allocs/op
	b.Run("query without index", func(b *testing.B) {
		b.ReportAllocs()
		doc := testutil.NewUserDoc()
		assert.Nil(b, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				return tx.Set(ctx, "user", doc)
			}))
			var docs []*gokvkit.Document
			assert.Nil(b, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 100000; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := db.Query(ctx, "user", gokvkit.Query{
					Select: []gokvkit.Select{{Field: "*"}},
					Where: []gokvkit.Where{
						{
							Field: "name",
							Op:    gokvkit.WhereOpContains,
							Value: doc.GetString("John"),
						},
					},
					Limit: 10,
				})
				assert.Nil(b, err)
			}
		}))
	})
}

func TestIndexing1(t *testing.T) {
	t.Run("matching unique index (contact.email)", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var docs gokvkit.Documents
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 5; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			page, err := db.Query(ctx, "user", gokvkit.Query{
				Select: []gokvkit.Select{
					{
						Field: "contact.email",
					},
				},
				Where: []gokvkit.Where{
					{
						Field: "contact.email",
						Op:    gokvkit.WhereOpEq,
						Value: docs[0].Get("contact.email"),
					},
				},
			})
			assert.NoError(t, err)
			assert.Equal(t, 1, page.Count)
			assert.Equal(t, page.Documents[0].Get("contact.email"), docs[0].Get("contact.email"))
			assert.Equal(t, "contact.email", page.Stats.Optimization.MatchedFields[0])
			assert.Equal(t, false, page.Stats.Optimization.Index.Primary)
		}))
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var docs gokvkit.Documents
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 5; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			page, err := db.Query(ctx, "user", gokvkit.Query{

				Select: []gokvkit.Select{
					{
						Field: "name",
					},
				},
				Where: []gokvkit.Where{
					{
						Field: "contact.email",
						Op:    gokvkit.WhereOpEq,
						Value: docs[0].Get("contact.email"),
					},
				},
			})
			assert.NoError(t, err)
			assert.Equal(t, 1, page.Count)
			assert.Equal(t, "contact.email", page.Stats.Optimization.MatchedFields[0])

			assert.Equal(t, false, page.Stats.Optimization.Index.Primary)
		}))
	})
	t.Run("non-matching (name)", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var docs gokvkit.Documents
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 5; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			page, err := db.Query(ctx, "user", gokvkit.Query{

				Select: []gokvkit.Select{
					{
						Field: "name",
					},
				},
				Where: []gokvkit.Where{
					{
						Field: "name",
						Op:    gokvkit.WhereOpContains,
						Value: docs[0].Get("name"),
					},
				},
			})
			assert.NoError(t, err)
			assert.Equal(t, 1, page.Count)
			assert.Equal(t, page.Documents[0].Get("name"), docs[0].Get("name"))
			assert.Equal(t, []string{}, page.Stats.Optimization.MatchedFields)

			assert.Equal(t, true, page.Stats.Optimization.Index.Primary)
		}))
	})
	t.Run("matching primary (_id)", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var docs gokvkit.Documents
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 5; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			page, err := db.Query(ctx, "user", gokvkit.Query{

				Select: []gokvkit.Select{
					{
						Field: "_id",
					},
				},
				Where: []gokvkit.Where{
					{
						Field: "_id",
						Op:    gokvkit.WhereOpEq,
						Value: docs[0].Get("_id"),
					},
				},
			})
			assert.NoError(t, err)
			assert.Equal(t, 1, page.Count)
			assert.Equal(t, page.Documents[0].Get("_id"), docs[0].Get("_id"))
			assert.Equal(t, []string{"_id"}, page.Stats.Optimization.MatchedFields)

			assert.Equal(t, true, page.Stats.Optimization.Index.Primary)
		}))
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var docs gokvkit.Documents
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 5; i++ {
					usr := testutil.NewUserDoc()
					docs = append(docs, usr)
					if err := tx.Set(ctx, "user", usr); err != nil {
						return err
					}
				}
				return nil
			}))
			page, err := db.Query(ctx, "user", gokvkit.Query{

				Select: []gokvkit.Select{
					{
						Field: "_id",
					},
				},
				Where: []gokvkit.Where{
					{
						Field: "_id",
						Op:    gokvkit.WhereOpContains,
						Value: docs[0].Get("_id"),
					},
				},
			})
			assert.NoError(t, err)
			assert.Equal(t, 1, page.Count)
			assert.Equal(t, page.Documents[0].Get("_id"), docs[0].Get("_id"))
			assert.Equal(t, []string{}, page.Stats.Optimization.MatchedFields)
			assert.Equal(t, true, page.Stats.Optimization.Index.Primary)
		}))
	})
	t.Run("cdc queries", func(t *testing.T) {
		t.Run("no results (>)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					return nil
				}))
				count := 0
				now := time.Now().UnixNano()
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpGt,
						Value: now,
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.Greater(t, d.GetFloat("timestamp"), float64(now))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.EqualValues(t, now, o.SeekValues["timestamp"])
				assert.False(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.Equal(t, 0, count)
			}))
		})
		t.Run("all results (>)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					return nil
				}))
				count := 0
				now := time.Now().Truncate(5 * time.Minute).UnixNano()
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpGt,
						Value: now,
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.Greater(t, d.GetFloat("timestamp"), float64(now))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.EqualValues(t, now, o.SeekValues["timestamp"])
				assert.False(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.NotEqual(t, 0, count)
			}))
		})
		t.Run("all results (<)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					return nil
				}))
				count := 0
				now := time.Now().Add(5 * time.Minute).UnixNano()
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpLt,
						Value: now,
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.Less(t, d.GetFloat("timestamp"), float64(now))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.EqualValues(t, now, o.SeekValues["timestamp"])
				assert.True(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.NotEqual(t, 0, count)
			}))
		})
		t.Run("some results (<=)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				var ts time.Time
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					ts = time.Unix(0, tx.CDC()[0].Timestamp)
					return nil
				}))
				count := 0
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpLte,
						Value: ts.UnixNano(),
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.LessOrEqual(t, d.GetFloat("timestamp"), float64(ts.UnixNano()))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.True(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.NotEqual(t, 0, count)
			}))
		})
		t.Run("no results (>)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					return nil
				}))
				count := 0
				now := time.Now().UnixNano()
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpGt,
						Value: now,
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.Greater(t, d.GetFloat("timestamp"), float64(now))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.EqualValues(t, now, o.SeekValues["timestamp"])
				assert.False(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.Equal(t, 0, count)
			}))
		})
		t.Run("no results (<)", func(t *testing.T) {
			assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
				var docs gokvkit.Documents
				assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
					for i := 0; i < 5; i++ {
						usr := testutil.NewUserDoc()
						docs = append(docs, usr)
						if err := tx.Set(ctx, "user", usr); err != nil {
							return err
						}
					}
					return nil
				}))
				count := 0
				now := time.Now().Truncate(15 * time.Minute).UnixNano()
				o, err := db.ForEach(ctx, "cdc", gokvkit.ForEachOpts{
					Where: []gokvkit.Where{{
						Field: "timestamp",
						Op:    gokvkit.WhereOpLt,
						Value: now,
					}},
				}, func(d *gokvkit.Document) (bool, error) {
					assert.Less(t, d.GetFloat("timestamp"), float64(now))
					count++
					return true, nil
				})
				assert.NoError(t, err)
				assert.Equal(t, false, o.Index.Primary)
				assert.EqualValues(t, now, o.SeekValues["timestamp"])
				assert.True(t, o.Reverse)
				assert.Equal(t, "timestamp", o.SeekFields[0])
				assert.Equal(t, 0, count)
			}))
		})
	})

}

func TestOrderBy(t *testing.T) {
	t.Run("basic asc/desc", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs []*gokvkit.Document
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					u := testutil.NewUserDoc()
					assert.NoError(t, u.Set("age", i))
					usrs = append(usrs, u)
					assert.NoError(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			{
				results, err := db.Query(ctx, "user", gokvkit.Q().
					Select(gokvkit.Select{Field: "*"}).
					OrderBy(gokvkit.OrderBy{Field: "age", Direction: gokvkit.OrderByDirectionAsc}).
					Query())
				assert.NoError(t, err)
				for i, d := range results.Documents {
					assert.Equal(t, usrs[i].Get("age"), d.Get("age"))
				}
			}
			{
				results, err := db.Query(ctx, "user", gokvkit.Q().
					Select(gokvkit.Select{Field: "*"}).
					OrderBy(gokvkit.OrderBy{Field: "age", Direction: gokvkit.OrderByDirectionDesc}).
					Query())
				assert.NoError(t, err)
				for i, d := range results.Documents {
					assert.Equal(t, usrs[len(usrs)-i-1].Get("age"), d.Get("age"))
				}
			}
		}))
	})
}

func TestPagination(t *testing.T) {
	t.Run("order by asc + pagination", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs []*gokvkit.Document
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					u := testutil.NewUserDoc()
					assert.NoError(t, u.Set("age", i))
					usrs = append(usrs, u)
					assert.NoError(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			for i := 0; i < 10; i++ {
				results, err := db.Query(ctx, "user", gokvkit.Q().
					OrderBy(gokvkit.OrderBy{Field: "age", Direction: gokvkit.OrderByDirectionAsc}).
					Page(i).
					Limit(1).
					Query())
				assert.NoError(t, err)
				assert.Equal(t, 1, results.Count)
				assert.Equal(t, usrs[i].Get("age"), results.Documents[0].Get("age"))
			}
		}))
	})
	t.Run("order by desc + pagination", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs []*gokvkit.Document
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					u := testutil.NewUserDoc()
					assert.NoError(t, u.Set("age", i))
					usrs = append(usrs, u)
					assert.NoError(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			for i := 0; i < 10; i++ {
				results, err := db.Query(ctx, "user", gokvkit.Q().
					OrderBy(gokvkit.OrderBy{Field: "age", Direction: gokvkit.OrderByDirectionDesc}).
					Page(i).
					Limit(1).
					Query())
				assert.NoError(t, err)
				assert.Equal(t, 1, results.Count)
				assert.Equal(t, usrs[len(usrs)-i-1].Get("age"), results.Documents[0].Get("age"))
			}
		}))
	})
	t.Run("order by desc + where + pagination", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs []*gokvkit.Document
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					u := testutil.NewUserDoc()
					assert.NoError(t, u.Set("age", i))
					usrs = append(usrs, u)
					assert.NoError(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			for i := 0; i < 10; i++ {
				results, err := db.Query(ctx, "user", gokvkit.Q().
					Where(gokvkit.Where{Field: "age", Op: gokvkit.WhereOpGte, Value: 5}).
					OrderBy(gokvkit.OrderBy{Field: "age", Direction: gokvkit.OrderByDirectionDesc}).
					Page(i).
					Limit(1).
					Query())
				assert.NoError(t, err)
				if i < 5 {
					assert.Equal(t, 1, results.Count)
					assert.Equal(t, usrs[len(usrs)-i-1].Get("age"), results.Documents[0].Get("age"))
				}
			}
		}))
	})
}

func TestAggregate(t *testing.T) {
	t.Run("sum advanced", func(t *testing.T) {
		assert.Nil(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs gokvkit.Documents
			ageSum := map[string]float64{}
			assert.Nil(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 10; i++ {
					u := testutil.NewUserDoc()
					ageSum[u.GetString("account_id")] += u.GetFloat("age")
					usrs = append(usrs, u)
					assert.Nil(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			query := gokvkit.Query{
				GroupBy: []string{"account_id"},
				//Where:      []schema.Where{
				//	{
				//
				//	},
				//},
				Select: []gokvkit.Select{
					{
						Field: "account_id",
					},
					{
						Field:     "age",
						Aggregate: gokvkit.AggregateFunctionSum,
						As:        "age_sum",
					},
				},
				OrderBy: []gokvkit.OrderBy{
					{
						Field:     "account_id",
						Direction: gokvkit.OrderByDirectionAsc,
					},
				},
			}
			results, err := db.Query(ctx, "user", query)
			if err != nil {
				t.Fatal(err)
			}
			assert.NotEqual(t, 0, results.Count)
			var accounts []string
			for _, result := range results.Documents {
				accounts = append(accounts, result.GetString("account_id"))
				assert.Equal(t, ageSum[result.GetString("account_id")], result.GetFloat("age_sum"))
			}
		}))
	})
}

func TestScript(t *testing.T) {
	t.Run("getAccount", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			getAccountScript := `
function getAccount(ctx, db, params) {
	let res = db.get(ctx, 'account', params.id)
	return res.get('_id')
}
 `
			results, err := db.RunScript(ctx, "getAccount", getAccountScript, map[string]any{
				"id": "1",
			})
			assert.NoError(t, err)
			fmt.Printf("%T %#v", results, results)
			assert.Equal(t, "1", results)
		}))
	})
	t.Run("getAccounts", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			getAccountScript := `
function getAccounts(ctx, db, params) {
	let res = db.query(ctx, 'account', {select: [{field: '*'}]})
	return res.documents
}
 `
			results, err := db.RunScript(ctx, "getAccounts", getAccountScript, map[string]any{})
			assert.NoError(t, err)
			fmt.Printf("%T %#v", results, results)
			assert.Equal(t, 101, len(results.(gokvkit.Documents)))
		}))
	})
	t.Run("setAccount", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			getAccountScript := `
function setAccount(ctx, db, params) {
	db.tx(ctx, true, (ctx, tx) => {
		tx.set(ctx, "account", params.doc)
	})
}
 `
			id := ksuid.New().String()
			doc, err := gokvkit.NewDocumentFrom(map[string]any{
				"_id":  id,
				"name": gofakeit.Company(),
			})
			_, err = db.RunScript(ctx, "setAccount", getAccountScript, map[string]any{
				"doc": doc,
			})
			assert.NoError(t, err)
			val, err := db.Get(ctx, "account", id)
			assert.NoError(t, err)
			assert.NotEmpty(t, val)
		}))
	})
	t.Run("forEachAccount", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			getAccountScript := `
function forEachAccount(ctx, db, params) {
	db.forEach(ctx, 'account', undefined, params.fn)
}
 `
			count := 0
			_, err := db.RunScript(ctx, "forEachAccount", getAccountScript, map[string]any{
				"fn": gokvkit.ForEachFunc(func(d *gokvkit.Document) (bool, error) {
					count++
					return true, nil
				}),
			})
			assert.NoError(t, err)
			assert.Equal(t, 101, count)
		}))
	})
}

func TestJoin(t *testing.T) {
	t.Run("join user to account", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			var usrs = map[string]*gokvkit.Document{}
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i < 100; i++ {
					u := testutil.NewUserDoc()
					usrs[u.GetString("_id")] = u
					assert.NoError(t, tx.Set(ctx, "user", u))
				}
				return nil
			}))
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				assert.NoError(t, tx.Set(ctx, "user", testutil.NewUserDoc()))
				return nil
			}))
			results, err := db.Query(ctx, "user", gokvkit.Q().
				Select(
					gokvkit.Select{Field: "acc._id", As: "account_id"},
					gokvkit.Select{Field: "acc.name", As: "account_name"},
					gokvkit.Select{Field: "_id", As: "user_id"},
				).
				Join(gokvkit.Join{
					Collection: "account",
					On: []gokvkit.Where{
						{
							Field: "_id",
							Op:    gokvkit.WhereOpEq,
							Value: "$account_id",
						},
					},
					As: "acc",
				}).
				Query())
			assert.NoError(t, err)

			for _, r := range results.Documents {
				assert.True(t, r.Exists("account_name"))
				assert.True(t, r.Exists("account_id"))
				assert.True(t, r.Exists("user_id"))
				if usrs[r.GetString("user_id")] != nil {
					assert.NotEmpty(t, usrs[r.GetString("user_id")])
					assert.Equal(t, usrs[r.GetString("user_id")].Get("account_id"), r.GetString("account_id"))
				}
			}
		}))
	})
	t.Run("join account to user", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			accID := ""
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				doc := testutil.NewUserDoc()
				accID = doc.GetString("account_id")
				doc2 := testutil.NewUserDoc()
				assert.Nil(t, doc2.Set("account_id", accID))
				assert.NoError(t, tx.Set(ctx, "user", doc))
				assert.NoError(t, tx.Set(ctx, "user", doc2))
				return nil
			}))
			results, err := db.Query(ctx, "account", gokvkit.Q().
				Select(
					gokvkit.Select{Field: "_id", As: "account_id"},
					gokvkit.Select{Field: "name", As: "account_name"},
					gokvkit.Select{Field: "usr.name"},
				).
				Where(
					gokvkit.Where{
						Field: "_id",
						Op:    gokvkit.WhereOpEq,
						Value: accID,
					},
				).
				Join(gokvkit.Join{
					Collection: "user",
					On: []gokvkit.Where{
						{
							Field: "account_id",
							Op:    gokvkit.WhereOpEq,
							Value: "$_id",
						},
					},
					As: "usr",
				}).
				OrderBy(gokvkit.OrderBy{Field: "account_name", Direction: gokvkit.OrderByDirectionAsc}).
				Query())
			assert.NoError(t, err)

			for _, r := range results.Documents {
				assert.True(t, r.Exists("account_name"))
				assert.True(t, r.Exists("account_id"))
				assert.True(t, r.Exists("usr"))
			}
			assert.Equal(t, 2, results.Count)
		}))
	})
	t.Run("cascade delete", func(t *testing.T) {
		assert.NoError(t, testutil.TestDB(func(ctx context.Context, db gokvkit.Database) {
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i <= 100; i++ {
					u := testutil.NewUserDoc()
					if err := tx.Set(ctx, "user", u); err != nil {
						return err
					}
					tsk := testutil.NewTaskDoc(u.GetString("_id"))
					if err := tx.Set(ctx, "task", tsk); err != nil {
						return err
					}
				}
				return nil
			}))
			assert.NoError(t, db.Tx(ctx, true, func(ctx context.Context, tx gokvkit.Tx) error {
				for i := 0; i <= 100; i++ {
					if err := tx.Delete(ctx, "account", fmt.Sprint(i)); err != nil {
						return err
					}
				}
				return nil
			}))
			results, err := db.Query(ctx, "account", gokvkit.Query{Select: []gokvkit.Select{{Field: "*"}}})
			assert.NoError(t, err)
			assert.Equal(t, 0, results.Count, "failed to delete accounts")
			results, err = db.Query(ctx, "user", gokvkit.Query{Select: []gokvkit.Select{{Field: "*"}}})
			assert.NoError(t, err)
			assert.Equal(t, 0, results.Count, "failed to cascade delete users")
			results, err = db.Query(ctx, "task", gokvkit.Query{Select: []gokvkit.Select{{Field: "*"}}})
			assert.NoError(t, err)
			assert.Equal(t, 0, results.Count, "failed to cascade delete tasks")
		}))
	})
}
