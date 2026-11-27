package testutil

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

var schemas atomic.Int64

// LiveStore opens a migrated store on an empty PostgreSQL schema of its own,
// dropped when the test ends, and skips with skip when
// ADAPTER_TEST_DATABASE_URL is unset.
//
// go test runs packages in parallel against the one test database, and the
// store tests drop every table to check recovery. A test on the shared schema
// would lose its tables mid-run, and a sweep across every row would find what
// other tests left behind.
func LiveStore(t *testing.T, skip string) *store.Store {
	t.Helper()

	base := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if base == "" {
		t.Skip(skip)
	}
	name := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), schemas.Add(1))

	admin, err := store.Open(t.Context(), store.Options{URL: base})
	require.NoError(t, err)
	require.NoError(t, admin.Gorm().WithContext(t.Context()).Exec("CREATE SCHEMA "+name).Error)
	t.Cleanup(func() {
		_ = admin.Gorm().Exec("DROP SCHEMA " + name + " CASCADE").Error
		_ = admin.Close()
	})

	parsed, err := url.Parse(base)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()

	db, err := store.Open(t.Context(), store.Options{URL: parsed.String()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	return db
}
