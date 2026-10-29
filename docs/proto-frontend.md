# Proto frontend

`protoc-gen-ratel` is a protoc plugin that turns `.proto` messages marked as
tables into PostgreSQL DDL with ratel annotations. It runs in the user's proto
toolchain (`easyp generate` or protoc) and writes one `.sql` file per `.proto`
file; ratel reads those files as ordinary schema sources ([input.md](input.md)).
The proto frontend never talks to ratel directly.

## Options

Options live in `proto/ratel/v1/options/ratel.proto`, package `ratel.v1.options`. One
extension field number, **60120**, is used on every descriptor scope
(`FileOptions`, `MessageOptions`, `FieldOptions`, `OneofOptions`,
`EnumOptions`), as plainjson does with 60040. It does not collide with 60000,
50000 (protoc-gen-go-plain) or 60040 (protoc-gen-go-plainjson).

```proto
extend google.protobuf.MessageOptions { optional Table table = 60120; }
extend google.protobuf.FieldOptions   { optional Column column = 60120; }
extend google.protobuf.OneofOptions   { optional Oneof oneof = 60120; }
extend google.protobuf.FileOptions    { optional File file = 60120; }
extend google.protobuf.EnumOptions    { optional Enum enum = 60120; }

message File   { optional string schema = 1; }
message Table  {
  optional string name = 1;                 // default: snake_case(message name)
  repeated string primary_key = 2;          // field names
  repeated Index indexes = 3;
  repeated Unique uniques = 4;
  repeated Check checks = 5;
}
message Index  { optional string name = 1; repeated string fields = 2; optional bool unique = 3; optional string where = 4; }
message Unique { optional string name = 1; repeated string fields = 2; }
message Check  { string name = 1; string expr = 2; }
message Column {
  optional string name = 1;                 // default: snake_case(field name)
  optional string type = 2;                 // PostgreSQL type override
  optional Embed embed = 3;                 // inline a message field's columns
  optional bool serialize = 4;              // store a message field as jsonb
  optional Ref references = 5;              // FK
  optional string default = 6;              // SQL expression
  optional string generated = 7;            // STORED generated expression
  optional bool skip = 8;                   // no column
}
message Embed  { optional string prefix = 1; }
message Ref    { string table = 1; repeated string columns = 2; optional string rel = 3; optional string inverse = 4; }
message Oneof  { optional bool embed = 1; }
message Enum   { optional bool pg_enum = 1; }  // CREATE TYPE ... AS ENUM instead of text + CHECK
```

Precedence follows plainjson: the nearest scope wins; unset values inherit.
`Ref.rel` and `Ref.inverse` become `@ratel.rel` on the emitted FK.

## Mapping defaults

| Proto | PostgreSQL |
|---|---|
| message with `table` | `CREATE TABLE`, snake_case name, in `File.schema` or `public` |
| `bool` | `bool` |
| `int32`, `sint32`, `sfixed32` | `int4` |
| `uint32`, `fixed32` | `int8` (values up to 2^32-1 do not fit `int4`) |
| `int64`, `sint64`, `sfixed64` | `int8` |
| `uint64`, `fixed64` | error unless `Column.type` is set (values do not fit `int8`) |
| `float` | `float4` |
| `double` | `float8` |
| `string` | `text` |
| `bytes` | `bytea` |
| `optional`, message-typed, wrapper fields | nullable column |
| other scalar fields | `NOT NULL`, no default |
| `embed` | the message's columns inlined, names prefixed with `Embed.prefix` |
| oneof with `embed` | every branch's columns, nullable; a `<oneof>_case text` column with `CHECK (<oneof>_case IN (<branch field names>))`; `NULL` means no branch set |
| `serialize` | `jsonb` |
| repeated scalar | array column |
| repeated message, map | `jsonb`; a child table is declared as its own message with a `Ref` |
| `google.protobuf.Timestamp` | `timestamptz` |
| `google.protobuf.Duration` | `interval`; conversion fails at runtime for values with sub-microsecond precision |
| enum | `text` with `CHECK (col IN (...))`, or a PostgreSQL enum with `Enum.pg_enum` |
| `Struct`, `Value`, `ListValue`, `Any` | `jsonb` |

Every emitted table and column carries `@ratel.origin` with the full proto name
of the element it came from, so output plugins can generate conversions between
proto messages and models.

## Go models

`ratel-gen-go` uses the proto messages themselves as models, so a row is read
into and written from the message the service already returns. The frontend
passes what only protogen knows as annotations:

- `@go.message <import path>.<Type>` on a table: the message's Go type
  (`example.com/shop/v1.Customer`).
- `@go.embed <path> <import path>.<Type>` on a table, once per embedded
  message: its Go field path and type, so the model can allocate it.
- `@go.field <path> <conversion> [type=<Go type>] [oneof=<Go type>]` on a
  column: the Go field path from the message root through embeds and oneofs
  (`Address.Street`, `Contact.Phone`), how the field's value becomes the
  column's, the Go type the conversion needs, and for a oneof member its
  wrapper type (`oneof=example.com/shop/v1.Customer_Phone`).
- `@go.case <path>` on a `<oneof>_case` column: the Go path of the oneof.

Go types are written as `<import path>.<Name>`, as `@go.type` is.

