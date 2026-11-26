package rush

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The CI runner user is not the container user that writes the results, so
// every result file must be world-readable (P7b post-report CI fix).
func TestWriteOutputsModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	dir := filepath.Join(t.TempDir(), "run")
	ts := [][]string{{"t_s", "sold"}, {"0", "0"}}
	if err := writeOutputs(dir, &Summary{RunID: "mode-test"}, ts); err != nil {
		t.Fatalf("writeOutputs: %v", err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o055 != 0o055 {
		t.Errorf("dir mode %v: want group and others r-x (0755)", st.Mode().Perm())
	}
	for _, f := range []string{"summary.json", "timeseries.csv", "report.md"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o044 != 0o044 {
			t.Errorf("%s mode %v: want group and others readable (0644)", f, st.Mode().Perm())
		}
	}
}
