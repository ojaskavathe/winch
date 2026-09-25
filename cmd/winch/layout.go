package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Mini layout engine for docked-mode give-back. When the user reshapes a
// window WHILE the sidebar is docked (tmux-equalize-nvim sets
// @winch_layout_dirty), restoring the pre-dock snapshot on leave would undo
// their change. Instead: parse the docked #{window_layout}, drop the sidebar
// leaf, rescale what remains to the full window width, and apply that.
// Heights are untouched — the sidebar only ever takes width.
//
// Parser/renderer mirror tmux's layout_dump format (and the copy in
// tmux-equalize-nvim): WxH,X,Y then ",pane" for a leaf or {..}/[..] for
// horizontal/vertical splits. select-layout assigns cells to panes by list
// order, so this is only ever applied AFTER the sidebar has left the window,
// when list order matches the string's geometric order again.

type lnode struct {
	kind byte // 'l' leaf, '{' row, '[' column
	w, h int
	x, y int
	pane string
	kids []*lnode
}

type lparser struct {
	s string
	i int
}

func (p *lparser) num() (int, error) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("layout: number expected at %d", p.i)
	}
	return strconv.Atoi(p.s[start:p.i])
}

func (p *lparser) expect(c byte) error {
	if p.i >= len(p.s) || p.s[p.i] != c {
		return fmt.Errorf("layout: %q expected at %d", c, p.i)
	}
	p.i++
	return nil
}

func (p *lparser) node() (*lnode, error) {
	n := &lnode{}
	var err error
	if n.w, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect('x'); err != nil {
		return nil, err
	}
	if n.h, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect(','); err != nil {
		return nil, err
	}
	if n.x, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect(','); err != nil {
		return nil, err
	}
	if n.y, err = p.num(); err != nil {
		return nil, err
	}
	if p.i >= len(p.s) {
		return nil, errors.New("layout: truncated")
	}
	switch c := p.s[p.i]; c {
	case ',':
		p.i++
		id, err := p.num()
		if err != nil {
			return nil, err
		}
		n.kind = 'l'
		n.pane = strconv.Itoa(id)
		return n, nil
	case '{', '[':
		p.i++
		n.kind = c
		end := byte('}')
		if c == '[' {
			end = ']'
		}
		for {
			kid, err := p.node()
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, kid)
			if p.i >= len(p.s) {
				return nil, errors.New("layout: unterminated split")
			}
			if p.s[p.i] == end {
				p.i++
				return n, nil
			}
			if err := p.expect(','); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("layout: unexpected %q at %d", c, p.i)
	}
}

func lrender(n *lnode) string {
	head := fmt.Sprintf("%dx%d,%d,%d", n.w, n.h, n.x, n.y)
	if n.kind == 'l' {
		return head + "," + n.pane
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte(n.kind)
	for i, kid := range n.kids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(lrender(kid))
	}
	if n.kind == '{' {
		b.WriteByte('}')
	} else {
		b.WriteByte(']')
	}
	return b.String()
}

// lchecksum is tmux's layout checksum (same as layout_checksum).
func lchecksum(body string) string {
	var sum uint16
	for _, r := range body {
		sum = (sum >> 1) + ((sum & 1) << 15)
		sum += uint16(r)
	}
	return fmt.Sprintf("%04x", sum)
}

// scaleX resizes a subtree to a new x/width, proportionally, leaving
// vertical geometry alone.
func scaleX(n *lnode, x, w int) {
	n.x, n.w = x, w
	switch n.kind {
	case '[':
		for _, kid := range n.kids {
			scaleX(kid, x, w)
		}
	case '{':
		old := make([]int, len(n.kids))
		for i, kid := range n.kids {
			old[i] = kid.w
		}
		cx := x
		for i, sz := range apportion(old, w-(len(n.kids)-1)) {
			scaleX(n.kids[i], cx, sz)
			cx += sz + 1
		}
	}
}

// scaleY is scaleX turned on its side: a new y/height, proportionally,
// leaving horizontal geometry alone.
func scaleY(n *lnode, y, h int) {
	n.y, n.h = y, h
	switch n.kind {
	case '{':
		for _, kid := range n.kids {
			scaleY(kid, y, h)
		}
	case '[':
		old := make([]int, len(n.kids))
		for i, kid := range n.kids {
			old[i] = kid.h
		}
		cy := y
		for i, sz := range apportion(old, h-(len(n.kids)-1)) {
			scaleY(n.kids[i], cy, sz)
			cy += sz + 1
		}
	}
}

