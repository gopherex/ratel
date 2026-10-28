package introspect

import "google.golang.org/protobuf/reflect/protoreflect"

// Sections exposes each section's name, query and argument mode to tests.
func Sections() [][3]string {
	out := make([][3]string, 0, len(sections()))

	for _, section := range sections() {
		mode := "namespaces"
		if section.database {
			mode = "database"
		}

		out = append(out, [3]string{section.name, section.query, mode})
	}

	return out
}

// ClearSources exposes clearSources to tests.
func ClearSources(message protoreflect.Message) { clearSources(message) }
