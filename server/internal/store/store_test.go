package store

import (
	"errors"
	"testing"
)

func TestIsUnique(t *testing.T) {
	cases := map[string]string{
		"constraint failed: UNIQUE constraint failed: users.email (2067)":       "users.email",
		"UNIQUE constraint failed: moments.author_id, moments.client_id (2067)": "moments.author_id, moments.client_id",
		"sqlite: step: UNIQUE constraint failed: users.username":                "users.username",
	}
	for msg, want := range cases {
		col, ok := isUnique(errors.New(msg))
		if !ok || col != want {
			t.Errorf("isUnique(%q) = %q, %v", msg, col, ok)
		}
	}
	if _, ok := isUnique(errors.New("FOREIGN KEY constraint failed")); ok {
		t.Error("FK is not unique")
	}
}
