package client

// In-process test environment: every client integration test runs against a
// real triplestore (webservice + api, disk-backed, temp dir) and a faithful
// filehost stand-in started inside the test binary, so the suite never skips
// and never needs test-env/start.sh. The servers themselves live in
// internal/testenv so other packages can share one harness. Set
// TIE_TEST_WEBSERVICE / TIE_TEST_FILEHOST to run against external servers.

import (
	"os"
	"testing"

	"github.com/uidbz/tie/internal/testenv"
)

func TestMain(m *testing.M) {
	if ws, fh := os.Getenv("TIE_TEST_WEBSERVICE"), os.Getenv("TIE_TEST_FILEHOST"); ws != "" && fh != "" {
		testingTripleStoreURL, testingFileHostURL = ws, fh
		os.Exit(m.Run())
	}

	env, shutdown, err := testenv.Start()
	if err != nil {
		panic(err)
	}
	testingTripleStoreURL, testingFileHostURL = env.TripleStoreURL, env.FileHostURL
	code := m.Run()
	shutdown()
	os.Exit(code)
}
