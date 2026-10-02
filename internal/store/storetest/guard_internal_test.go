package storetest

import "testing"

// TestLockSchemaRefusesConcurrentReuse is a fast, Postgres-independent unit
// test on the raw locking primitive that
// TestPoolRefusesConcurrentReuseOfTheSameSchemaName (in storetest_test.go)
// exercises end-to-end through Pool. It runs unconditionally, without
// needing WEATHER_TEST_POSTGRES_DSN, so the guard's core logic — refuse a
// second concurrent holder, allow sequential reuse after release — is
// checked even when no database is available locally.
func TestLockSchemaRefusesConcurrentReuse(t *testing.T) {
	const name = "wp_test_internal_lock_probe"

	if !lockSchema(name) {
		t.Fatal("first lockSchema call should succeed")
	}
	if lockSchema(name) {
		// Release what this wrongly-successful call took, so a bug here
		// doesn't also leak the entry into later tests in this package.
		unlockSchema(name)
		t.Fatal("second concurrent lockSchema call for the same name should fail")
	}
	unlockSchema(name)

	if !lockSchema(name) {
		t.Fatal("lockSchema should succeed again after unlockSchema released the name (sequential reuse)")
	}
	unlockSchema(name)
}
