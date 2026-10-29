package gengo

import (
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
)

// protoModel is a table whose model is a proto message, from the annotations
// the proto frontend emits (docs/proto-frontend.md, Go models).
type protoModel struct {
	message goType
	embeds  map[string]goType     // Go field path of an embedded message -> its type
	fields  map[string]protoField // column -> how its proto field converts
	cases   map[string]string     // case column -> Go path of its oneof
}

// protoField is the proto field of a column and its conversion.
type protoField struct {
	path       string // Go field path from the message root
	conversion conversion
	typ        goType // the Go type a conversion names, if any
	oneof      goType // the oneof wrapper type of a member
	caseName   string // the member's proto name, its case column value
}

// conversion names how a proto field converts to its column (@go.field).
type conversion string

const (
	convValue      conversion = "value"
	convOptional   conversion = "optional"
	convUint32     conversion = "uint32"
	convUint64     conversion = "uint64"
	convTimestamp  conversion = "timestamp"
	convDuration   conversion = "duration"
	convMap        conversion = "map"
	convEnum       conversion = "enum"
	convWrapper    conversion = "wrapper"
	convMessage    conversion = "message"
	convMessages   conversion = "messages"
	convMessageMap conversion = "messagemap"
)

// typed reports whether a conversion names a Go type; known is false for an
// unknown conversion.
func (c conversion) typed() (bool, bool) {
	switch c {
	case convValue, convOptional, convUint32, convUint64, convTimestamp, convDuration, convMap:
		return false, true
	case convEnum, convWrapper, convMessage, convMessages, convMessageMap:
		return true, true
	default:
		return false, false
	}
}

// protoTable is the model of a table, created on its first annotation.
func (s *state) protoTable(parts []string) *protoModel {
	key := strings.Join(parts, ".")
	if s.protos[key] == nil {
		s.protos[key] = &protoModel{
			embeds: make(map[string]goType), fields: make(map[string]protoField), cases: make(map[string]string),
		}
	}

	return s.protos[key]
}

// annotateProto records @go.message, @go.embed, @go.field and @go.case; the
// boolean is false for another annotation.
func (s *state) annotateProto(annotation *ir.AnnotationValue) (bool, error) {
	var usage string

	switch annotation.GetName() {
	case "message":
		usage = s.annotateMessage(annotation)
	case "embed":
		usage = s.annotateEmbed(annotation)
	case "field":
		usage = s.annotateField(annotation)
	case "case":
		usage = s.annotateCase(annotation)
	default:
		return false, nil
	}

	if usage != "" {
		return true, &plugin.Error{Code: codeAnnotation, Message: usage, Source: annotation.GetSource()}
	}

	return true, nil
}

// Each annotate function records a valid annotation and returns "", or
// returns the annotation's usage.

func (s *state) annotateMessage(annotation *ir.AnnotationValue) string {
	target, args := annotation.GetObject(), annotation.GetArgs()
	if target.GetKind() != ir.ObjectKind_OBJECT_KIND_TABLE || len(args) != 1 || args[0].GetKey() != "" {
		return "@go.message takes one Go type and targets a table"
	}

	s.protoTable(target.GetName().GetParts()).message = override(args[0].GetValue())

	return ""
}

func (s *state) annotateEmbed(annotation *ir.AnnotationValue) string {
	target, args := annotation.GetObject(), annotation.GetArgs()
	if target.GetKind() != ir.ObjectKind_OBJECT_KIND_TABLE || !positional(args, len(args)) || len(args) != 2 {
		return "@go.embed takes a Go field path and a Go type and targets a table"
	}

	s.protoTable(target.GetName().GetParts()).embeds[args[0].GetValue()] = override(args[1].GetValue())

	return ""
}

func (s *state) annotateField(annotation *ir.AnnotationValue) string {
	target := annotation.GetObject()
	parts := target.GetName().GetParts()

	field, ok := protoFieldOf(annotation.GetArgs())
	if target.GetKind() != ir.ObjectKind_OBJECT_KIND_COLUMN || len(parts) != columnParts || !ok {
		return "@go.field takes a Go field path, a conversion, type= when the conversion needs one, " +
			"oneof= for a member, and targets a column"
	}

	s.protoTable(parts[:2]).fields[parts[2]] = field

	return ""
}

func (s *state) annotateCase(annotation *ir.AnnotationValue) string {
	target, args := annotation.GetObject(), annotation.GetArgs()
	parts := target.GetName().GetParts()

	if target.GetKind() != ir.ObjectKind_OBJECT_KIND_COLUMN || len(parts) != columnParts || len(args) != 1 ||
		args[0].GetKey() != "" {
		return "@go.case takes the Go path of a oneof and targets a column"
	}

	s.protoTable(parts[:2]).cases[parts[2]] = args[0].GetValue()

	return ""
}

// positional reports whether the first count arguments have no key.
func positional(args []*ir.AnnotationArg, count int) bool {
	if len(args) < count {
		return false
	}

	for _, arg := range args[:count] {
		if arg.GetKey() != "" {
			return false
		}
	}

	return true
}

// protoFieldOf parses the arguments of @go.field.
func protoFieldOf(args []*ir.AnnotationArg) (protoField, bool) {
	// A Go field path and a conversion, then keyed arguments.
	const leading = 2

	if !positional(args, leading) {
		return protoField{}, false
	}

	field := protoField{path: args[0].GetValue(), conversion: conversion(args[1].GetValue())}

	typed, known := field.conversion.typed()
	if !known {
		return protoField{}, false
	}

	for _, arg := range args[2:] {
		switch arg.GetKey() {
		case "type":
			field.typ = override(arg.GetValue())
		case "oneof":
			field.oneof = override(arg.GetValue())
		case "case":
			field.caseName = arg.GetValue()
		default:
			return protoField{}, false
		}
	}

	member := field.oneof.expr != ""

	return field, typed == (field.typ.expr != "") && member == (field.caseName != "")
}

// checkProtos rejects a proto table without its message or with a column
// that has no field: the model could not read or write it.
func (s *state) checkProtos() error {
	for key, model := range s.protos {
		if model.message.expr == "" {
			return &plugin.Error{Code: codeAnnotation, Message: "table " + key + " has @go.field without @go.message"}
		}

		schema, name, _ := strings.Cut(key, ".")

		table := s.table(&ir.QualifiedName{Parts: []string{schema, name}})
		if table == nil {
			continue
		}

		for _, column := range table.GetColumns() {
			_, field := model.fields[column.GetName()]
			_, oneof := model.cases[column.GetName()]

			if !field && !oneof {
				return &plugin.Error{Code: codeAnnotation, Message: "column " + key + "." + column.GetName() +
					" of a proto model has neither @go.field nor @go.case"}
			}
		}
	}

	return nil
}