| Conversion | Proto field | Column | `type=` |
|---|---|---|---|
| `value` | scalar, repeated scalar, `bytes` | its own type | |
| `optional` | `optional` scalar (`*T`) | nullable | |
| `uint32` | `uint32`, `fixed32` | `int8` | |
| `uint64` | `uint64`, `fixed64` with `Column.type` numeric | `numeric` | |
| `enum` | enum, repeated enum | text or PostgreSQL enum (array), by value name | the enum |
| `timestamp` | `google.protobuf.Timestamp` | `timestamptz` | |
| `duration` | `google.protobuf.Duration` | `interval` | |
| `wrapper` | `google.protobuf.*Value` | nullable scalar | the wrapper |
| `message` | `Struct`, `Value`, `ListValue`, `Any`, a `serialize` message | `jsonb` through protojson | the message |
| `messages` | repeated message | `jsonb` array of protojson objects | the element message |
| `map` | map with scalar values | `jsonb` object | |
| `messagemap` | map with message values | `jsonb` object of protojson values | the value message |

The generated model embeds the message and holds loaded relations:

```go
type Customer struct {
	*shopv1.Customer
	R CustomerRelations
}
```

- Fields are promoted (`row.Email`), and `row.Customer` is the message to
  return. Selected rows always carry a non-nil message.
- An embedded message is allocated only when one of its columns is not NULL;
  a nil one writes NULL to all of them. A oneof is set from its case column;
  writing an unset oneof writes NULL to the case and every member.
- Descriptor columns are typed by the proto field: `Status` compares
  `shopv1.Status` values, `CreatedAt` `*timestamppb.Timestamp` values; the
  conversion encodes each argument (`ratel.NewCol` with an encoder).
- Insert rows: `Row()` steps for required columns, as for SQL tables, and
  `RowOf(message)`, which sets every insertable column from the message. A
  column with a default or identity keeps it while its field holds the zero
  value (proto3 cannot tell an unset field from a zero one); any other column
  takes the field's value, zero included.
- A `Duration` scans only an interval without months: a month has no fixed
  length. Days count as 24 hours.

## Rules

- A flattened column name that collides with another is a generation error
  naming both proto paths.
- Output is deterministic and depends only on the input descriptors.
- Every option and mapping row is covered by a golden fixture
  (`cmd/protoc-gen-ratel/internal/ddl/testdata/valid`) or by a negative case in
  `ddl_test.go`. Every golden output must parse as PostgreSQL (go-pgquery).

## Decisions

- **No `CREATE SCHEMA`.** Each file emits only the objects it declares. A
  schema is declared once by a hand-written file, because two generated files
  declaring it would be a duplicate object ([input.md](input.md)).
- **Identifiers are always quoted** (`"shop"."customers"`). A lower-case simple
  identifier has the same catalog identity quoted or bare, so no keyword list is
  needed.
- **Origin paths.** A table carries its full message name. A column carries the
  table name plus the field path through embeds, for example
  `shop.v1.Customer.address.street`. A case column carries the oneof name.
- **Nullability.** A field is nullable when it has proto presence (`optional`,
  message-typed, oneof member). Everything inside an embedded message is nullable,
  because the embedding message may be absent. Repeated and map fields are
  `NOT NULL`.
- **Oneof.** `Oneof.embed` adds the `<oneof>_case` column; members map as
  ordinary nullable columns with or without it.
- **Repeated messages and well-known repeated types** (`Timestamp`, wrappers)
  are `jsonb`, as repeated messages are.
- **A message field needs an explicit mapping.** A message that is neither a
  well-known type nor a wrapper needs `embed` or `serialize`; otherwise it is an
  error.
- **Enum text values.** A text enum has `CHECK (col IN (...))`; a repeated text
  enum has `CHECK (col <@ ARRAY[...])`. A PostgreSQL enum (`pg_enum`) is declared
  in the file of the enum.
- **Reference targets.** `Ref.table` is the full proto name of a table message
  in the request. `Ref.columns` names exactly one direct field of that table.
  `rel` and `inverse` must be exported identifiers; `inverse` needs `rel`.
- **Raw SQL fragments** (`type`, `default`, `generated`, `where`, check
  expressions) are inserted verbatim. Each must be one line without `;`, `--` or
  `/*`; anything else is an error, because a fragment cannot be escaped.
- **Parameters.** The plugin takes none; a non-empty parameter is an error.

## Usage

Build the plugin from the repository root and put it on `PATH`:

```sh
go build -o bin/protoc-gen-ratel ./cmd/protoc-gen-ratel
bin/protoc-gen-ratel --version
```

Add it to the proto module's `easyp.gen.yaml`, next to the Go plugins:

```yaml
generate:
  paths:
    - shop
plugins:
  - name: ratel
    out: gen/schema
```

Then generate from the proto module (easyp resolves `protoc-gen-ratel` from
`PATH`):

```sh
PATH="$PWD/bin:$PATH" bin/easyp generate
```

The output is `gen/schema/shop/v1/<file>.sql`, one file per `.proto` with
`generate` paths. Point the schema sources of `ratel.yaml` at that directory.
Protoc drives the same binary without arguments: it reads a
`CodeGeneratorRequest` on stdin and writes a `CodeGeneratorResponse` to stdout.
