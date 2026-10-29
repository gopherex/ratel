package gengo

import (
	"strconv"
	"strings"
)

// scanSlot is how a scanner reads one column of a proto model: straight into
// the message field, or into a temporary converted after Scan.
type scanSlot struct {
	target string // Scan argument
	temp   string // the temporary's Go type; empty for a direct target
	valid  string // condition under which the temporary holds a value
	raw    string // the temporary's value
}

// scanSlot picks the Scan argument of a column.
func (s *state) scanSlot(file *unit, value column, name string) scanSlot {
	if target, direct := directTarget(value); direct {
		return scanSlot{target: target}
	}

	slot := scanSlot{target: "&" + name, valid: name + " != nil", raw: name}
	if value.proto.oneof != "" {
		return pgtypeSlot(file, slot, name, "Text", "String")
	}

	return s.fieldSlot(file, value, slot, name)
}

// directTarget is the message field a top-level column of the field's own
// type scans into without a temporary.
func directTarget(value column) (string, bool) {
	column := value.proto
	embeds, _, leaf := segments(column)

	direct := column.oneof == "" && column.field.oneof.expr == "" && len(embeds) == 0 &&
		column.field.conversion == convValue && (!value.nullable || column.field.optional)

	return "&m." + leaf, direct
}

// fieldSlot reads a field column into a temporary its conversion reads.
func (s *state) fieldSlot(file *unit, value column, slot scanSlot, name string) scanSlot {
	column := value.proto
	nullable := value.nullable || column.field.oneof.expr != ""

	switch column.field.conversion {
	case convValue:
		return s.valueSlot(file, value, slot, name, nullable)
	case convUint32:
		return plainOrPgtype(file, slot, name, nullable, int64Type, "Int8", "Int64")
	case convUint64:
		return pgtypeSlot(file, slot, name, "Numeric", "")
	case convEnum:
		if value.array {
			slot.temp = "[]string"

			return slot
		}

		return plainOrPgtype(file, slot, name, nullable, stringType, "Text", "String")
	case convTimestamp:
		file.imports = append(file.imports, "time")
		slot.temp = "*time.Time"

		return slot
	case convDuration:
		return pgtypeSlot(file, slot, name, "Interval", "")
	case convWrapper:
		return s.wrapperSlot(file, value, slot, name)
	default:
		slot.temp = "[]byte"

		return slot
	}
}

// valueSlot reads a column of the field's own type.
func (s *state) valueSlot(file *unit, value column, slot scanSlot, name string, nullable bool) scanSlot {
	if !nullable {
		slot.temp, slot.valid = file.use(value.goType), ""

		return slot
	}

	target := s.nullTarget(file, value.typ, value.goType)
	slot.temp, slot.valid, slot.raw = target.temp, target.valid(name), target.valueOf(name)

	return slot
}

// wrapperSlot reads the nullable column of a wrapper message.
func (s *state) wrapperSlot(file *unit, value column, slot scanSlot, name string) scanSlot {
	switch wrapperKind(value.proto.field) {
	case wrapperUInt32:
		return pgtypeSlot(file, slot, name, "Int8", "Int64")
	case wrapperUInt64:
		return pgtypeSlot(file, slot, name, "Numeric", "")
	default:
		return s.valueSlot(file, value, slot, name, true)
	}
}

func pgtypeSlot(file *unit, slot scanSlot, name, typ, field string) scanSlot {
	file.imports = append(file.imports, pgtypeImport)
	slot.temp, slot.valid, slot.raw = "pgtype."+typ, name+".Valid", name

	if field != "" {
		slot.raw = name + "." + field
	}

	return slot
}

func plainOrPgtype(file *unit, slot scanSlot, name string, nullable bool, plain, typ, field string) scanSlot {
	if nullable {
		return pgtypeSlot(file, slot, name, typ, field)
	}

	slot.temp, slot.valid = plain, ""

	return slot
}

// loadValue renders the statements that turn a scanned raw value into the
// field value, in the variable value; a conversion that fails returns.
func loadValue(file *unit, column *protoColumn, raw, failure string) string {
	field := column.field
	failing := func(call string) string {
		return "\t\tvalue, err := " + call + "\n\t\tif err != nil {\n\t\t\treturn " + failure + ", err\n\t\t}\n"
	}

	switch field.conversion {
	case convUint32:
		return failing("ratelpb.Uint32(" + raw + ")")
	case convUint64:
		return failing("ratelpb.Uint64(" + raw + ")")
	case convEnum:
		enum := file.use(field.typ)
		if strings.HasPrefix(column.fieldType, "[]") {
			return failing("ratelpb.FromEnums[" + enum + "](" + enum + "_value, " + raw + ")")
		}

		return failing("ratelpb.FromEnum[" + enum + "](" + enum + "_value, " + raw + ")")
	case convTimestamp:
		return "\t\tvalue := ratelpb.FromTime(" + raw + ")\n"
	case convDuration:
		return failing("ratelpb.FromInterval(" + raw + ")")
	case convWrapper:
		return loadWrapper(file, column, raw, failure)
	case convMessage:
		return failing("ratelpb.FromJSON[" + file.use(field.typ) + "](" + raw + ")")
	case convMessages:
		return failing("ratelpb.FromJSONList[" + file.use(field.typ) + "](" + raw + ")")
	case convMap:
		return failing("ratelpb.FromScalarMap[" + file.use(field.key) + ", " + file.use(field.value) + "](" + raw + ")")
	case convMessageMap:
		return failing("ratelpb.FromJSONMap[" + file.use(field.key) + ", " + file.use(field.typ) + "](" + raw + ")")
	default:
		return "\t\tvalue := " + raw + "\n"
	}
}

