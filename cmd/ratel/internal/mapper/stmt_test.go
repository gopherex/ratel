package mapper_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestMapStatements(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct{ sql, kind string }{
		{"SELECT DISTINCT ON (a) a FROM app.t LEFT JOIN app.u USING (id) WHERE a > $1" +
			" GROUP BY a HAVING count(*) > 0 ORDER BY a DESC NULLS FIRST LIMIT $2 OFFSET $3", "select"},
		{"SELECT * FROM t CROSS JOIN LATERAL (SELECT 1) s", "select"},
		{"SELECT * FROM unnest($1::int[]) WITH ORDINALITY AS u(x,n)", "select"},
		{"SELECT 1 UNION ALL SELECT 2 INTERSECT SELECT 3", "select"},
		{"VALUES (1,2), (3,4)", "select"},
		{"SELECT * FROM t FOR UPDATE OF t NOWAIT", "select"},
		{"INSERT INTO t(a) VALUES ($1) ON CONFLICT (a) WHERE a > 0 DO UPDATE SET a = $2 WHERE t.a > 0 RETURNING *", "insert"},
		{"INSERT INTO t DEFAULT VALUES", "insert"},
		{"INSERT INTO t SELECT * FROM u", "insert"},
		{"UPDATE ONLY t SET (a,b) = (SELECT 1,2) FROM u WHERE t.id=u.id RETURNING t.*", "update"},
		{"DELETE FROM ONLY t USING u WHERE t.id=u.id RETURNING *", "delete"},
		{"MERGE INTO t USING s ON t.id=s.id WHEN MATCHED THEN UPDATE SET a=s.a" +
			" WHEN NOT MATCHED THEN INSERT(id,a) VALUES(s.id,s.a)", "merge"},
		{"TRUNCATE t", "raw"},
		{"BEGIN", "raw"},
		{"CREATE TABLE t (id int)", "raw"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("stmt.sql", tc.sql, raw, 0)
			got := current.MapStatement(raw.GetStmt())
			reflected := got.ProtoReflect()

			if string(reflected.WhichOneof(reflected.Descriptor().Oneofs().Get(0)).Name()) != tc.kind {
				t.Fatal(got)
			}

			if tc.kind == "select" && got.GetSelect().GetRawSql() == "" {
				t.Fatal("missing SQL")
			}

			if tc.kind == "update" && len(got.GetUpdate().GetSet()[0].GetColumns()) != 2 {
				t.Fatal("multi-column assignment split", got)
			}

			if tc.kind == "merge" && len(got.GetMerge().GetWhens()) != 2 {
				t.Fatal(got)
			}

			if tc.kind == "insert" && got.GetInsert().GetOnConflict() != nil &&
				got.GetInsert().GetOnConflict().GetIndexPredicate() == nil {
				t.Fatal("index predicate lost")
			}
		})
	}
}

func BenchmarkStatements(b *testing.B) {
	for _, sql := range []string{
		"INSERT INTO t VALUES ($1) RETURNING *", "UPDATE t SET a=$1 WHERE id=$2",
		"DELETE FROM t WHERE id=$1", "MERGE INTO t USING s ON t.id=s.id WHEN MATCHED THEN DELETE",
	} {
		b.Run(sql, func(b *testing.B) {
			tree, err := parse.Parse(sql)
			if err != nil {
				b.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("bench.sql", sql, raw, 0)

			for b.Loop() {
				current.MapStatement(raw.GetStmt())
			}
		})
	}
}
