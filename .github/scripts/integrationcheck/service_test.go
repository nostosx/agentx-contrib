package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestServiceSuitesRequired(t *testing.T) {
	t.Parallel()
	for name, required := range map[string][]string{"pgvector": pgvectorSuites, "document": documentSuites, "retrieval": retrievalSuites, "debug": debugSuites} {
		t.Run(name, func(t *testing.T) {
			var complete bytes.Buffer
			for _, suite := range required {
				for _, action := range []string{"run", "pass"} {
					if err := json.NewEncoder(&complete).Encode(event{Action: action, Test: suite}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if n, err := validateRequired(bytes.NewReader(complete.Bytes()), required); err != nil || n != len(required) {
				t.Fatalf("complete: %d %v", n, err)
			}
			if _, err := validateRequired(bytes.NewReader(nil), required); err == nil {
				t.Fatal("empty acceptance succeeded")
			}
			for _, action := range []string{"skip", "fail", "run"} {
				var incomplete bytes.Buffer
				incomplete.Write(complete.Bytes())
				if err := json.NewEncoder(&incomplete).Encode(event{Action: action, Test: "unfinished"}); err != nil {
					t.Fatal(err)
				}
				if _, err := validateRequired(&incomplete, required); err == nil {
					t.Fatalf("accepted %s", action)
				}
			}
		})
	}
}
