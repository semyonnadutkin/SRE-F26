package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestJSONLogging(t *testing.T) {
	var output bytes.Buffer
	newLogger(&output).Error("request failed\nsecond line", "operation", "upload")
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatal("each event must occupy one line")
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"level": "error", "message": "request failed\nsecond line",
		"service.name": "fileserver", "operation": "upload",
	} {
		if event[key] != want {
			t.Errorf("%s = %v; want %q", key, event[key], want)
		}
	}
	timestamp, ok := event["timestamp"].(string)
	if !ok {
		t.Fatal("missing timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		t.Fatal(err)
	}
}
