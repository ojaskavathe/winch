package main

import "testing"

func TestFitLayoutGrows(t *testing.T) {
	// 200x49: a 26-col left pane and a right column split top/bottom.
	body := "200x49,0,0{26x49,0,0,1,173x49,27,0[173x24,27,0,2,173x24,27,25,3]}"
	in := lchecksum(body) + "," + body
	out, err := fitLayout(in, 250, 59)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := layoutDims(out); w != 250 || h != 59 {
		t.Fatalf("fitted to %dx%d, want 250x59: %s", w, h, out)
	}
	_, got, _ := cutChecksum(out)
	root, err := (&lparser{s: got}).node()
	if err != nil {
		t.Fatal(err)
	}
	l, r := root.kids[0], root.kids[1]
	if l.w+1+r.w != 250 || r.x != l.w+1 {
		t.Fatalf("row does not tile 250 cols: %s", got)
	}
	top, bot := r.kids[0], r.kids[1]
	if top.h+1+bot.h != 59 || bot.y != top.h+1 || top.w != r.w {
		t.Fatalf("column does not tile 59 rows: %s", got)
	}
	if l.h != 59 {
		t.Fatalf("left pane height %d, want 59", l.h)
	}
	if lchecksum(got) != out[:4] {
		t.Fatalf("checksum does not match body: %s", out)
	}
}

// A layout that already fits must come back byte for byte: the exact restore
// is the whole point of saving it.
func TestFitLayoutNoopWhenFits(t *testing.T) {
	body := "200x49,0,0{26x49,0,0,1,173x49,27,0,2}"
	in := lchecksum(body) + "," + body
	if out, _ := fitLayout(in, 200, 49); out != in {
		t.Fatalf("rewrote a fitting layout: %s -> %s", in, out)
	}
}

func cutChecksum(s string) (string, string, bool) {
	if len(s) < 5 || s[4] != ',' {
		return "", "", false
	}
	return s[:4], s[5:], true
}
