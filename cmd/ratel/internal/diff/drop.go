package diff

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// Dropped is the change removing a rendered object the desired state lacks.
func Dropped(object introspect.Object) Change {
	kind, name := kindOf(object.Key), nameOf(object.Key)
	change := Change{Key: object.Key, Before: object.SQL}

	switch kind {
	case schemaObject:
		change.Kind, change.SQL = DropSchema, "DROP SCHEMA "+name+";"
	case extensionObject:
		change.Kind, change.SQL = DropExtension, "DROP EXTENSION "+name+";"
	case typeObject:
		change.Kind, change.SQL = DropType, "DROP "+typeKeyword(object.SQL)+" "+name+";"
	case sequenceObject:
		change.Kind, change.SQL = DropSequence, "DROP SEQUENCE "+name+";"
	case routineObject:
		change.Kind, change.SQL = DropRoutine, "DROP "+routineKeyword(object.SQL)+" "+name+";"
	case tableObject:
		return droppedTable(change, name, object.SQL)
	case constraintObject:
		change.Kind, change.Table = DropConstraint, parentOf(name)
		change.SQL = "ALTER TABLE " + change.Table + " DROP CONSTRAINT " + child(name) + ";"
	case indexObject:
		change.Kind, change.Table = DropIndex, indexTable(object.SQL)
		change.SQL = "DROP INDEX " + name + ";"
	case viewObject:
		change.Kind, change.SQL = DropView, "DROP "+viewKeyword(object.SQL)+" "+name+";"
	case triggerObject:
		change.Kind, change.Table = DropTrigger, parentOf(name)
		change.SQL = "DROP TRIGGER " + child(name) + " ON " + change.Table + ";"
	case policyObject:
		change.Kind, change.Table = DropPolicy, parentOf(name)
		change.SQL = "DROP POLICY " + child(name) + " ON " + change.Table + ";"
	case rlsObject:
		change.Kind, change.Table = DisableRLS, name
		change.SQL = "ALTER TABLE " + name + " DISABLE ROW LEVEL SECURITY, NO FORCE ROW LEVEL SECURITY;"
	case grantObject:
		change.Kind, change.SQL = Revoke, revoke(object.SQL)
	default:
		change.Kind, change.SQL = Comment, uncomment(object.Key)
	}

	return change
}

func droppedTable(change Change, name, sql string) Change {
	change.Table = name

	if parent, ok := partitionParent(sql); ok {
		change.Kind = DetachPartition
		change.Table = parent
		change.SQL = "ALTER TABLE " + parent + " DETACH PARTITION " + name + ";\nDROP TABLE " + name + ";"

		return change
	}

	change.Kind, change.SQL = DropTable, "DROP TABLE "+name+";"

	return change
}

// partitionParent is the parent of a CREATE TABLE ... PARTITION OF statement.
func partitionParent(sql string) (string, bool) {
	_, rest, ok := strings.Cut(sql, " PARTITION OF ")
	if !ok {
		return "", false
	}

	parent, _, _ := strings.Cut(rest, " ")

	return parent, true
}

// child is the last part of a "schema.table.child" name.
func child(name string) string {
	return name[len(parentOf(name))+1:]
}

func typeKeyword(sql string) string {
	if strings.HasPrefix(sql, "CREATE DOMAIN ") {
		return "DOMAIN"
	}

	return "TYPE"
}

func routineKeyword(sql string) string {
	if strings.HasPrefix(sql, "CREATE OR REPLACE PROCEDURE ") {
		return "PROCEDURE"
	}

	return "FUNCTION"
}

func materialized(sql string) bool {
	return strings.HasPrefix(sql, "CREATE MATERIALIZED VIEW ")
}

func viewKeyword(sql string) string {
	if materialized(sql) {
		return "MATERIALIZED VIEW"
	}

	return "VIEW"
}

// revoke reverses a rendered GRANT ... TO grantee [WITH GRANT OPTION].
func revoke(grant string) string {
	statement := strings.TrimSuffix(strings.TrimSuffix(grant, ";"), " WITH GRANT OPTION")
	to := strings.LastIndex(statement, " TO ")

	return "REVOKE" + strings.TrimPrefix(statement[:to], "GRANT") + " FROM " + statement[to+len(" TO "):] + ";"
}

// uncomment clears the comment of a "comment <object type> <identity>" key.
func uncomment(key string) string {
	rest := nameOf(key)

	for _, kind := range []string{"table column", "table constraint", "materialized view"} {
		if identity, ok := strings.CutPrefix(rest, kind+" "); ok {
			return "COMMENT ON " + commentKeyword(kind) + " " + identity + " IS NULL;"
		}
	}

	kind, identity, _ := strings.Cut(rest, " ")

	return "COMMENT ON " + commentKeyword(kind) + " " + identity + " IS NULL;"
}

// commentKeyword is the COMMENT ON keyword of a pg_identify_object type.
func commentKeyword(kind string) string {
	switch kind {
	case "table column":
		return "COLUMN"
	case "table constraint":
		return "CONSTRAINT"
	default:
		return strings.ToUpper(kind)
	}
}
