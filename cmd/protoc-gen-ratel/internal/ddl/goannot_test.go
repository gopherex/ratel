package ddl_test

import (
	"strings"
	"testing"
)

const goModels = `syntax = "proto3";

package models.v1;

option go_package = "example.com/models/v1;modelsv1";

import "google/protobuf/duration.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "ratel/v1/options/ratel.proto";

enum Kind {
  KIND_UNSPECIFIED = 0;
  KIND_A = 1;
}

message Inner {
  string label = 1;
  oneof choice {
    option (ratel.v1.options.oneof) = { embed: true };
    int64 number = 2;
    string text = 3;
  }
}

message Row {
  option (ratel.v1.options.table) = { primary_key: "id" };

  int64 id = 1;
  optional string note = 2;
  uint32 small = 3;
  uint64 big = 4 [(ratel.v1.options.column) = { type: "numeric(20)" }];
  Kind kind = 5;
  repeated Kind kinds = 6;
  google.protobuf.Timestamp at = 7;
  google.protobuf.Duration ttl = 8;
  google.protobuf.Int64Value score = 9;
  google.protobuf.Struct meta = 10;
  repeated Inner history = 11;
  map<string, int32> counts = 12;
  map<string, Inner> named = 13;
  Inner inner = 14 [(ratel.v1.options.column) = { embed: { prefix: "inner_" } }];
}
`

func TestGoAnnotations(t *testing.T) {
	t.Parallel()

	outputs, err := generate(t, compile(t, map[string]string{"models.proto": goModels}, []string{"models.proto"}))
	if err != nil {
		t.Fatal(err)
	}

	sql := outputs["models.sql"]

	for _, want := range []string{
		"-- @go.message example.com/models/v1.Row\n",
		"-- @go.embed Inner example.com/models/v1.Inner\n",
		"-- @go.field Id value\n",
		"-- @go.field Note optional\n",
		"-- @go.field Small uint32\n",
		"-- @go.field Big uint64\n",
		"-- @go.field Kind enum type=example.com/models/v1.Kind\n",
		"-- @go.field Kinds enum type=example.com/models/v1.Kind\n",
		"-- @go.field At timestamp\n",
		"-- @go.field Ttl duration\n",
		"-- @go.field Score wrapper type=google.golang.org/protobuf/types/known/wrapperspb.Int64Value\n",
		"-- @go.field Meta message type=google.golang.org/protobuf/types/known/structpb.Struct\n",
		"-- @go.field History messages type=example.com/models/v1.Inner\n",
		"-- @go.field Counts map\n",
		"-- @go.field Named messagemap type=example.com/models/v1.Inner\n",
		"-- @go.field Inner.Label value\n",
		"-- @go.field Inner.Choice.Number value oneof=example.com/models/v1.Inner_Number case=number\n",
		"-- @go.case Inner.Choice\n",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}
