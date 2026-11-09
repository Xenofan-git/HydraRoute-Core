package xrayapi

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestBalancerInfoWireContract(t *testing.T) {
	initBalancerDescriptors()
	req := dynamicpb.NewMessage(balancerDescriptors.request)
	req.Set(balancerDescriptors.request.Fields().ByName("tag"), protoreflect.ValueOfString("hr_bal_profile"))
	wire, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != "\x0a\x0ehr_bal_profile" {
		t.Fatalf("request wire = %q", wire)
	}
	decoded := dynamicpb.NewMessage(balancerDescriptors.response)
	// GetBalancerInfoResponse.balancer -> BalancerMsg.principle_target -> PrincipleTargetInfo.tag.
	// Nested protobuf fields: outer 1, principle_target 6, repeated tag 1.
	payload := []byte{0x0a, 0x0c, 0x32, 0x0a, 0x0a, 0x08, 'h', 'r', '_', 'n', 'o', 'd', 'e', '1'}
	if err := proto.Unmarshal(payload, decoded); err != nil {
		t.Fatal(err)
	}
	bal := decoded.Get(balancerDescriptors.response.Fields().ByName("balancer")).Message()
	principle := bal.Descriptor().Fields().ByName("principle_target")
	if !bal.Has(principle) {
		t.Fatal("principle_target missing")
	}
	tags := bal.Get(principle).Message().Get(bal.Get(principle).Message().Descriptor().Fields().ByName("tag")).List()
	if tags.Len() != 1 || tags.Get(0).String() != "hr_node1" {
		t.Fatalf("unexpected tags: %v", tags)
	}
	// HRWeb's protoField callback overwrites its captured value on each
	// matching field, so its scalar field extraction returns the last value.
	multi := []byte{0x0a, 0x16, 0x32, 0x14, 0x0a, 0x08, 'h', 'r', '_', 'n', 'o', 'd', 'e', '1', 0x0a, 0x08, 'h', 'r', '_', 'n', 'o', 'd', 'e', '2'}
	decoded = dynamicpb.NewMessage(balancerDescriptors.response)
	if err := proto.Unmarshal(multi, decoded); err != nil {
		t.Fatal(err)
	}
	bal = decoded.Get(balancerDescriptors.response.Fields().ByName("balancer")).Message()
	principle = bal.Descriptor().Fields().ByName("principle_target")
	tags = bal.Get(principle).Message().Get(bal.Get(principle).Message().Descriptor().Fields().ByName("tag")).List()
	if tags.Len() != 2 || tags.Get(1).String() != "hr_node2" {
		t.Fatalf("unexpected repeated tags: %v", tags)
	}
	if tags.Get(tags.Len()-1).String() != "hr_node2" {
		t.Fatal("scalar HRWeb extraction must resolve to last repeated tag")
	}
}

func TestGetBalancerPrincipleTargetTagsRejectsEmptyTag(t *testing.T) {
	if _, err := GetBalancerPrincipleTargetTags(context.Background(), "127.0.0.1:1", ""); err == nil {
		t.Fatal("expected empty balancer tag error")
	}
}
