package ui

import "testing"

func TestTextBufReplaceUndo(t *testing.T) {
	b := newTextBuf("select 1;\nfrom t")
	var spliced [][3]int
	b.onSplice = func(f, r, a int) { spliced = append(spliced, [3]int{f, r, a}) }
	end := b.replace(tpos{0, 7}, tpos{0, 8}, "a,\n  b", tpos{0, 8}, tpos{0, 7}, true)
	if b.text() != "select a,\n  b;\nfrom t" || end != (tpos{1, 3}) {
		t.Fatalf("got %q end %v", b.text(), end)
	}
	if len(spliced) != 1 || spliced[0] != [3]int{0, 1, 2} {
		t.Fatalf("splices %v", spliced)
	}
	c, an, ok := b.undoStep()
	if !ok || b.text() != "select 1;\nfrom t" || c != (tpos{0, 8}) || an != (tpos{0, 7}) {
		t.Fatalf("undo: %q %v %v", b.text(), c, an)
	}
	if _, _, ok := b.redoStep(); !ok || b.text() != "select a,\n  b;\nfrom t" {
		t.Fatalf("redo: %q", b.text())
	}
}

func TestTextBufTypingGroups(t *testing.T) {
	b := newTextBuf("")
	p := tpos{}
	for _, ch := range []string{"s", "e", "l"} {
		p = b.replace(p, p, ch, p, p, false)
	}
	b.breakGroup()
	p = b.replace(p, p, "x", p, p, false)
	b.undoStep()
	if b.text() != "sel" {
		t.Fatalf("first undo: %q", b.text())
	}
	b.undoStep()
	if b.text() != "" {
		t.Fatalf("second undo: %q", b.text())
	}
}

func TestTextBufMotion(t *testing.T) {
	b := newTextBuf("  foo_bar(baz)  qux\nnext")
	if p := b.wordRight(tpos{0, 0}); p != (tpos{0, 9}) {
		t.Fatalf("wordRight %v", p)
	}
	if p := b.wordLeft(tpos{0, 9}); p != (tpos{0, 2}) {
		t.Fatalf("wordLeft %v", p)
	}
	if p := b.right(tpos{0, len(b.lines[0])}); p != (tpos{1, 0}) {
		t.Fatalf("right across lines %v", p)
	}
	s, e := b.wordAt(tpos{0, 5})
	if b.slice(s, e) != "foo_bar" {
		t.Fatalf("wordAt %q", b.slice(s, e))
	}
	a, m, ok := b.matchBracket(tpos{0, 9})
	if !ok || a != (tpos{0, 9}) || m != (tpos{0, 13}) {
		t.Fatalf("bracket %v %v %v", a, m, ok)
	}
	if off := b.offset(tpos{1, 2}); b.posAt(off) != (tpos{1, 2}) || off != len(b.lines[0])+3 {
		t.Fatalf("offset %d", off)
	}
}

func TestTextBufUnicode(t *testing.T) {
	b := newTextBuf("àé")
	if p := b.right(tpos{0, 0}); p.col != 2 {
		t.Fatalf("right over multibyte rune: %v", p)
	}
	if p := b.clamp(tpos{0, 1}); p.col != 0 {
		t.Fatalf("clamp inside rune: %v", p)
	}
}
