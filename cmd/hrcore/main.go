package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"hydraroute.local/core/internal/compiler"
)

func main() {
	input := flag.String("input", "-", "JSON compile request file, or - for stdin")
	output := flag.String("output", "-", "Xray JSON output file, or - for stdout")
	flag.Parse()

	var r io.Reader = os.Stdin
	if *input != "-" {
		f, err := os.Open(*input)
		fatalIf(err)
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(io.LimitReader(r, 8<<20))
	dec.DisallowUnknownFields()
	var req compiler.CompileRequest
	fatalIf(dec.Decode(&req))
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			fatalIf(fmt.Errorf("multiple JSON values in input"))
		}
		fatalIf(err)
	}
	result, err := compiler.Compile(req)
	fatalIf(err)
	if *output == "-" {
		_, err = os.Stdout.Write(append(result, '\n'))
		fatalIf(err)
		return
	}
	tmp := *output + ".tmp"
	fatalIf(os.WriteFile(tmp, append(result, '\n'), 0600))
	fatalIf(os.Rename(tmp, *output))
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "hrcore:", err)
		os.Exit(1)
	}
}
