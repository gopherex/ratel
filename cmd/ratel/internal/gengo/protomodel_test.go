package gengo_test

import (
	"slices"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestProtoSegments(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		path           string
		isCase, member bool
		embeds         []string
		oneof, leaf    string
	}{
		{name: "top-level field", path: "Email", leaf: "Email"},
		{name: "embedded field", path: "Address.Inner.Street", embeds: []string{"Address", "Inner"}, leaf: "Street"},
		{
			name: "member", path: "Address.Contact.Phone", member: true, embeds: []string{"Address"}, oneof: "Contact",
			leaf: "Phone",
		},
		{name: "case", path: "Address.Contact", isCase: true, embeds: []string{"Address"}, oneof: "Contact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			embeds, oneof, leaf := gengo.ProtoSegments(tc.path, tc.isCase, tc.member)
			if !slices.Equal(embeds, tc.embeds) || oneof != tc.oneof || leaf != tc.leaf {
				t.Fatalf("segments = %v, %q, %q", embeds, oneof, leaf)
			}
		})
	}
}

func TestProtoConversion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		conversion, wrapper, fieldType, want string
	}{
		{"value", "", "string", "v"},
		{"uint32", "", "uint32", "int64(v)"},
		{"uint64", "", "uint64", "ratelpb.Numeric(v)"},
		{"enum", "pb.Status", "pb.Status", "v.String()"},
		{"enum", "pb.Status", "[]pb.Status", "ratelpb.EnumNames(v)"},
		{"timestamp", "", "*timestamppb.Timestamp", "v.AsTime()"},
		{"wrapper", "wrapperspb.Int32Value", "*wrapperspb.Int32Value", "v.GetValue()"},
		{"wrapper", "wrapperspb.UInt32Value", "*wrapperspb.UInt32Value", "int64(v.GetValue())"},
		{"wrapper", "wrapperspb.UInt64Value", "*wrapperspb.UInt64Value", "ratelpb.Numeric(v.GetValue())"},
		{"duration", "", "*durationpb.Duration", "ratelpb.Interval(v)"},
		{"message", "structpb.Struct", "*structpb.Struct", "ratelpb.JSON(v)"},
		{"messages", "pb.Note", "[]*pb.Note", "ratelpb.JSONList(v)"},
		{"map", "", "map[string]int32", "ratelpb.ScalarMap(v)"},
		{"messagemap", "pb.Note", "map[string]*pb.Note", "ratelpb.JSONMap(v)"},
	} {
		t.Run(tc.conversion+" "+tc.fieldType, func(t *testing.T) {
			t.Parallel()

			if got := gengo.ProtoConversion(tc.conversion, tc.wrapper, tc.fieldType, "v"); got != tc.want {
				t.Fatalf("conversion = %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkProtoSegments(b *testing.B) {
	for b.Loop() {
		gengo.ProtoSegments("Address.Contact.Phone", false, true)
	}
}

func BenchmarkProtoConversion(b *testing.B) {
	for b.Loop() {
		gengo.ProtoConversion("wrapper", "wrapperspb.UInt64Value", "*wrapperspb.UInt64Value", "v")
	}
}
