package gengo

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
)

// maxPositional is the most parameters a query function takes positionally;
// more become a <Query>Params struct.
const maxPositional = 3

// parameter is a query parameter as a Go argument.
type parameter struct {
	name   string // Go parameter name
	field  string // Params struct field
	goType string
}

// queryFiles renders one package per query group.
func (s *state) queryFiles(queries []*ir.Query) ([]*pluginpb.File, error) {
	groups := make(map[string][]*ir.Query)

	for _, value := range queries {
		group := s.packages[value.GetName()]
		if group == "" {
			return nil, &plugin.Error{
				Code: "RTL-G06", Message: "query " + value.GetName() + " has no package",
				Source: value.GetMeta().GetSource(),
			}
		}

		groups[group] = append(groups[group], value)
	}

	files := make([]*pluginpb.File, 0, len(groups))

	for _, group := range slices.Sorted(mapKeys(groups)) {
		file := &unit{schema: "", names: make(map[string]string), registry: newRegistry()}

		for _, value := range groups[group] {
			if err := s.query(file, value); err != nil {
				return nil, err
			}
		}

		if err := file.declare("RegisterTypes", "query package "+group); err != nil {
			return nil, err
		}

		writeRegisterTypes(file)

		name := packageName(group)

		contents, err := source(name, file.imports, file.body.String())
		if err != nil {
			return nil, err
		}

		files = append(files, &pluginpb.File{Path: name + "/" + name + ".go", Contents: contents})
	}

	return files, nil
}

func mapKeys[V any](values map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range values {
			if !yield(key) {
				return
			}
		}
	}
}

func (s *state) parameters(file *unit, value *ir.Query) ([]parameter, error) {
	out := make([]parameter, 0, len(value.GetParameters()))

	for _, item := range value.GetParameters() {
		mapped, err := s.goType(item.GetType(), "")
		if err != nil {
			return nil, err
		}

		name := item.GetName()
		if name == "" {
			name = "arg" + strconv.Itoa(int(item.GetNumber()))
		}

		s.use(file, item.GetType(), mapped, item.GetNullable())

		goType := file.use(mapped)

		if item.GetNullable() {
			file.imports = append(file.imports, ratelImport)
			goType = "ratel.Null[" + goType + "]"
		}

		out = append(out, parameter{name: parameterName(name), field: pascal(name), goType: goType})
	}

	return out, nil
}

// callArguments passes a query function's own parameters on.
func callArguments(parameters []parameter) string {
	if len(parameters) > maxPositional {
		return "params"
	}

	names := make([]string, 0, len(parameters))
	for _, value := range parameters {
		names = append(names, value.name)
	}

	return strings.Join(names, ", ")
}

// declaration is the parameter list a query function takes after db.
func declaration(name string, parameters []parameter) string {
	if len(parameters) > maxPositional {
		return ", params " + name + "Params"
	}

	declared := make([]string, 0, len(parameters))
	for _, value := range parameters {
		declared = append(declared, value.name+" "+value.goType)
	}

	if len(declared) == 0 {
		return ""
	}

	return ", " + strings.Join(declared, ", ")
}

// arguments are the query's SQL arguments in parameter order.
func arguments(parameters []parameter) string {
	passed := make([]string, 0, len(parameters))

	for _, value := range parameters {
		if len(parameters) > maxPositional {
			passed = append(passed, "params."+value.field)
		} else {
			passed = append(passed, value.name)
		}
	}

	return strings.Join(passed, ", ")
}

func writeParams(file *unit, name string, parameters []parameter) {
	file.printf("// %sParams are the parameters of %s.\ntype %sParams struct {\n", name, name, name)

	for _, value := range parameters {
		file.printf("\t%s %s\n", value.field, value.goType)
	}

	file.printf("}\n\n")
}

