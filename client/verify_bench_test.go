package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blobCheckFixture builds a client whose default filehost is a local stub
// answering HEAD for every content address (one in ten answers 404, so the
// result is not empty), plus n file rows to check.
func blobCheckFixture(b *testing.B, n int) (*TieClient, []Row) {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real filehost costs a round trip; without it the benchmark measures
		// a localhost stub and concurrency has nothing to hide.
		time.Sleep(blobCheckRTT)
		if strings.HasSuffix(r.URL.Path, "0") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", "10")
	}))
	b.Cleanup(srv.Close)

	cfg := verifyTestConfig()
	cfg.FileHosts = map[string]FileHost{"default": {URL: srv.URL}}
	cfg.DefaultFileHosts = []string{"default"}
	tc := NewTieClient(cfg)

	rows := make([]Row, n)
	for i := range rows {
		rows[i] = Row{Key: fmt.Sprintf("%064x", i)}
	}
	return tc, rows
}

// blobCheckRows is the sample size and blobCheckRTT the modeled round trip:
// 400 HEADs at 5 ms cost 2 s one at a time and about 130 ms with 16 in flight.
const (
	blobCheckRows = 400
	blobCheckRTT  = 5 * time.Millisecond
)

// BenchmarkCheckBlobsPooled measures the worker pool; BenchmarkCheckBlobsSequential
// measures what it replaces — one HEAD per round trip (§5.2 item 7).
func BenchmarkCheckBlobsPooled(b *testing.B) {
	tc, rows := blobCheckFixture(b, blobCheckRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tc.checkBlobsExist(rows); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckBlobsSequential(b *testing.B) {
	tc, rows := blobCheckFixture(b, blobCheckRows)
	fh := FileHostClient(tc.Config.FileHosts["default"])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, row := range rows {
			if _, _, err := fh.Stat(row.Key); err != nil {
				b.Fatal(err)
			}
		}
	}
}
