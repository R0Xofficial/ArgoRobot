package main

import (
	"context"
	"testing"

	"argo/internal/db"
)

func cleanDB(t *testing.T, url string) {
	t.Helper()
	d, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	defer d.Close()
	if _, err := d.Exec(context.Background(), "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
}
