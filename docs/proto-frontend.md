# Proto frontend

`protoc-gen-ratel` is a protoc plugin that turns `.proto` messages marked as
tables into PostgreSQL DDL with ratel annotations. It runs in the user's proto
toolchain (`easyp generate` or protoc) and writes one `.sql` file per `.proto`
file; ratel reads those files as ordinary schema sources ([input.md](input.md)).
The proto frontend never talks to ratel directly.

## Options

Options live in `proto/ratel/v1/options/ratel.proto`, package `ratel.v1`. One
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

## Rules

- A flattened column name that collides with another is a generation error
  naming both proto paths.
- Output is deterministic and depends only on the input descriptors.
- Negative cases (`testdata/invalid`) and a conformance harness in the style of
  plainjson's `spectest` cover every option and every mapping row.
