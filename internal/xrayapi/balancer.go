package xrayapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const getBalancerInfoMethod = "/xray.app.router.command.RoutingService/GetBalancerInfo"

var balancerDescriptors struct {
	sync.Once
	request, response protoreflect.MessageDescriptor
}

func initBalancerDescriptors() {
	balancerDescriptors.Do(func() {
		optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		stringType := descriptorpb.FieldDescriptorProto_TYPE_STRING
		messageType := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		field := func(name string, number int32, label descriptorpb.FieldDescriptorProto_Label, typ descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
			f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Label: &label, Type: &typ}
			if typeName != "" {
				f.TypeName = proto.String(typeName)
			}
			return f
		}
		file := &descriptorpb.FileDescriptorProto{
			Name: proto.String("app/router/command/command.proto"), Package: proto.String("xray.app.router.command"), Syntax: proto.String("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{
				{Name: proto.String("GetBalancerInfoRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, optional, stringType, "")}},
				{Name: proto.String("GetBalancerInfoResponse"), Field: []*descriptorpb.FieldDescriptorProto{field("balancer", 1, optional, messageType, ".xray.app.router.command.BalancerMsg")}},
				{Name: proto.String("BalancerMsg"), Field: []*descriptorpb.FieldDescriptorProto{field("override", 5, optional, messageType, ".xray.app.router.command.OverrideInfo"), field("principle_target", 6, optional, messageType, ".xray.app.router.command.PrincipleTargetInfo")}},
				{Name: proto.String("OverrideInfo"), Field: []*descriptorpb.FieldDescriptorProto{field("target", 2, optional, stringType, "")}},
				{Name: proto.String("PrincipleTargetInfo"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, repeated, stringType, "")}},
			},
			Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("RoutingService"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("GetBalancerInfo"), InputType: proto.String(".xray.app.router.command.GetBalancerInfoRequest"), OutputType: proto.String(".xray.app.router.command.GetBalancerInfoResponse")}}}},
		}
		fd, err := protodesc.NewFile(file, nil)
		if err != nil {
			panic(fmt.Sprintf("build Xray router API descriptors: %v", err))
		}
		balancerDescriptors.request = fd.Messages().ByName("GetBalancerInfoRequest")
		balancerDescriptors.response = fd.Messages().ByName("GetBalancerInfoResponse")
	})
}

// GetBalancerPrincipleTargetTags reads Xray RoutingService strategy candidates.
// These are runtime outbound tags, not persistent node IDs, and are not
// necessarily the outbound currently carrying a connection.
func GetBalancerPrincipleTargetTags(ctx context.Context, address, balancerTag string) ([]string, error) {
	if balancerTag == "" {
		return nil, fmt.Errorf("balancer tag is empty")
	}
	if address == "" {
		address = DefaultAddress
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to Xray Routing API: %w", err)
	}
	defer conn.Close()
	initBalancerDescriptors()
	request := dynamicpb.NewMessage(balancerDescriptors.request)
	request.Set(balancerDescriptors.request.Fields().ByName("tag"), protoreflect.ValueOfString(balancerTag))
	response := dynamicpb.NewMessage(balancerDescriptors.response)
	if err := conn.Invoke(ctx, getBalancerInfoMethod, request, response); err != nil {
		return nil, fmt.Errorf("read Xray balancer %q state: %w", balancerTag, err)
	}
	balancerField := balancerDescriptors.response.Fields().ByName("balancer")
	if !response.Has(balancerField) {
		return nil, nil
	}
	balancer := response.Get(balancerField).Message()
	principleField := balancer.Descriptor().Fields().ByName("principle_target")
	if !balancer.Has(principleField) {
		return nil, nil
	}
	tagsField := balancer.Get(principleField).Message().Descriptor().Fields().ByName("tag")
	values := balancer.Get(principleField).Message().Get(tagsField).List()
	out := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		if tag := values.Get(i).String(); tag != "" {
			out = append(out, tag)
		}
	}
	return out, nil
}

// GetBalancerPrincipleTargetTag mirrors HRWeb protoField semantics: repeated
// matching fields overwrite the prior value, so the last strategy target wins.
func GetBalancerPrincipleTargetTag(ctx context.Context, address, balancerTag string) (string, error) {
	tags, err := GetBalancerPrincipleTargetTags(ctx, address, balancerTag)
	if err != nil || len(tags) == 0 {
		return "", err
	}
	return tags[len(tags)-1], nil
}
