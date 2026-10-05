package history

import (
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	for i := 0; i < MaxEntries+10; i++ {
		if err := Add("conn/1", Entry{At: time.Now(), SQL: "select " + string(rune('a'+i%26)), Rows: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = Add("conn/1", Entry{SQL: "   "})
	es, err := Load("conn/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != MaxEntries || es[0].Rows != MaxEntries+9 {
		t.Fatalf("%d entries, newest %d", len(es), es[0].Rows)
	}
	if err := Clear("conn/1"); err != nil {
		t.Fatal(err)
	}
	if es, _ := Load("conn/1"); len(es) != 0 {
		t.Fatal("not cleared")
	}
}
