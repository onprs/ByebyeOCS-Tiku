package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessRequiresCompleteMatchingSchema(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	ready := func(expected int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if recorder.Code != expected {
			t.Fatalf("就绪状态错误：%d/%s", recorder.Code, recorder.Body.String())
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	ready(http.StatusOK)
	var checksum string
	if err := f.pool.QueryRow(ctx, `SELECT checksum FROM tiku.schema_migrations WHERE version=1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE tiku.schema_migrations SET checksum=repeat('0',64) WHERE version=1`)
	ready(http.StatusServiceUnavailable)
	exec(`UPDATE tiku.schema_migrations SET checksum=$1 WHERE version=1`, checksum)
	exec(`INSERT INTO tiku.schema_migrations(version,name,checksum) VALUES(999,'future',repeat('a',64))`)
	ready(http.StatusServiceUnavailable)
	exec(`DELETE FROM tiku.schema_migrations WHERE version=999`)
	ready(http.StatusOK)
	exec(`DROP SCHEMA tiku CASCADE`)
	if err := f.store.CheckSchema(ctx); err == nil {
		t.Fatal("空库未被启动检查拒绝")
	}
	ready(http.StatusServiceUnavailable)
	live := httptest.NewRecorder()
	f.handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if live.Code != http.StatusOK {
		t.Fatal("存活检查与就绪检查没有分开")
	}
}
