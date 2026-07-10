package mongreldb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	mdb "github.com/visorcraft/mongreldb-go"
)

// These are live integration tests against a real mongreldb-server daemon.
// They boot the daemon from a binary resolved in this order:
//  1. MONGRELDB_SERVER env var (path to the server binary).
//  2. A prebuilt binary at ./bin/mongreldb-server (downloaded by `make server`
//     or the CI workflow).
//  3. mongreldb-server on PATH.
//
// If no binary is available, the suite is skipped. Set MONGRELDB_URL to point
// at an already-running daemon to skip the boot and connect directly.

var (
	testClient *mdb.Client
	testURL    string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// If a daemon is already running, connect to it directly.
	if existing := os.Getenv("MONGRELDB_URL"); existing != "" {
		if reachable(ctx, existing) {
			testURL = existing
			testClient = mdb.NewClient(testURL, mdb.WithToken(os.Getenv("MONGRELDB_TOKEN")))
			os.Exit(m.Run())
		}
		// Asked for a specific URL but it's not up - fail loudly rather than
		// silently booting our own.
		fmt.Fprintf(os.Stderr, "mongreldb: MONGRELDB_URL=%s is not reachable\n", existing)
		os.Exit(1)
	}

	bin, err := resolveServerBinary()
	if err != nil {
		// No daemon available: still run the suite. Live tests self-skip via
		// skipIfNoClient; the offline httptest-based tests run normally.
		fmt.Printf("--- no daemon: %v\n", err)
		os.Exit(m.Run())
	}

	port, err := freePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mongreldb: no free port: %v\n", err)
		os.Exit(1)
	}
	dataDir, err := os.MkdirTemp("", "mongreldb-go-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mongreldb: temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dataDir)

	testURL = "http://127.0.0.1:" + strconv.Itoa(port)
	cmd := exec.Command(bin, dataDir, "--port", strconv.Itoa(port))
	logFile, _ := os.CreateTemp("", "mongreldb-go-server-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "mongreldb: start server: %v\n", err)
		os.Exit(1)
	}

	// Always tear the daemon down.
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		logFile.Close()
	}

	if !waitForHealth(ctx, testURL, 40*time.Second) {
		kill()
		body, _ := os.ReadFile(logFile.Name())
		fmt.Fprintf(os.Stderr, "mongreldb: server did not become healthy. Log:\n%s\n", body)
		os.Exit(1)
	}

	testClient = mdb.NewClient(testURL)
	code := m.Run()
	kill()
	os.Exit(code)
}

// skipIfNoClient skips a test when the suite was unable to boot a daemon.
func skipIfNoClient(t *testing.T) {
	t.Helper()
	if testClient == nil {
		t.Skip("no mongreldb-server available")
	}
}

