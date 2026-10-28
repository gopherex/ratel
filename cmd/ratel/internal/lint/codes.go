package lint

// detail is the finding text of a code, as docs/migrations.md lists it.
func detail(code string) string {
	if text, ok := lockDetail(code); ok {
		return text
	}

	if text, ok := changeDetail(code); ok {
		return text
	}

	return structureDetail(code)
}

func lockDetail(code string) (string, bool) {
	if text, ok := rewriteDetail(code); ok {
		return text, true
	}

	switch code {
	case "RTL-L01":
		return "blocking lock on an existing table without lock-retry guard", true
	case "RTL-L02":
		return "non-concurrent index build on an existing table", true
	case "RTL-L03":
		return "non-concurrent index drop", true
	case "RTL-L04":
		return "validation scan under a blocking lock (check, FK)", true
	case "RTL-L05":
		return "unique or primary key built under an exclusive lock", true
	case "RTL-L06":
		return "SET NOT NULL scan", true
	case "RTL-L07":
		return "ATTACH PARTITION scan under an exclusive lock", true
	case "RTL-L08":
		return "blocking DETACH PARTITION", true
	case "RTL-L09":
		return "exclusion constraint on an existing table", true
	case "RTL-L10":
		return "non-concurrent REINDEX", true
	default:
		return "", false
	}
}

func rewriteDetail(code string) (string, bool) {
	switch code {
	case "RTL-R01":
		return "rewrite from a volatile default, serial, identity or stored generated column", true
	case "RTL-R02":
		return "type change rewrites the table", true
	case "RTL-R03":
		return "rewrite by SET LOGGED, access method, tablespace, CLUSTER, VACUUM FULL", true
	case "RTL-R04":
		return "type or collation change rebuilds indexes", true
	default:
		return "", false
	}
}

func changeDetail(code string) (string, bool) {
	switch code {
	case "RTL-D01":
		return "drops a table, column or schema", true
	case "RTL-D02":
		return "TRUNCATE", true
	case "RTL-D03":
		return "narrowing type change may lose data", true
	case "RTL-D04":
		return "drops an index, constraint or default; hidden CASCADE dependents", true
	case "RTL-D05":
		return "removes an enum value", true
	case "RTL-C01":
		return "rename visible to running code", true
	case "RTL-C02":
		return "drop of an object running code may still use", true
	case "RTL-C03":
		return "new NOT NULL without default", true
	case "RTL-C04":
		return "type change visible to prepared statements and scanners", true
	case "RTL-C05":
		return "new enum value reaches old clients", true
	case "RTL-C06":
		return "constraint tightening", true
	case "RTL-C07":
		return "dropped default or dropped NOT NULL", true
	case "RTL-C08":
		return "ENABLE ROW LEVEL SECURITY before policies", true
	default:
		return "", false
	}
}

func structureDetail(code string) string {
	switch code {
	case "RTL-T01":
		return "statement that cannot run in a transaction inside one"
	case "RTL-T02":
		return "new enum value used in the same transaction"
	case "RTL-T03":
		return "slow work after an exclusive lock in the same transaction"
	case "RTL-T04":
		return "hand-written step overriding the runner's lock_timeout or statement_timeout"
	case "RTL-T05":
		return "exclusive locks on several tables in one transaction"
	case "RTL-T06":
		return "data change outside a backfill step"
	case "RTL-P01":
		return "FK columns without a supporting index"
	case "RTL-P02":
		return "int4 primary key, serial, timestamp without time zone, char(n)"
	default:
		return "rule " + code
	}
}