func (s *state) query(file *unit, value *ir.Query) error {
	name := value.GetName()

	parameters, err := s.parameters(file, value)
	if err != nil {
		return err
	}

	if len(parameters) > maxPositional || value.GetCommand() == ir.QueryCommand_QUERY_COMMAND_COPY_FROM {
		if err := file.declare(name+"Params", "query "+name); err != nil {
			return err
		}

		writeParams(file, name, parameters)
	}

	if err := file.declare(name, "query "+name); err != nil {
		return err
	}

	file.imports = append(file.imports, "context", ratelImport)

	switch value.GetCommand() {
	case ir.QueryCommand_QUERY_COMMAND_EXEC, ir.QueryCommand_QUERY_COMMAND_EXEC_ROWS:
		writeExec(file, value, parameters)

		return nil
	case ir.QueryCommand_QUERY_COMMAND_COPY_FROM:
		return s.writeCopy(file, value, parameters)
	default:
		return s.writeRead(file, value, parameters)
	}
}

func writeExec(file *unit, value *ir.Query, parameters []parameter) {
	name := value.GetName()
	declared, passed := declaration(name, parameters), arguments(parameters)

	statement := "ratel.Statement{SQL: " + strconv.Quote(value.GetSql()) + ", Args: []any{" + passed + "}}"
	if value.GetCommand() == ir.QueryCommand_QUERY_COMMAND_EXEC_ROWS {
		file.printf("// %s runs the query and returns the number of affected rows.\n"+
			"func %s(ctx context.Context, db *ratel.DB%s) (int64, error) {\n\treturn db.Exec(ctx, %s)\n}\n\n",
			name, name, declared, statement)

		return
	}

	file.printf("// %s runs the query.\nfunc %s(ctx context.Context, db *ratel.DB%s) error {\n"+
		"\t_, err := db.Exec(ctx, %s)\n\n\treturn err\n}\n\n", name, name, declared, statement)
}

// writeCopy streams parameter rows into the INSERT target through COPY.
func (s *state) writeCopy(file *unit, value *ir.Query, parameters []parameter) error {
	insert := value.GetAst().GetInsert()
	rows := insert.GetValues().GetRows()

	if insert == nil || len(rows) != 1 {
		return &plugin.Error{Code: "RTL-G07", Message: "query " + value.GetName() +
			": :copyfrom takes INSERT ... VALUES with one row of parameters", Source: value.GetMeta().GetSource()}
	}

	columns := insert.GetColumns()
	if len(columns) == 0 {
		if table := s.table(insert.GetTableName()); table != nil {
			for _, column := range table.GetColumns() {
				columns = append(columns, column.GetName())
			}
		}
	}

	values := make([]string, 0, len(rows[0].GetValues()))

	for _, item := range rows[0].GetValues() {
		number := item.GetParameter().GetPosition()
		if number == 0 || int(number) > len(parameters) {
			return &plugin.Error{Code: "RTL-G07", Message: "query " + value.GetName() +
				": every :copyfrom value must be a parameter", Source: value.GetMeta().GetSource()}
		}

		values = append(values, "row."+parameters[number-1].field)
	}

	table := insert.GetTableName()
	name := value.GetName()

	file.imports = append(file.imports, "iter", "github.com/jackc/pgx/v5")
	file.printf("// %s copies rows through COPY and returns the number of copied rows.\n"+
		"func %s(ctx context.Context, db *ratel.DB, rows iter.Seq[%sParams]) (int64, error) {\n"+
		"\tnext, stop := iter.Pull(rows)\n\tdefer stop()\n\n"+
		"\treturn db.Copy(ctx, pgx.Identifier{%s, %s}, %s, pgx.CopyFromFunc(func() ([]any, error) {\n"+
		"\t\trow, ok := next()\n\t\tif !ok {\n\t\t\treturn nil, nil\n\t\t}\n\n"+
		"\t\treturn []any{%s}, nil\n\t}))\n}\n\n",
		name, name, name, strconv.Quote(schemaOf(table)), strconv.Quote(objectOf(table)), quotedList(columns),
		strings.Join(values, ", "))

	return nil
}
