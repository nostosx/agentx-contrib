package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	var lines []string
	for _, suite := range suites {
		for _, backend := range []string{"postgresql", "mysql"} {
			for _, action := range []string{"run", "pass"} {
				data, err := json.Marshal(event{Action: action, Package: "a", Test: suite + "/" + backend})
				if err != nil {
					t.Fatal(err)
				}
				lines = append(lines, string(data))
			}
		}
	}
	valid := strings.Join(lines, "\n")
	for _, tt := range []struct {
		name  string
		input string
		ok    bool
	}{
		{name: "complete_without_final_newline", input: valid, ok: true},
		{name: "blank_lines", input: "\n" + valid + "\n\n", ok: true},
		{name: "malformed", input: valid + "\nnot JSON"},
		{name: "package_isolation", input: valid + "\n" + `{"Action":"run","Package":"b","Test":"TestMemoryStoreConformance/mysql"}`},
		{name: "package_skip", input: valid + "\n" + `{"Action":"skip","Package":"b"}`},
		{name: "package_failure", input: valid + "\n" + `{"Action":"fail","Package":"b"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			count, err := validate(strings.NewReader(tt.input))
			if (err == nil) != tt.ok || (tt.ok && count != len(suites)*2) {
				t.Fatalf("count=%d error=%v", count, err)
			}
		})
	}
}
