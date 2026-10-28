//go:build integration

package tests_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel"
)

const (
	benchManyRows = 100
	benchCopyRows = 1000
	benchBatchRow = 10
)

type benchKeyed struct {
	ratel.KeyedTable[writeRecord, recordInsert]
}

// benchKeyedDescriptor is a primary-keyed view of write_app.records for Save.
func benchKeyedDescriptor() benchKeyed {
	table := ratel.NewTable("write_app", "records", []string{"id", "email", "score"},
		func(row pgx.Row) (writeRecord, error) {
			var record writeRecord
			err := row.Scan(&record.ID, &record.Email, &record.Score)

			return record, err
		},
		ratel.PrimaryKey([]string{"id"}, func(record writeRecord) []any { return []any{record.ID} }),
		ratel.ModelValues(func(record writeRecord) []any { return []any{record.ID, record.Email, record.Score} }))

	return benchKeyed{KeyedTable: ratel.NewKeyedTable(ratel.NewWriteTable(table,
		func(row recordInsert) bool { return row.valid },
		ratel.InsertField[recordInsert]{
			Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
			Value: func(row recordInsert) (any, bool) { return row.email, true },
			Values: func(rows []recordInsert) any {
				return recordArray(rows, func(row recordInsert) string { return row.email })
			},
		},
		ratel.InsertField[recordInsert]{
			Name: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
			Value: func(row recordInsert) (any, bool) { return row.ID.V, row.ID.Set },
			Values: func(rows []recordInsert) any {
				return recordArray(rows, func(row recordInsert) int64 { return row.ID.V })
			},
		},
		ratel.InsertField[recordInsert]{
			Name: "score", Type: pgx.Identifier{"pg_catalog", "int8"},
			Value: func(row recordInsert) (any, bool) { return row.Score.V, row.Score.Set },
			Values: func(rows []recordInsert) any {
				return recordArray(rows, func(row recordInsert) int64 { return row.Score.V })
			},
		}))}
}

func benchEmails(prefix string, count int) []string {
	emails := make([]string, count)
	for index := range emails {
		emails[index] = fmt.Sprintf("%s-%d", prefix, index)
	}

	return emails
}

func benchInserts(emails []string) []recordInsert {
	rows := make([]recordInsert, len(emails))
	for index, email := range emails {
		rows[index] = newRecordInsert(email)
	}

	return rows
}

// benchReset empties the table outside the timed section.
func benchReset(b *testing.B, pool *pgxpool.Pool) {
	b.Helper()
	b.StopTimer()

	if _, err := pool.Exec(b.Context(), "TRUNCATE write_app.records RESTART IDENTITY"); err != nil {
		b.Fatal(err)
	}

	b.StartTimer()
}

// benchSeed inserts rows outside the timed section and returns their ids.
func benchSeed(b *testing.B, pool *pgxpool.Pool, emails []string) []int64 {
	b.Helper()
	b.StopTimer()

	records, err := ratel.New(pool).Insert(recordDescriptor(), benchInserts(emails)...).Returning(b.Context())
	if err != nil {
		b.Fatal(err)
	}

	b.StartTimer()

	ids := make([]int64, len(records))
	for index := range records {
		ids[index] = records[index].ID
	}

	return ids
}

func benchCheck(b *testing.B, err error) {
	b.Helper()

	if err != nil {
		b.Fatal(err)
	}
}

func BenchmarkWrite(b *testing.B) {
	pool := writePool(b)
	records := recordDescriptor()
	database := ratel.New(pool)
	one := benchInserts(benchEmails("one", 1))
	many := benchInserts(benchEmails("many", benchManyRows))
	copied := benchInserts(benchEmails("copy", benchCopyRows))

	for _, tc := range []struct {
		name string
		run  func(*testing.B)
	}{
		{"insert one", func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				benchReset(b, pool)

				_, err := database.Insert(records, one...).Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"insert many", func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				benchReset(b, pool)

				_, err := database.Insert(records, many...).Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"insert many returning", func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				benchReset(b, pool)

				_, err := database.Insert(records, many...).Returning(b.Context())
				benchCheck(b, err)
			}
		}},
		{"update", func(b *testing.B) {
			b.Helper()

			benchReset(b, pool)

			row := benchSeed(b, pool, []string{"update"})[0]

			var score int64

			for b.Loop() {
				score++
				_, err := database.Update(records).Set(records.Score.To(score)).Where(records.ID.Eq(row)).Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"upsert update", func(b *testing.B) {
			b.Helper()

			benchReset(b, pool)
			benchSeed(b, pool, benchEmails("many", benchManyRows))

			for b.Loop() {
				_, err := database.Upsert(records, many...).OnConflict(records.Email).Update(records.Score).
					Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"upsert do nothing", func(b *testing.B) {
			b.Helper()

			benchReset(b, pool)
			benchSeed(b, pool, benchEmails("many", benchManyRows))

			for b.Loop() {
				_, err := database.Upsert(records, many...).OnConflict(records.Email).DoNothing().Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"delete", func(b *testing.B) {
			b.Helper()

			benchReset(b, pool)

			for b.Loop() {
				id := benchSeed(b, pool, []string{"delete"})[0]
				_, err := database.Delete(records).Where(records.ID.Eq(id)).Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"save", func(b *testing.B) {
			b.Helper()

			keyed := benchKeyedDescriptor()

			benchReset(b, pool)

			model := writeRecord{ID: benchSeed(b, pool, []string{"save"})[0], Email: "save", Score: 1}

			for b.Loop() {
				model.Score++
				_, err := database.Save(keyed, model).Exec(b.Context())
				benchCheck(b, err)
			}
		}},
		{"batch insert update delete", func(b *testing.B) {
			b.Helper()

			var inserted, updated, deleted int64

			for b.Loop() {
				benchReset(b, pool)

				id := benchSeed(b, pool, []string{"batch-seed"})[0]
				err := database.Batch(b.Context(),
					database.Insert(records, many[:benchBatchRow]...).Batch(&inserted),
					database.Update(records).Set(records.Score.To(int64(2))).Where(records.ID.Eq(id)).Batch(&updated),
					database.Delete(records).Where(records.ID.Eq(id)).Batch(&deleted))
				benchCheck(b, err)
			}
		}},
		{"copy from", func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				benchReset(b, pool)

				_, err := database.CopyFrom(b.Context(), records, slices.Values(copied))
				benchCheck(b, err)
			}
		}},
		{"transaction insert many", func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				benchReset(b, pool)

				transaction, err := pool.Begin(b.Context())
				benchCheck(b, err)

				_, err = ratel.New(transaction).Insert(records, many...).Exec(b.Context())
				benchCheck(b, err)
				benchCheck(b, transaction.Commit(b.Context()))
			}
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			tc.run(b)
		})
	}
}