// resolveServerBinary finds the daemon binary, or returns a skip-worthy error.
func resolveServerBinary() (string, error) {
	if env := os.Getenv("MONGRELDB_SERVER"); env != "" {
		if isExecutable(env) {
			return env, nil
		}
		return "", fmt.Errorf("MONGRELDB_SERVER=%s not found or not executable (live tests skipped)", env)
	}

	local := filepath.Join("bin", "mongreldb-server")
	if isExecutable(local) {
		abs, _ := filepath.Abs(local)
		return abs, nil
	}

	if path, err := exec.LookPath("mongreldb-server"); err == nil {
		return path, nil
	}
	return "", errors.New("mongreldb-server binary not found (set MONGRELDB_SERVER, place it in ./bin, install it on PATH, or set MONGRELDB_URL); live tests skipped")
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func reachable(ctx context.Context, url string) bool {
	c := mdb.NewClient(url, mdb.WithToken(os.Getenv("MONGRELDB_TOKEN")))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, _ := c.Health(ctx)
	return ok
}

func waitForHealth(ctx context.Context, url string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if reachable(ctx, url) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// uniqueTable returns a unique table name per call to isolate each test's data.
func uniqueTable(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// intCol returns a column descriptor for a typed int64 column.
func intCol(id int64, name string, primaryKey bool) mdb.Column {
	return mdb.Column{
		ID: id, Name: name, Type: "int64",
		PrimaryKey: primaryKey, Nullable: false,
	}
}

// floatCol returns a column descriptor for a typed float64 column.
func floatCol(id int64, name string) mdb.Column {
	return mdb.Column{
		ID: id, Name: name, Type: "float64",
		PrimaryKey: false, Nullable: false,
	}
}

// freshTable drops name if present then creates it with columns.
func freshTable(t *testing.T, ctx context.Context, name string, columns ...mdb.Column) {
	t.Helper()
	_ = testClient.DropTable(ctx, name) // ignore "not found"
	if _, err := testClient.CreateTable(ctx, name, columns); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func TestHealth(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	ok, err := testClient.Health(ctx)
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if !ok {
		t.Fatal("expected healthy daemon")
	}
}

func TestCreateTableAndCount(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_tbl")
	freshTable(t, ctx, name, intCol(1, "id", true), floatCol(2, "amount"))

	if n, err := testClient.Count(ctx, name); err != nil {
		t.Fatalf("Count: %v", err)
	} else if n != 0 {
		t.Fatalf("expected 0 rows, got %d", n)
	}
}

func TestPutAndCountRoundTrip(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_put")
	freshTable(t, ctx, name, intCol(1, "id", true), floatCol(2, "amount"))

	if _, err := testClient.Put(ctx, name, mdb.Cells{1: int64(1), 2: 99.5}, ""); err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	if _, err := testClient.Put(ctx, name, mdb.Cells{1: int64(2), 2: 150.0}, ""); err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if n, err := testClient.Count(ctx, name); err != nil {
		t.Fatalf("Count: %v", err)
	} else if n != 2 {
		t.Fatalf("expected 2 rows, got %d", n)
	}
}

func TestQueryByPK(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_pk")
	freshTable(t, ctx, name, intCol(1, "id", true))

	mustPut(t, ctx, name, mdb.Cells{1: int64(42)})
	mustPut(t, ctx, name, mdb.Cells{1: int64(43)})

	rows, err := testClient.Query(name).
		Where("pk", map[string]any{"value": int64(42)}).
		Execute(ctx)
	if err != nil {
		t.Fatalf("query pk: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row, got %d", len(rows))
	}
	// The returned row must carry the queried PK value.
	if got := cellInt64(t, rows[0], 1); got != 42 {
		t.Fatalf("expected pk 42, got %v", got)
	}
}

func TestQueryRange(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_range")
	freshTable(t, ctx, name, intCol(1, "id", true), intCol(2, "amount", false))

	mustPut(t, ctx, name, mdb.Cells{1: int64(1), 2: int64(50)})
	mustPut(t, ctx, name, mdb.Cells{1: int64(2), 2: int64(120)})
	mustPut(t, ctx, name, mdb.Cells{1: int64(3), 2: int64(200)})

	// Range predicate using friendly aliases (column/min/max -> column_id/lo/hi).
	q := testClient.Query(name).
		Where("range", map[string]any{
			"column": int64(2),
			"min":    int64(100),
			"max":    int64(150),
		})
	rows, err := q.Execute(ctx)
	if err != nil {
		t.Fatalf("query range: %v", err)
	}
	// Only the row with amount=120 (pk=2) falls in [100, 150].
	if len(rows) != 1 {
		t.Fatalf("range query should return exactly the matching row, got %d", len(rows))
	}
	if q.Truncated() {
		t.Fatal("result should not be truncated")
	}
	// Verify the PK and amount values of returned rows match the filter range.
	for _, r := range rows {
		if got := cellInt64(t, r, 1); got != 2 {
			t.Fatalf("expected returned pk 2, got %v", got)
		}
		amt := cellInt64(t, r, 2)
		if amt < 100 || amt > 150 {
			t.Fatalf("returned amount %d outside range [100,150]", amt)
		}
	}
}

func TestTransactionPutCommit(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_txn")
	freshTable(t, ctx, name, intCol(1, "id", true))

	txn := testClient.Begin()
	txn.Put(name, mdb.Cells{1: int64(1)}, false)
	txn.Put(name, mdb.Cells{1: int64(2)}, false)
	txn.Put(name, mdb.Cells{1: int64(3)}, false)
	if txn.Count() != 3 {
		t.Fatalf("expected 3 staged ops, got %d", txn.Count())
	}

	results, err := txn.Commit(ctx, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if n, err := testClient.Count(ctx, name); err != nil {
		t.Fatalf("Count: %v", err)
	} else if n != 3 {
		t.Fatalf("expected 3 rows after commit, got %d", n)
	}
}

func TestDeleteByPK(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_del")
	freshTable(t, ctx, name, intCol(1, "id", true))

	mustPut(t, ctx, name, mdb.Cells{1: int64(5)})
	if n, _ := testClient.Count(ctx, name); n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}

	if err := testClient.DeleteByPK(ctx, name, int64(5)); err != nil {
		t.Fatalf("DeleteByPK: %v", err)
	}
	if n, _ := testClient.Count(ctx, name); n != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", n)
	}
}

func TestUpsertUpdatesCellVisibleOnPKQuery(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_upsert")
	freshTable(t, ctx, name, intCol(1, "id", true), floatCol(2, "amount"))

	// Initial insert, then update the amount cell on conflict.
	if _, err := testClient.Upsert(ctx, name, mdb.Cells{1: int64(7), 2: 10.0}, mdb.Cells{2: 10.0}, ""); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if _, err := testClient.Upsert(ctx, name, mdb.Cells{1: int64(7), 2: 99.5}, mdb.Cells{2: 99.5}, ""); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}

	rows, err := testClient.Query(name).
		Where("pk", map[string]any{"value": int64(7)}).
		Execute(ctx)
	if err != nil {
		t.Fatalf("query pk after upsert: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row, got %d", len(rows))
	}
	if got := cellInt64(t, rows[0], 1); got != 7 {
		t.Fatalf("expected pk 7, got %v", got)
	}
	if got := cellFloat64(t, rows[0], 2); got != 99.5 {
		t.Fatalf("expected updated amount 99.5, got %v", got)
	}
}

func TestSQL(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_sql")
	freshTable(t, ctx, name, intCol(1, "id", true), intCol(2, "amount", false))

	if n, _ := testClient.Count(ctx, name); n != 0 {
		t.Fatalf("expected 0 rows, got %d", n)
	}
	// INSERT via SQL must increase the row count.
	if _, err := testClient.SQL(ctx, "INSERT INTO "+name+" (id, amount) VALUES (10, 42)"); err != nil {
		t.Fatalf("SQL INSERT: %v", err)
	}
	if n, _ := testClient.Count(ctx, name); n != 1 {
		t.Fatalf("expected count to increase to 1, got %d", n)
	}
	// JSON SQL mode must return the inserted row.
	rows, err := testClient.SQL(ctx, "SELECT id, amount FROM "+name)
	if err != nil {
		t.Fatalf("SQL SELECT: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from JSON SELECT, got %d", len(rows))
	}
}

func TestSchema(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_schema")
	freshTable(t, ctx, name, intCol(1, "id", true), floatCol(2, "amount"))

	schema, err := testClient.Schema(ctx)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if _, ok := schema[name]; !ok {
		t.Fatalf("schema catalog missing table %q", name)
	}
}

func TestSchemaFor(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_schema_for")
	freshTable(t, ctx, name, intCol(1, "id", true), floatCol(2, "amount"))

	desc, err := testClient.SchemaFor(ctx, name)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if _, ok := desc["schema_id"]; !ok {
		t.Fatalf("descriptor missing schema_id; got %v", desc)
	}
	cols, _ := desc["columns"].([]any)
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(cols))
	}
}

