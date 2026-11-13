package geodata

import (
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
)

func field(n protowire.Number, b []byte) []byte {
	x := protowire.AppendTag(nil, n, protowire.BytesType)
	return protowire.AppendBytes(x, b)
}
func TestValidate(t *testing.T) {
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	good := field(1, entry)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := Validate(name, good); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{{"unknown.dat", good}, {"geoip.dat", nil}, {"geoip.dat", []byte{0x0a, 0xff}}, {"geosite.dat", field(1, field(1, []byte("RU")))}, {"geoip.dat", field(2, []byte("x"))}} {
		if err := Validate(tc.name, tc.data); err == nil {
			t.Fatalf("accepted invalid %s %x", tc.name, tc.data)
		}
	}
}