func loadWrapper(file *unit, column *protoColumn, raw, failure string) string {
	wrapper := file.use(column.field.typ)
	constructor := wrapper[:strings.LastIndex(wrapper, ".")+1] + wrapperKind(column.field)

	switch wrapperKind(column.field) {
	case wrapperUInt64:
		return innerValue("ratelpb.Uint64("+raw+")", failure) + "\t\tvalue := " + constructor + "(inner)\n"
	case wrapperUInt32:
		return innerValue("ratelpb.Uint32("+raw+")", failure) + "\t\tvalue := " + constructor + "(inner)\n"
	default:
		return "\t\tvalue := " + constructor + "(" + raw + ")\n"
	}
}

// innerValue renders a failing conversion into the variable inner.
func innerValue(call, failure string) string {
	return "\t\tinner, err := " + call + "\n\t\tif err != nil {\n\t\t\treturn " + failure + ", err\n\t\t}\n\n"
}

// ensure renders the allocation of the embedded messages on a path; it
// returns the parent expression.
func ensure(file *unit, model *protoModel, embeds []string) (string, string) {
	var code strings.Builder

	parent := "m"

	for index := range embeds {
		path := strings.Join(embeds[:index+1], ".")
		child := parent + "." + embeds[index]
		code.WriteString("\t\tif " + child + " == nil {\n\t\t\t" + child + " = &" + file.use(model.embeds[path]) +
			"{}\n\t\t}\n\n")

		parent = child
	}

	return code.String(), parent
}

// writeProtoScanner writes the scanner of a proto model: Scan into the
// message's fields and temporaries, then convert the temporaries.
func (s *state) writeProtoScanner(file *unit, names identifiers, model *protoModel, columns []column) {
	message := file.use(model.message)
	targets := make([]string, len(columns))
	slots := make([]scanSlot, len(columns))

	var declared, loads strings.Builder

	for index, value := range columns {
		name := "scan" + strconv.Itoa(index)
		slots[index] = s.scanSlot(file, value, name)
		targets[index] = slots[index].target

		if slots[index].temp != "" {
			declared.WriteString("\tvar " + name + " " + slots[index].temp + "\n")
		}
	}

	for index, value := range columns {
		switch {
		case slots[index].temp == "" || value.proto.field.oneof.expr != "":
		case value.proto.oneof != "":
			loads.WriteString(loadCase(file, model, columns, slots, index))
		default:
			loads.WriteString(loadField(file, model, value.proto, slots[index]))
		}
	}

	file.printf("func %s(row pgx.Row) (%s, error) {\n\tmodel := %s{%s: &%s{}}\n\tm := model.%s\n\n%s\n",
		names.scanner, names.model, names.model, messageField(message), message, messageField(message),
		declared.String())
	file.printf("\tif err := row.Scan(%s); err != nil {\n\t\treturn model, err\n\t}\n\n%s\treturn model, nil\n}\n\n",
		strings.Join(targets, ", "), loads.String())
}

func loadField(file *unit, model *protoModel, column *protoColumn, slot scanSlot) string {
	embeds, _, leaf := segments(column)
	allocate, parent := ensure(file, model, embeds)

	assigned := "value"
	if column.field.optional {
		assigned = "&value"
	}

	body := loadValue(file, column, slot.raw, "model") + allocate + "\t\t" + parent + "." + leaf + " = " + assigned + "\n"
	if slot.valid == "" {
		return "\t{\n" + body + "\t}\n\n"
	}

	return "\tif " + slot.valid + " {\n" + body + "\t}\n\n"
}

// loadCase sets a oneof from its case column and its member's temporary.
func loadCase(file *unit, model *protoModel, columns []column, slots []scanSlot, index int) string {
	column := columns[index].proto
	embeds, oneof, _ := segments(column)
	allocate, parent := ensure(file, model, embeds)

	var cases strings.Builder

	for member, value := range columns {
		field := value.proto.field
		if field.oneof.expr == "" || memberOf(value.proto) != column.oneof {
			continue
		}

		_, _, leaf := segments(value.proto)
		cases.WriteString("\t\tcase " + strconv.Quote(field.caseName) + ":\n\t\t\tvar member " + value.proto.fieldType +
			"\n\n\t\t\tif " + slots[member].valid + " {\n" + indent(loadValue(file, value.proto, slots[member].raw, "model")) +
			"\t\t\t\tmember = value\n\t\t\t}\n\n" + indent(allocate) + "\t\t\t" + parent + "." + oneof + " = &" +
			file.use(field.oneof) + "{" + leaf + ": member}\n")
	}

	return "\tif " + slots[index].valid + " {\n\t\tswitch " + slots[index].raw + " {\n" + cases.String() +
		"\t\t}\n\t}\n\n"
}

// indent shifts generated statements one tab right.
func indent(code string) string {
	var out strings.Builder

	for _, line := range strings.SplitAfter(code, "\n") {
		if strings.TrimSpace(line) != "" {
			out.WriteString("\t")
		}

		out.WriteString(line)
	}

	return out.String()
}