func TestTableNamesListsCreatedTable(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_tables")
	freshTable(t, ctx, name, intCol(1, "id", true))

	names, err := testClient.TableNames(ctx)
	if err != nil {
		t.Fatalf("TableNames: %v", err)
	}
	if !containsString(names, name) {
		t.Fatalf("table list %v missing %q", names, name)
	}
}

func TestErrorOnNonexistentTable(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_missing")
	_, err := testClient.SchemaFor(ctx, name)
	if err == nil {
		t.Fatal("expected error for nonexistent table, got nil")
	}
	if !errors.Is(err, mdb.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestErrorTypeCarriesStatus(t *testing.T) {
	skipIfNoClient(t)
	ctx := context.Background()

	name := uniqueTable("go_missing2")
	_, err := testClient.SchemaFor(ctx, name)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *mdb.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResponseError, got %T (%v)", err, err)
	}
	if re.Status != 404 {
		t.Fatalf("expected status 404, got %d", re.Status)
	}
	// Sentinel must be matchable too.
	if !errors.Is(err, mdb.ErrNotFound) {
		t.Fatalf("errors.Is(err, ErrNotFound) = false; err=%v", err)
	}
}

func TestAuthOptionIsApplied(t *testing.T) {
	// Config-only test (no daemon needed): the client must attach a Bearer
	// header when a token is configured, asserted via an in-process server.
	srv := newRecordingServer(t)
	c := mdb.NewClient(srv.URL, mdb.WithToken("super-secret"))
	ok, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !ok {
		t.Fatal("expected healthy")
	}
	if got := srv.lastAuth; got != "Bearer super-secret" {
		t.Fatalf("expected Bearer auth header, got %q", got)
	}
}

func TestBasicAuthOptionIsApplied(t *testing.T) {
	srv := newRecordingServer(t)
	c := mdb.NewClient(srv.URL, mdb.WithBasicAuth("alice", "s3cret"))
	_, _ = c.Health(context.Background())
	want := "Basic " + base64Std("alice:s3cret")
	if got := srv.lastAuth; got != want {
		t.Fatalf("expected Basic auth header %q, got %q", want, got)
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func mustPut(t *testing.T, ctx context.Context, table string, cells mdb.Cells) {
	t.Helper()
	if _, err := testClient.Put(ctx, table, cells, ""); err != nil {
		t.Fatalf("Put %s: %v", table, err)
	}
}

// cellInt64 extracts an int64 value for colID from a Kit row's flat cells
// array (shape: [col_id, value, ...]). JSON values may arrive as json.Number.
func cellInt64(t *testing.T, row map[string]any, colID int64) int64 {
	t.Helper()
	v := cellValue(row, colID)
	n, err := toInt64(v)
	if err != nil {
		t.Fatalf("cell %d not an int64: %v (%T)", colID, v, v)
	}
	return n
}

// cellFloat64 extracts a float64 value for colID from a Kit row's flat cells
// array (shape: [col_id, value, ...]).
func cellFloat64(t *testing.T, row map[string]any, colID int64) float64 {
	t.Helper()
	v := cellValue(row, colID)
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			t.Fatalf("cell %d not a float64: %v (%T)", colID, v, v)
		}
		return f
	}
	t.Fatalf("cell %d not a float64: %v (%T)", colID, v, v)
	return 0
}

// cellValue looks up a column value in the flat cells array of a Kit row.
func cellValue(row map[string]any, colID int64) any {
	cells, ok := row["cells"].([]any)
	if !ok {
		return nil
	}
	for i := 0; i+1 < len(cells); i += 2 {
		if id, err := toInt64(cells[i]); err == nil && id == colID {
			return cells[i+1]
		}
	}
	return nil
}

// toInt64 coerces a JSON-decoded numeric value to int64.
func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case float64:
		return int64(x), nil
	case json.Number:
		return x.Int64()
	}
	return 0, fmt.Errorf("not numeric: %T", v)
}
