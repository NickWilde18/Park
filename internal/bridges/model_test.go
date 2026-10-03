package bridges

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A model's answer may come in a code block, or after other words; what a round keeps of it is only the pull requests
// it was given, each in its first group, and groups of two or more, named as the model named them, trimmed.
func TestGroups(t *testing.T) {
	got, err := groups("Here you go:\n```json\n"+`{"groups": [{"name": " Exports ", "pull_requests": ["acme/api#1", "acme/web#3", "other/x#9"]},`+
		`{"name": "Retries", "pull_requests": ["acme/api#1", "acme/api#2", "acme/web#4"]}, {"name": "Alone", "pull_requests": ["acme/web#5"]}]}`+
		"\n```", []string{"acme/api#1", "acme/api#2", "acme/web#3", "acme/web#4", "acme/web#5"})
	want := []Group{{Name: "Exports", PRs: []string{"acme/api#1", "acme/web#3"}}, {Name: "Retries", PRs: []string{"acme/api#2", "acme/web#4"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := groups("I cannot tell.", nil); err == nil {
		t.Fatal("an answer without JSON was read")
	}
}

// A model is asked through the chat completions protocol: the model's name, the instructions, and the thread's text
// with its pull requests, with the key as a bearer token. An error status comes back with the service's message, cut
// short.
func TestSplit(t *testing.T) {
	var asked struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&asked)
		if strings.Contains(asked.Messages[1].Content, "fail") {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "` + strings.Repeat("slow down ", 40) + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "{\"groups\": [{\"name\": \"Exports\", \"pull_requests\": [\"acme/api#1\", \"acme/web#3\"]}]}"}}]}`))
	}))
	defer srv.Close()
	m := newModel(srv.URL+"/v1/", "a-model", "the key", 5*time.Second)
	got, err := m.Split(context.Background(), "Exports, front and back", []string{"acme/api#1", "acme/web#3"})
	if err != nil || len(got) != 1 || got[0].Name != "Exports" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if path != "/v1/chat/completions" || auth != "Bearer the key" || asked.Model != "a-model" || asked.Messages[0].Role != "system" ||
		!strings.Contains(asked.Messages[1].Content, "Exports, front and back") || !strings.Contains(asked.Messages[1].Content, "acme/web#3") {
		t.Fatalf("asked %s with %q: %+v", path, auth, asked)
	}
	_, err = m.Split(context.Background(), "fail", []string{"acme/api#1", "acme/web#3"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "…") || len(err.Error()) > 260 {
		t.Fatalf("got %v, want the status and the message cut short", err)
	}
}