// apportion splits avail cells across siblings in proportion to their old
// sizes (largest remainder), every sibling keeping at least one cell.
func apportion(old []int, avail int) []int {
	total := 0
	for _, o := range old {
		total += o
	}
	if total <= 0 || avail < len(old) {
		avail = max(avail, len(old))
		total = 0
		for i := range old {
			old[i] = 1
			total++
		}
	}
	sizes := make([]int, len(old))
	rem := make([]float64, len(old))
	sum := 0
	for i, o := range old {
		exact := float64(avail) * float64(o) / float64(total)
		sizes[i] = max(1, int(exact))
		rem[i] = exact - float64(int(exact))
		sum += sizes[i]
	}
	for sum < avail {
		best := 0
		for i := range rem {
			if rem[i] > rem[best] {
				best = i
			}
		}
		sizes[best]++
		rem[best] = -1
		sum++
	}
	for sum > avail {
		big := 0
		for i := range sizes {
			if sizes[i] > sizes[big] {
				big = i
			}
		}
		if sizes[big] <= 1 {
			break
		}
		sizes[big]--
		sum--
	}
	return sizes
}

// fitLayout rescales a whole layout to a w x h window. A layout string records
// the window size it was taken at, and tmux's select-layout applies it as
// written: replay a layout saved before the client grew (a monitor switch while
// docked) and the panes stay laid out for the old size inside the bigger
// window — the dead margin, and every pane "resized" wrong. Anything winch
// replays goes through here first. A layout that already fits is returned
// untouched, byte for byte, so the exact restore stays exact.
func fitLayout(layout string, w, h int) (string, error) {
	if w <= 0 || h <= 0 {
		return layout, nil
	}
	if lw, lh := layoutDims(layout); lw == w && lh == h {
		return layout, nil
	}
	_, body, ok := strings.Cut(layout, ",")
	if !ok {
		return "", fmt.Errorf("layout: no checksum in %q", layout)
	}
	root, err := (&lparser{s: body}).node()
	if err != nil {
		return "", err
	}
	scaleX(root, 0, w)
	scaleY(root, 0, h)
	body = lrender(root)
	return lchecksum(body) + "," + body, nil
}

// sansSidebar takes a docked window's layout (checksum,body) and the sidebar
// pane id (%N), removes the sidebar leaf, and rescales the remaining panes
// across the full window width. Returns a complete checksum,body string for
// select-layout.
func sansSidebar(layout, sidebarPane string) (string, error) {
	_, body, ok := strings.Cut(layout, ",")
	if !ok {
		return "", fmt.Errorf("layout: no checksum in %q", layout)
	}
	root, err := (&lparser{s: body}).node()
	if err != nil {
		return "", err
	}
	id := strings.TrimPrefix(sidebarPane, "%")
	if root.kind != '{' {
		return "", errors.New("layout: root is not a row; sidebar not found")
	}
	idx := -1
	for i, kid := range root.kids {
		if kid.kind == 'l' && kid.pane == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", errors.New("layout: sidebar is not a direct root child")
	}
	kids := append(append([]*lnode{}, root.kids[:idx]...), root.kids[idx+1:]...)
	if len(kids) == 0 {
		return "", errors.New("layout: nothing left without the sidebar")
	}
	if len(kids) == 1 {
		n := kids[0]
		scaleX(n, root.x, root.w)
		n.y, n.h = root.y, root.h
		body = lrender(n)
		return lchecksum(body) + "," + body, nil
	}
	root.kids = kids
	scaleX(root, root.x, root.w)
	body = lrender(root)
	return lchecksum(body) + "," + body, nil
}

// eqLeafResizes walks leaves in geometric order and pins each internal
// boundary with an absolute resize; leaves on the window's right/bottom edge
// are skipped (tmux would move their opposite edge). Content-safe where
// select-layout is not: with a joined sidebar, pane INDEX order diverges
// from geometric order and select-layout would shuffle contents. Used by
// equalize's docked path and by dockOpen's width assertion — it lives here,
// outside the noequalize build tag.
func eqLeafResizes(n *lnode, right, bottom int, add func(...string)) {
	if n.kind == 'l' {
		if n.x+n.w < right {
			add("resize-pane", "-t", "%"+n.pane, "-x", strconv.Itoa(n.w))
		}
		if n.y+n.h < bottom {
			add("resize-pane", "-t", "%"+n.pane, "-y", strconv.Itoa(n.h))
		}
		return
	}
	for _, kid := range n.kids {
		eqLeafResizes(kid, right, bottom, add)
	}
}
