//go:build integration

package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/ai"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestMongoAIScripts: the assistant runs scripts on a session that
// refuses every write they attempt.
func TestMongoAIScripts(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	conn := dbtest.Prepare(t, tg)
	tools := &ai.Tools{Conn: conn, Schema: tg.Schema, AllowQueries: true}
	run := func(src string) (string, error) {
		args, _ := json.Marshal(map[string]string{"sql": src})
		return tools.Run(context.Background(), ai.ToolCall{Name: "run_query", Args: args})
	}
	out, err := run(`let n = 0; db.customers.find().forEach(c => { n++ }); [{customers: n}]`)
	if err != nil || !strings.Contains(out, "customers\n3") {
		t.Fatalf("read script: %q %v", out, err)
	}
	for _, w := range []string{
		`for (const id of [1, 2]) db.customers.deleteOne({_id: id})`,
		`let c = db.getCollection("customers"); c.updateOne({_id: 1}, {$set: {name: "x"}})`,
		`db.customers.aggregate([{$out: "stolen"}]).toArray()`,
		`db.runCommand({drop: "customers"})`,
	} {
		if out, err := run(w); err == nil {
			t.Errorf("write script ran: %s → %q", w, out)
		}
	}
	out, err = run(`db.customers.countDocuments({})`)
	if err != nil || !strings.Contains(out, "3") {
		t.Fatalf("documents changed: %q %v", out, err)
	}
	// static statements are still classified before running
	if _, err := run(`db.customers.drop()`); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("static write: %v", err)
	}
}
