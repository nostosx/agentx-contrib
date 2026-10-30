// Command integrationcheck requires successful acceptance of both SQL backends.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type event struct {
	Action  string
	Package string
	Test    string
}

type testKey struct {
	Package string
	Test    string
}

var suites = []string{
	"TestSessionStoreConformance", "TestCheckpointStoreConformance",
	"TestWorkflowCheckpointStoreConformance", "TestMemoryStoreConformance",
	"TestTokenStoreConformance",
}

func validate(input io.Reader) (int, error) {
	var required []string
	for _, suite := range suites {
		for _, backend := range []string{"postgresql", "mysql"} {
			required = append(required, suite+"/"+backend)
		}
	}
	return validateRequired(input, required)
}

var pgvectorSuites = []string{
	"TestIntegrationVectorStore", "TestIntegrationMemoryStore",
	"TestIntegrationRetrievalNamespacesAndFilters", "TestIntegrationVectorAtomicReplacement",
}

var documentSuites = []string{"TestIntegrationPDFConversion", "TestIntegrationOCR"}

var retrievalSuites = []string{"TestIntegrationQdrant", "TestIntegrationElasticsearch"}

var debugSuites = []string{"TestIntegrationBrowser"}

func validateRequired(input io.Reader, required []string) (int, error) {
	started, finished := map[testKey]bool{}, map[testKey]bool{}
	passed := map[string]bool{}
	reader := bufio.NewReader(input)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if len(bytes.TrimSpace(line)) != 0 {
			var item event
			if decodeErr := json.Unmarshal(line, &item); decodeErr != nil {
				return 0, fmt.Errorf("invalid integration event: %w", decodeErr)
			}
			if item.Action == "skip" || item.Action == "fail" {
				return 0, fmt.Errorf("integration acceptance contains %s events", item.Action)
			}
			key := testKey{item.Package, item.Test}
			if item.Test != "" && item.Action == "run" {
				started[key] = true
			}
			if item.Action == "pass" {
				passed[item.Test] = true
				if item.Test != "" {
					finished[key] = true
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	for key := range started {
		if !finished[key] {
			return 0, fmt.Errorf("integration acceptance contains unfinished test: %s/%s", key.Package, key.Test)
		}
	}
	for _, name := range required {
		if !passed[name] {
			return 0, fmt.Errorf("missing successful integration suite: %s", name)
		}
	}
	return len(required), nil
}

func run(args []string) error {
	if len(args) != 1 && (len(args) != 2 || (args[1] != "sqlstore" && args[1] != "pgvector" && args[1] != "document" && args[1] != "retrieval" && args[1] != "debug")) {
		return errors.New("usage: integrationcheck result-file [sqlstore|pgvector|document|retrieval|debug]")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer file.Close()
	var count int
	if len(args) == 2 && args[1] == "pgvector" {
		count, err = validateRequired(file, pgvectorSuites)
	} else if len(args) == 2 && args[1] == "document" {
		count, err = validateRequired(file, documentSuites)
	} else if len(args) == 2 && args[1] == "retrieval" {
		count, err = validateRequired(file, retrievalSuites)
	} else if len(args) == 2 && args[1] == "debug" {
		count, err = validateRequired(file, debugSuites)
	} else {
		count, err = validate(file)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Integration acceptance: all %d required suites passed, no skips\n", count)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
