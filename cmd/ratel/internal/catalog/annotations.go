package catalog

import (
	"cmp"
	"slices"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
)

func (b *builder) annotate(files []File, owned []string) error {
	targets := make([]annotate.Target, 0, len(b.objects))
	for _, entry := range b.objects {
		targets = append(targets, annotate.Target{Object: entry.reference, Meta: entry.meta, ForeignKey: b.foreignKey(entry)})
		targets = append(targets, annotationChildren(entry)...)
	}

	targets = append(targets, b.annotationAliases(targets)...)

	slices.SortFunc(targets, func(left, right annotate.Target) int {
		return cmp.Compare(objectKey(left.Object), objectKey(right.Object))
	})

	for _, file := range files {
		diagnostics, err := annotate.Attach(b.parser, annotate.File{Name: file.Name, SQL: file.SQL}, targets, owned)
		if err != nil {
			return err
		}

		b.catalog.Diagnostics = append(b.catalog.GetDiagnostics(), diagnostics...)
	}

	return nil
}

func (b *builder) foreignKey(entry declaration) bool {
	switch value := entry.message.(type) {
	case *ir.Constraint:
		return value.GetForeignKey() != nil
	case *ir.Column:
		parts := entry.reference.GetName().GetParts()

		parent := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: &ir.QualifiedName{Parts: parts[:len(parts)-1]}}
		switch table := b.objects[objectKey(parent)].message.(type) {
		case *ir.Table:
			for _, constraint := range table.GetConstraints() {
				if slices.Contains(constraint.GetForeignKey().GetColumns(), value.GetName()) {
					return true
				}
			}
		default:
		}
	default:
	}

	return false
}

func (b *builder) annotationAliases(targets []annotate.Target) []annotate.Target {
	var aliases []annotate.Target

	for _, stmt := range b.statements {
		alter := stmt.syntax.GetAlterTableStmt()
		if alter == nil {
			continue
		}

		fragment := stmt.mapper.MapAlterTable(alter)
		source := fragment.GetMeta().GetSource()

		if len(fragment.GetConstraints()) == 1 {
			wanted := fragment.GetConstraints()[0].GetMeta().GetSource()

			for _, target := range targets {
				actual := target.Meta.GetSource()
				if target.Object.GetKind() == ir.ObjectKind_OBJECT_KIND_CONSTRAINT &&
					actual.GetFile() == wanted.GetFile() && actual.GetStartOffset() == wanted.GetStartOffset() {
					target.Source = source
					aliases = append(aliases, target)
				}
			}

			continue
		}

		name := &ir.QualifiedName{Parts: []string{alter.GetRelation().GetRelname()}}
		if schema := alter.GetRelation().GetSchemaname(); schema != "" {
			name.Parts = []string{schema, name.GetParts()[0]}
		}

		qualify(name, stmt.schema, ir.ObjectKind_OBJECT_KIND_TABLE)
		entry := b.objects[objectKey(&ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: name})]
		aliases = append(aliases, annotate.Target{Object: entry.reference, Meta: entry.meta, Source: source})
	}

	for _, pending := range b.comments {
		entry := b.objects[objectKey(pending.reference)]
		aliases = append(aliases, annotate.Target{
			Object: entry.reference, Meta: entry.meta, Source: pending.meta.GetSource(), ForeignKey: b.foreignKey(entry),
		})
	}

	return append(aliases, b.grantAnnotationAliases()...)
}

func (b *builder) grantAnnotationAliases() []annotate.Target {
	type sourceKey struct {
		file   string
		offset uint64
	}

	grouped := make(map[sourceKey][]annotate.Target)

	for _, schema := range b.catalog.GetSchemas() {
		for _, grant := range schema.GetGrants() {
			source := grant.GetMeta().GetSource()
			key := sourceKey{file: source.GetFile(), offset: source.GetStartOffset()}
			entry := b.objects[objectKey(grant.GetObject())]
			target := annotate.Target{Object: entry.reference, Meta: entry.meta, Source: source, ForeignKey: b.foreignKey(entry)}
			grouped[key] = append(grouped[key], target)
		}
	}

	var aliases []annotate.Target

	for _, targets := range grouped {
		first := targets[0]
		same := true

		for _, target := range targets[1:] {
			if objectKey(first.Object) != objectKey(target.Object) {
				same = false

				break
			}
		}

		if same {
			aliases = append(aliases, first)
		}
	}

	return aliases
}

func annotationChildren(entry declaration) []annotate.Target {
	var result []annotate.Target

	add := func(name string, kind ir.ObjectKind, meta *ir.Metadata) {
		result = append(result, annotate.Target{
			Object: &ir.ObjectRef{Kind: kind, Name: &ir.QualifiedName{
				Parts: append(slices.Clone(entry.reference.GetName().GetParts()), name),
			}}, Meta: meta,
		})
	}

	switch value := entry.message.(type) {
	case *ir.CompositeType:
		for _, field := range value.GetFields() {
			add(field.GetName(), ir.ObjectKind_OBJECT_KIND_COLUMN, field.GetMeta())
		}
	case *ir.DomainType:
		for _, constraint := range value.GetConstraints() {
			add(constraint.GetName(), ir.ObjectKind_OBJECT_KIND_CONSTRAINT, constraint.GetMeta())
		}
	default:
	}

	return result
}
