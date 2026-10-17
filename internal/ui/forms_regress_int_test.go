//go:build dev && integration

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/ai"
	"github.com/twoojoo/lumino/internal/db"
)

// askAboutObject targets the connection of the object, not the current one.
func TestFormsRegressAskAboutObjectTargetsConn(t *testing.T) {
	path := sqliteFixture(t, 3)
	h := newHarness(t, []db.Config{
		{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path},
		{ID: "other", Name: "other sqlite", Driver: "sqlite", File: path},
	}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })
	if a.currentConn() != "lite" {
		t.Fatalf("current conn %q", a.currentConn())
	}
	// no provider call can happen: an OpenAI-compatible preset without a
	// model fails in Build, before any request
	a.ai.cfg = &ai.Config{Provider: "openai", Providers: map[string]ai.ProviderConfig{}, DisabledConnections: []string{"other"}}
	ref := db.ObjectRef{Schema: "main", Name: "items", Type: db.ObjTable}

	a.askAboutObject("other", ref)
	if n := len(a.ai.items); n == 0 || !strings.Contains(a.ai.items[n-1].text, "other sqlite") {
		t.Fatalf("question not sent to the object's connection: %+v", a.ai.items)
	}

	// enabled but not connected: it connects that connection (not the
	// current one) and the queued question goes there
	a.ai.cfg.DisabledConnections = nil
	before := len(a.ai.items)
	a.askAboutObject("other", ref)
	h.waitFor("other connected", func() bool { return a.connState("other").status == stReady })
	h.waitFor("answer", func() bool { return len(a.ai.items) > before })
	if it := a.ai.items[len(a.ai.items)-1]; it.kind != chatError || !strings.Contains(it.text, "model") {
		t.Fatalf("unexpected item %+v", it)
	}

	// while a run is active the request is not dropped silently
	a.ai.running = true
	before = len(a.ai.items)
	a.askAboutObject("lite", ref)
	a.ai.running = false
	if len(a.ai.items) != before || !a.statusErr || !strings.Contains(a.status, "still answering") {
		t.Fatalf("busy assistant: status %q items %d→%d", a.status, before, len(a.ai.items))
	}
	h.settle(50 * time.Millisecond)
}
