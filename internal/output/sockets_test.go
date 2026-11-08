package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/sockets"
)

func TestResultItem_PublishedSocketsAdditiveJSON(t *testing.T) {
	uid := 1000
	gid := 1000
	env := NewEnvelope("status", "lab1")
	env.Results = []ResultItem{{
		Container: "lab1",
		Action:    "Running",
		OK:        true,
		PublishedSockets: []sockets.PublishedSocket{{
			Device:  "socket-broker",
			Name:    "broker",
			Listen:  "unix:/run/sshigit/broker.sock",
			Connect: "unix:/run/sshigit-broker/lab1.sock",
			Bind:    "instance",
			UID:     &uid,
			GID:     &gid,
			Mode:    "0660",
			Managed: true,
		}},
	}}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	jsonStr := string(data)
	want := `"published_sockets":[{"device":"socket-broker","name":"broker","listen":"unix:/run/sshigit/broker.sock","connect":"unix:/run/sshigit-broker/lab1.sock","bind":"instance","uid":1000,"gid":1000,"mode":"0660","managed":true}]`
	if !strings.Contains(jsonStr, want) {
		t.Fatalf("published_sockets JSON mismatch\nwant substring: %s\ngot: %s", want, jsonStr)
	}

	// Optional fields are omitted when undeclared.
	data, err = json.Marshal(NewEnvelope("status", "lab2"))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), "published_sockets") {
		t.Fatalf("empty envelope must omit published_sockets: %s", data)
	}
}
