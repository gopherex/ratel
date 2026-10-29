package gengo

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
)

// parameter is a query parameter as a Go argument.
type parameter struct {
	name   string // Go field name in a step
	method string // step method, Params struct field
	goType string
	// nullable values are ratel.Null, passed to pgx through Arg so they need
	// no type registration.
	nullable bool
}

// argument is the pgx argument of a value held in expression.
func (p parameter) argument(expression string) string {
	if p.nullable {
		return expression + ".Arg()"
	}

	return expression
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

		if !file.registry.empty() {
			if err := file.declare("RegisterTypes", "query package "+group); err != nil {
				return nil, err
			}

			writeRegisterTypes(file)
		}

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

		s.use(file, item.GetType())

		goType := file.use(mapped)

		if item.GetNullable() {
			file.imports = append(file.imports, ratelImport)
			goType = "ratel.Null[" + goType + "]"
		}

		out = append(out, parameter{
			name: parameterName(name), method: pascal(name), goType: goType, nullable: item.GetNullable(),
		})
	}

	return out, nil
}

// writeParams declares the row type of a :copyfrom query.
func writeParams(file *unit, name string, parameters []parameter) {
	file.printf("// %sParams are the parameters of %s.\ntype %sParams struct {\n", name, name, name)

	for _, value := range parameters {
		file.printf("\t%s %s\n", value.method, value.goType)
	}

	file.printf("}\n\n")
}

// writeSteps writes the builder of a query: a function starting it and one
// step type per parameter, in order, each with one method naming its
// parameter. Only the last step returns the runnable value, built by build
// from the arguments in parameter order, so a parameter cannot be skipped or
// swapped. Without parameters the function returns it directly.
func writeSteps(file *unit, name, result string, parameters []parameter, build func(arguments string) string) error {
	if len(parameters) == 0 {
		file.printf("// %s builds the query.\nfunc %s() %s {\n\treturn %s\n}\n\n", name, name, result, build(""))

		return nil
	}

	steps := make([]string, len(parameters))

	for index, value := range parameters {
		steps[index] = camel(name) + value.method
		if err := file.declare(steps[index], "query "+name); err != nil {
			return err
		}
	}

	file.printf("// %s starts the query; each parameter is one step, in order of appearance.\n"+
		"func %s() %s {\n\treturn %s{}\n}\n\n", name, name, steps[0], steps[0])

	for index, value := range parameters {
		file.printf("type %s struct{", steps[index])

		carried := make([]string, 0, index+1)

		for _, earlier := range parameters[:index] {
			file.printf("\n\t%s %s", earlier.name, earlier.goType)

			carried = append(carried, earlier.name)
		}

		if index > 0 {
			file.printf("\n")
		}

		returned, next := result, build(stepArguments(parameters[:index+1]))

		if index < len(parameters)-1 {
			returned = steps[index+1]
			next = stepLiteral(returned, append(carried, value.name))
		}

		file.printf("}\n\n// %s sets the parameter %s.\nfunc (s %s) %s(v %s) %s {\n\treturn %s\n}\n\n",
			value.method, value.name, steps[index], value.method, value.goType, returned, next)
	}

	return nil
}

// stepLiteral is the next step carrying the earlier values and the new one
// (the method argument v).
func stepLiteral(step string, names []string) string {
	fields := make([]string, len(names))

	for index, name := range names[:len(names)-1] {
		fields[index] = name + ": s." + name
	}

	fields[len(names)-1] = names[len(names)-1] + ": v"

	return step + "{" + strings.Join(fields, ", ") + "}"
}

// stepArguments are the pgx arguments of the last step: the carried values
// followed by the method argument v.
func stepArguments(parameters []parameter) string {
	arguments := make([]string, 0, len(parameters))
	for _, value := range parameters[:len(parameters)-1] {
		arguments = append(arguments, value.argument("s."+value.name))
	}

	return strings.Join(append(arguments, parameters[len(parameters)-1].argument("v")), ", ")
}

func (s *state) query(file *unit, value *ir.Query) error {
	name := value.GetName()

	parameters, err := s.parameters(file, value)
	if err != nil {
		return err
	}

	if value.GetCommand() == ir.QueryCommand_QUERY_COMMAND_COPY_FROM {
		if err := file.declare(name+"Params", "query "+name); err != nil {
			return err
		}

		writeParams(file, name, parameters)
	}

	if err := file.declare(name, "query "+name); err != nil {
		return err
	}

	file.imports = append(file.imports, ratelImport)

	switch value.GetCommand() {
	case ir.QueryCommand_QUERY_COMMAND_EXEC, ir.QueryCommand_QUERY_COMMAND_EXEC_ROWS:
		return writeSteps(file, name, "ratel.Command", parameters, func(arguments string) string {
			return commandCall(value, arguments)
		})
	case ir.QueryCommand_QUERY_COMMAND_COPY_FROM:
		return s.writeCopy(file, value, parameters)
	default:
		return s.writeRead(file, value, parameters)
	}
}

func commandCall(value *ir.Query, arguments string) string {
	call := "ratel.NewCommand(" + strconv.Quote(value.GetSql())
	if arguments != "" {
		call += ", " + arguments
	}

	return call + ")"
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

		values = append(values, parameters[number-1].argument("row."+parameters[number-1].method))
	}

	table := insert.GetTableName()
	name := value.GetName()

	file.imports = append(file.imports, "context", "iter", "github.com/jackc/pgx/v5")
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
