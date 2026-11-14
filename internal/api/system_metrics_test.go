package api

import "testing"

func TestParseCPUSample(t *testing.T) {
	x, e := parseCPUSample("cpu  10 20 30 40 5 6 7 8 9 10\ncpu0 0\n")
	if e != nil || x.total != 145 || x.idle != 45 {
		t.Fatalf("cpu=%+v err=%v", x, e)
	}
}
func TestParseMemory(t *testing.T) {
	total, used, e := parseMemory("MemTotal: 512000 kB\nMemAvailable: 112000 kB\n")
	if e != nil || total != 512000*1024 || used != 400000*1024 {
		t.Fatalf("memory=%d/%d err=%v", used, total, e)
	}
}
