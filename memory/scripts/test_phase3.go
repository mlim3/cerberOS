//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const baseURL = "http://localhost:8080/api/v1"
const userID = "123e4567-e89b-12d3-a456-426614174000" // Example UUID

func main() {
	fmt.Println("Starting Phase 3 Integration Tests...")

	// 1. Traceability Test (/save)
	fmt.Println("\n--- Testing Traceability (/save) ---")
	saveReq := map[string]interface{}{
		"content":      "I love programming in Go.",
		"sourceType":   "chat",
		"sourceId":     "123e4567-e89b-12d3-a456-426614174001",
		"extractFacts": true,
	}
	saveBody, _ := json.Marshal(saveReq)
	resp, err := http.Post(fmt.Sprintf("%s/personal_info/%s/save", baseURL, userID), "application/json", bytes.NewBuffer(saveBody))
	if err != nil {
		fmt.Printf("Error calling /save: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %d\nResponse: %s\n", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		fmt.Println("Failed traceability test: unexpected status code.")
		os.Exit(1)
	}

	var saveResult struct {
		Data struct {
			FactIds []string `json:"factIds"`
		} `json:"data"`
	}
	json.Unmarshal(body, &saveResult)

	if len(saveResult.Data.FactIds) == 0 {
		fmt.Println("Warning: No facts extracted. Concurrency test will be skipped.")
	} else {
		factID := saveResult.Data.FactIds[0]

		// 2. Concurrency Test (PUT /facts/{factId})
		fmt.Println("\n--- Testing Concurrency (PUT /facts) ---")

		fmt.Println("Fetching all facts to get current version...")
		respAll, err := http.Get(fmt.Sprintf("%s/personal_info/%s/all", baseURL, userID))
		if err != nil {
			fmt.Printf("Error calling /all: %v\n", err)
			os.Exit(1)
		}
		defer respAll.Body.Close()
		allBody, _ := io.ReadAll(respAll.Body)

		var allResult struct {
			Data struct {
				Facts []struct {
					FactId  string `json:"factId"`
					Version int32  `json:"version"`
				} `json:"facts"`
			} `json:"data"`
		}
		json.Unmarshal(allBody, &allResult)

		var currentVersion int32 = -1
		for _, f := range allResult.Data.Facts {
			if f.FactId == factID {
				currentVersion = f.Version
				break
			}
		}

		if currentVersion == -1 {
			fmt.Println("Failed to find the newly created fact.")
			os.Exit(1)
		}
		fmt.Printf("Found fact %s with version %d\n", factID, currentVersion)

		// Update fact successfully
		updateReq := map[string]interface{}{
			"category":   "preferences",
			"factKey":    "favorite_language",
			"factValue":  "Go (updated)",
			"confidence": 0.95,
			"version":    currentVersion,
		}
		updateBody, _ := json.Marshal(updateReq)
		reqUpdate, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/personal_info/%s/facts/%s", baseURL, userID, factID), bytes.NewBuffer(updateBody))
		reqUpdate.Header.Set("Content-Type", "application/json")

		respUpdate, _ := http.DefaultClient.Do(reqUpdate)
		defer respUpdate.Body.Close()
		uBody, _ := io.ReadAll(respUpdate.Body)
		fmt.Printf("Update 1 Status: %d\nResponse: %s\n", respUpdate.StatusCode, string(uBody))

		if respUpdate.StatusCode != http.StatusOK {
			fmt.Println("First update failed!")
			os.Exit(1)
		}

		// Try updating again with the same (now stale) version
		fmt.Println("\nTrying update with stale version to trigger 409 Conflict...")
		reqConflict, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/personal_info/%s/facts/%s", baseURL, userID, factID), bytes.NewBuffer(updateBody))
		reqConflict.Header.Set("Content-Type", "application/json")

		respConflict, _ := http.DefaultClient.Do(reqConflict)
		defer respConflict.Body.Close()
		cBody, _ := io.ReadAll(respConflict.Body)
		fmt.Printf("Update 2 Status: %d\nResponse: %s\n", respConflict.StatusCode, string(cBody))

		if respConflict.StatusCode == http.StatusConflict {
			fmt.Println("Concurrency test passed: Received 409 Conflict.")
		} else {
			fmt.Println("Concurrency test failed: Expected 409 Conflict.")
			os.Exit(1)
		}
	}

	// 3. Semantic Retrieval Test (/query)
	fmt.Println("\n--- Testing Semantic Retrieval (/query) ---")
	queryReq := map[string]interface{}{
		"query": "What language do I love?",
		"topK":  2,
	}
	queryBody, _ := json.Marshal(queryReq)
	respQuery, err := http.Post(fmt.Sprintf("%s/personal_info/%s/query", baseURL, userID), "application/json", bytes.NewBuffer(queryBody))
	if err != nil {
		fmt.Printf("Error calling /query: %v\n", err)
		os.Exit(1)
	}
	defer respQuery.Body.Close()

	qBody, _ := io.ReadAll(respQuery.Body)
	fmt.Printf("Query Status: %d\nResponse: %s\n", respQuery.StatusCode, string(qBody))

	if respQuery.StatusCode != http.StatusOK {
		fmt.Println("Query test failed: expected 200 OK.")
		os.Exit(1)
	}

	var queryResult struct {
		Data struct {
			Results []struct {
				SourceReferences []interface{} `json:"sourceReferences"`
			} `json:"results"`
		} `json:"data"`
	}
	json.Unmarshal(qBody, &queryResult)

	hasSourceRefs := false
	for _, res := range queryResult.Data.Results {
		if len(res.SourceReferences) > 0 {
			hasSourceRefs = true
			break
		}
	}

	if hasSourceRefs {
		fmt.Println("Semantic Retrieval test passed: Found source references in query results.")
	} else {
		fmt.Println("Semantic Retrieval test failed: No source references found in results.")
		os.Exit(1)
	}

	fmt.Println("\nAll Phase 3 Integration Tests Passed Successfully!")
}
