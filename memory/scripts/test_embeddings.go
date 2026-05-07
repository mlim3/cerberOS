//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	baseURL = "http://localhost:8080/api/v1"
	userID  = "22222222-2222-2222-2222-222222222222" // Bob from seed.sql
)

func main() {
	// 1. Save personal info about a specific topic
	fmt.Println("Saving personal info 1: I love eating Italian food, especially pizza and pasta.")
	saveReq1 := map[string]interface{}{
		"content":      "I love eating Italian food, especially pizza and pasta.",
		"sourceType":   "chat",
		"sourceId":     "22222222-2222-2222-2222-222222222222",
		"extractFacts": true,
	}
	saveInfo(saveReq1)

	// Add a slight delay to ensure different creation times
	time.Sleep(1 * time.Second)

	fmt.Println("\nSaving personal info 2: I am a software engineer writing Go code.")
	saveReq2 := map[string]interface{}{
		"content":      "I am a software engineer writing Go code.",
		"sourceType":   "chat",
		"sourceId":     "22222222-2222-2222-2222-222222222222",
		"extractFacts": true,
	}
	saveInfo(saveReq2)

	time.Sleep(1 * time.Second)

	// 2. Query for semantically related info
	// Should match the first save
	queryText1 := "What kind of cuisine do I enjoy?"
	fmt.Printf("\nQuerying: '%s'\n", queryText1)
	queryInfo(queryText1)

	fmt.Println("--------------------------------")

	// Should match the second save
	queryText2 := "What is my profession?"
	fmt.Printf("\nQuerying: '%s'\n", queryText2)
	queryInfo(queryText2)
}

func saveInfo(reqData map[string]interface{}) {
	reqBody, _ := json.Marshal(reqData)
	resp, err := http.Post(fmt.Sprintf("%s/personal_info/%s/save", baseURL, userID), "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatalf("Failed to save info: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("Save returned status %d: %s\n", resp.StatusCode, string(body))
	} else {
		fmt.Println("Save successful!")
	}
}

func queryInfo(query string) {
	reqData := map[string]interface{}{
		"query": query,
		"topK":  2, // Get top 2 results to see difference in similarity
	}
	reqBody, _ := json.Marshal(reqData)
	resp, err := http.Post(fmt.Sprintf("%s/personal_info/%s/query", baseURL, userID), "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatalf("Failed to query info: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Query returned status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	data := result["data"].(map[string]interface{})
	results := data["results"].([]interface{})

	for i, r := range results {
		res := r.(map[string]interface{})
		fmt.Printf("Match %d (Similarity: %.4f): %s\n", i+1, res["similarityScore"], res["text"])
	}
}
