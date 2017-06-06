package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"syscall"
	"time"
	"unicode"
	"unsafe"
)

/*** defines ***/

const KILO_VERSION = "0.0.1"
const KILO_TAB_STOP = 8
const KILO_QUIT_TIMES = 3
const (
	BACKSPACE   = 127
	ARROW_LEFT  = 1000 + iota
	ARROW_RIGHT = 1000 + iota
	ARROW_UP    = 1000 + iota
	ARROW_DOWN  = 1000 + iota
	DEL_KEY     = 1000 + iota
	HOME_KEY    = 1000 + iota
	END_KEY     = 1000 + iota
	PAGE_UP     = 1000 + iota
	PAGE_DOWN   = 1000 + iota
)

const (
	HL_NORMAL = 0
	HL_NUMBER = iota
	HL_MATCH  = iota
)

/*** data ***/

type Termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Cc     [20]byte
	Ispeed uint32
	Ospeed uint32
}

type erow struct {
	size   int
	rsize  int
	chars  []byte
	render []byte
	hl     []byte
}

type editorConfig struct {
	cx          int
	cy          int
	rx          int
	rowoff      int
	coloff      int
	screenRows  int
	screenCols  int
	numRows     int
	rows        []erow
	dirty       bool
	filename    string
	statusmsg   string
	statusmsg_time time.Time
	origTermios *Termios
}

type WinSize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

var E editorConfig

/*** terminal ***/

func die(err error) {
	disableRawMode()
	io.WriteString(os.Stdout, "\x1b[2J")
	io.WriteString(os.Stdout, "\x1b[H")
	log.Fatal(err)
}

func TcSetAttr(fd uintptr, termios *Termios) error {
	// TCSETS+1 == TCSETSW, because TCSAFLUSH doesn't exist
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCSETS+1), uintptr(unsafe.Pointer(termios))); err != 0 {
		return err
	}
	return nil
}

func TcGetAttr(fd uintptr) *Termios {
	var termios = &Termios{}
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(termios))); err != 0 {
		log.Fatalf("Problem getting terminal attributes: %s\n", err)
	}
	return termios
}

func enableRawMode() {
	E.origTermios = TcGetAttr(os.Stdin.Fd())
	var raw Termios
	raw = *E.origTermios
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN+1] = 0
	raw.Cc[syscall.VTIME+1] = 1
	if e := TcSetAttr(os.Stdin.Fd(), &raw); e != nil {
		log.Fatalf("Problem enabling raw mode: %s\n", e)
	}
}

func disableRawMode() {
	if e := TcSetAttr(os.Stdin.Fd(), E.origTermios); e != nil {
		log.Fatalf("Problem disabling raw mode: %s\n", e)
	}
}

func editorReadKey() int {
	var buffer [1]byte
	var cc int
	var err error
	for cc, err = os.Stdin.Read(buffer[:]); cc != 1; cc, err = os.Stdin.Read(buffer[:]) {
	}
	if err != nil {
		die(err)
	}
	if buffer[0] == '\x1b' {
		var seq [2]byte
		if cc, _ = os.Stdin.Read(seq[:]); cc != 2 {
			return '\x1b'
		}

		if seq[0] == '[' {
			if seq[1] >= '0' && seq[1] <= '9' {
				if cc, err = os.Stdin.Read(buffer[:]); cc != 1 {
					return '\x1b'
				}
				if buffer[0] == '~' {
					switch seq[1] {
					case '1':
						return HOME_KEY
					case '3':
						return DEL_KEY
					case '4':
						return END_KEY
					case '5':
						return PAGE_UP
					case '6':
						return PAGE_DOWN
					case '7':
						return HOME_KEY
					case '8':
						return END_KEY
					}
				}
				// XXX - what happens here?
			} else {
				switch seq[1] {
				case 'A':
					return ARROW_UP
				case 'B':
					return ARROW_DOWN
				case 'C':
					return ARROW_RIGHT
				case 'D':
					return ARROW_LEFT
				case 'H':
					return HOME_KEY
				case 'F':
					return END_KEY
				}
			}
		} else if seq[0] == '0' {
			switch seq[1] {
			case 'H':
				return HOME_KEY
			case 'F':
				return END_KEY
			}
		}

		return '\x1b'
	}
	return int(buffer[0])
}

func getCursorPosition(rows *int, cols *int) int {
	io.WriteString(os.Stdout, "\x1b[6n")
	var buffer [1]byte
	var buf []byte
	var cc int
	for cc, _ = os.Stdin.Read(buffer[:]); cc == 1; cc, _ = os.Stdin.Read(buffer[:]) {
		if buffer[0] == 'R' {
			break
		}
		buf = append(buf, buffer[0])
	}
	if string(buf[0:2]) != "\x1b[" {
		log.Printf("Failed to read rows;cols from tty\n")
		return -1
	}
	if n, e := fmt.Sscanf(string(buf[2:]), "%d;%d", rows, cols); n != 2 || e != nil {
		if e != nil {
			log.Printf("getCursorPosition: fmt.Sscanf() failed: %s\n", e)
		}
		if n != 2 {
			log.Printf("getCursorPosition: got %d items, wanted 2\n", n)
		}
		return -1
	}
	return 0
}

func getWindowSize(rows *int, cols *int) int {
	var w WinSize
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL,
		os.Stdout.Fd(),
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&w)),
	)
	if err != 0 { // type syscall.Errno
		io.WriteString(os.Stdout, "\x1b[999C\x1b[999B")
		return getCursorPosition(rows, cols)
	} else {
		*rows = int(w.Row)
		*cols = int(w.Col)
		return 0
	}
	return -1
}

/*** syntax hightlighting ***/

func editorUpdateSyntax(row *erow) {
	row.hl = make([]byte, row.rsize)
	for i, c := range row.render {
		if unicode.IsDigit(rune(c)) {
			row.hl[i] = HL_NUMBER
		}
	}
}

func editorSyntaxToColor(hl byte) int {
	switch hl {
	case HL_NUMBER:
		return 31
	case HL_MATCH:
		return 34
	}
	return 37
}

/*** row operations ***/

func editorRowCxToRx(row *erow, cx int) int {
	rx := 0
	for j := 0; j < row.size && j < cx; j++ {
		if row.chars[j] == '\t' {
			rx += ((KILO_TAB_STOP - 1) - (rx % KILO_TAB_STOP))
		}
		rx++
	}
	return rx
}

func editorRowRxToCx(row *erow, rx int) int {
	curRx := 0
	var cx int
	for cx = 0; cx < row.size; cx++ {
		if row.chars[cx] == '\t' {
			curRx += (KILO_TAB_STOP - 1) - (curRx % KILO_TAB_STOP)
		}
		curRx++
		if curRx > rx { break }
	}
	return cx
}

func editorUpdateRow(row *erow) {
	tabs := 0
	for _, c := range row.chars {
		if c == '\t' {
			tabs++
		}
	}

	row.render = make([]byte, row.size + tabs*(KILO_TAB_STOP - 1))

	idx := 0
	for _, c := range row.chars {
		if c == '\t' {
			row.render[idx] = ' '
			idx++
			for (idx%KILO_TAB_STOP) != 0 {
				row.render[idx] = ' '
				idx++
			}
		} else {
			row.render[idx] = c
			idx++
		}
	}
	row.rsize = idx
	editorUpdateSyntax(row)
}

func editorInsertRow(at int, s []byte) {
	if at < 0 || at > E.numRows { return }
	var r erow
	r.chars = s
	r.size = len(s)

	if at == 0 {
		t := make([]erow, 1)
		t[0] = r	
		E.rows = append(t, E.rows...)
	} else if at == E.numRows {
		E.rows = append(E.rows, r)
	} else {
		t := make([]erow, 1)
		t[0] = r	
		E.rows = append(E.rows[:at], append(t, E.rows[at:]...)...)
	}

	editorUpdateRow(&E.rows[at])
	E.numRows++
	E.dirty = true
}

func editorDelRow(at int) {
	if at < 0 || at > E.numRows { return }
	E.rows = append(E.rows[:at], E.rows[at+1:]...)
	E.numRows--
	E.dirty = true
}

func editorRowInsertChar(row *erow, at int, c byte) {
	if at < 0 || at > row.size {
		row.chars = append(row.chars, c)
	} else if at == 0 {
		t := make([]byte, row.size+1)
		t[0] = c
		copy(t[1:], row.chars)
		row.chars = t
	} else {
		row.chars = append(
			row.chars[:at],
			append(append(make([]byte,0),c), row.chars[at:]...)...
		)
	}
	row.size = len(row.chars)
	editorUpdateRow(row)
	E.dirty = true
}

func editorRowAppendString(row *erow, s []byte) {
	row.chars = append(row.chars, s...)
	row.size = len(row.chars)
	editorUpdateRow(row)
	E.dirty = true
}

func editorRowDelChar(row *erow, at int) {
	if at < 0 || at > row.size { return }
	row.chars = append(row.chars[:at], row.chars[at+1:]...)
	row.size--
	E.dirty = true
	editorUpdateRow(row)
}

/*** editor operations ***/

func editorInsertChar(c byte) {
	if E.cy == E.numRows {
		var emptyRow []byte
		editorInsertRow(E.numRows, emptyRow)
	}
	editorRowInsertChar(&E.rows[E.cy], E.cx, c)
	E.cx++
}

func editorInsertNewLine() {
	if E.cx == 0 {
		editorInsertRow(E.cy, make([]byte, 0))
	} else {
		editorInsertRow(E.cy+1, E.rows[E.cy].chars[E.cx:])
		E.rows[E.cy].chars = E.rows[E.cy].chars[:E.cx]
		E.rows[E.cy].size = len(E.rows[E.cy].chars)
		editorUpdateRow(&E.rows[E.cy])
	}
	E.cy++
	E.cx = 0
}

func editorDelChar() {
	if E.cy == E.numRows { return }
	if E.cx == 0 && E.cy == 0 { return }
	if E.cx > 0 {
    	editorRowDelChar(&E.rows[E.cy], E.cx - 1)
		E.cx--
	} else {
		E.cx = E.rows[E.cy - 1].size
		editorRowAppendString(&E.rows[E.cy - 1], E.rows[E.cy].chars)
		editorDelRow(E.cy)
		E.cy--
	}
}

/*** file I/O ***/

func editorRowsToString() (string, int) {
	totlen := 0
	buf := ""
	for _, row := range E.rows {
		totlen += row.size + 1
		buf += string(row.chars) + "\n"
	}
	return buf, totlen
}

func editorOpen(filename string) {
	E.filename = filename
	fd, err := os.Open(filename)
	if err != nil {
		die(err)
	}
	defer fd.Close()
	fp := bufio.NewReader(fd)

	for line, err := fp.ReadBytes('\n'); err == nil; line, err = fp.ReadBytes('\n') { 
		// Trim trailing newlines and carriage returns
		for c := line[len(line) - 1]; len(line) > 0 && (c == '\n' || c == '\r'); {
			line = line[:len(line)-1]
			if len(line) > 0 {
				c = line[len(line) - 1]
			}
		}
		editorInsertRow(E.numRows, line)
	}

	if err != nil && err != io.EOF {
		die(err)
	}
	E.dirty = false
}

func editorSave() {
	if E.filename == "" {
		E.filename = editorPrompt("Save as: %q", nil)
		if E.filename == "" {
			editorSetStatusMessage("Save aborted")
			return
		}
	}
	buf, len := editorRowsToString()
	fp,e := os.OpenFile(E.filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if e != nil {
		editorSetStatusMessage("Can't save! file open error %s", e)
		return
	}
	defer fp.Close()
	n, err := io.WriteString(fp, buf)
	if err == nil {
		if n == len {
			E.dirty = false
			editorSetStatusMessage("%d bytes written to disk", len)
		} else {
			editorSetStatusMessage(fmt.Sprintf("wanted to write %d bytes to file, wrote %d", len, n))
		}
		return
	}
	editorSetStatusMessage("Can't save! I/O error %s", err)
}

/*** find ***/

var lastMatch int = -1
var direction int = 1
var savedHlLine int
var savedHl []byte

func editorFindCallback(qry []byte, key int) {

	if savedHlLine > 0 {
		copy(E.rows[savedHlLine].hl, savedHl)
		savedHlLine = 0
		savedHl = nil
	}

	if key == '\r' || key == '\x1b' {
		lastMatch = -1
		direction = 1
		return
	} else if key == ARROW_RIGHT || key == ARROW_DOWN {
		direction = 1
	} else if key == ARROW_LEFT  || key == ARROW_UP   {
		direction = -1
	} else {
		lastMatch = -1
		direction = 1
	}

	if lastMatch == -1 { direction = 1 }
	current := lastMatch

	for _ = range E.rows {
		current += direction
		if current == -1 {
			current = E.numRows - 1
		} else if current == E.numRows {
			current = 0
		}
		row := &E.rows[current]
		x := bytes.Index(row.render, qry)
		if x > -1 {
			lastMatch = current
			E.cy = current
			E.cx = editorRowRxToCx(row, x)
			E.rowoff = E.numRows
			savedHlLine = current
			savedHl = make([]byte, row.rsize)
			copy(savedHl, row.hl)
			max := x + len(qry)
			for i := x; i < max; i++ {
				row.hl[i] = HL_MATCH
			}
			break
		}
	}
}

func editorFind() {
	savedCx     := E.cx
	savedCy     := E.cy
	savedColoff := E.coloff
	savedRowoff := E.rowoff
	query := editorPrompt("Search: %s (ESC/Arrows/Enter)",
		editorFindCallback)
	if query == "" {
		E.cx = savedCx
		E.cy = savedCy
		E.coloff = savedColoff
		E.rowoff = savedRowoff
	}
}

/*** input ***/

func editorPrompt(prompt string, callback func([]byte,int)) string {
	var buf []byte

	for {
		editorSetStatusMessage(prompt, buf)
		editorRefreshScreen()

		c := editorReadKey()

		if c == DEL_KEY || c == ('h' & 0x1f) || c == BACKSPACE {
			if (len(buf) > 0) {
				buf = buf[:len(buf)-1]
			}
		} else if c == '\x1b' {
			editorSetStatusMessage("")
			if callback != nil {
				callback(buf, c)
			}
			return ""
		} else if c == '\r' {
			if len(buf) != 0 {
				editorSetStatusMessage("")
				if callback != nil {
					callback(buf, c)
				}
				return string(buf)
			}
		} else {
			if c >= 0x20 && c < 128 {
				buf = append(buf, byte(c))
			}
		}
		if callback != nil {
			callback(buf, c)
		}
	}
}

func editorMoveCursor(key int) {
	switch key {
	case ARROW_LEFT:
		if E.cx != 0 {
			E.cx--
		} else if E.cy > 0 {
			E.cy--
			E.cx = E.rows[E.cy].size
		}
	case ARROW_RIGHT:
		if E.cy < E.numRows {
			if E.cx < E.rows[E.cy].size {
				E.cx++
			} else if E.cx == E.rows[E.cy].size {
				E.cy++
				E.cx = 0
			}
		}
	case ARROW_UP:
		if E.cy != 0 {
			E.cy--
		}
	case ARROW_DOWN:
		if E.cy < E.numRows {
			E.cy++
		}
	}

	rowlen := 0
	if E.cy < E.numRows {
		rowlen = E.rows[E.cy].size
	}
	if E.cx > rowlen {
		E.cx = rowlen
	}
}

var quitTimes int = KILO_QUIT_TIMES

func editorProcessKeypress() {
	c := editorReadKey()
	switch c {
	case '\r':
		editorInsertNewLine()
		break
	case ('q' & 0x1f):
		if E.dirty && quitTimes > 0 {
			editorSetStatusMessage("Warning!!! File has unsaved changes. Press Ctrl-Q %d more times to quit.", quitTimes)
			quitTimes--
			return
		}
		io.WriteString(os.Stdout, "\x1b[2J")
		io.WriteString(os.Stdout, "\x1b[H")
		disableRawMode()
		os.Exit(0)
	case ('s' & 0x1f):
		editorSave()
	case HOME_KEY:
		E.cx = 0
	case END_KEY:
		if E.cy < E.numRows {
			E.cx = E.rows[E.cy].size
		}
	case ('f' & 0x1f):
		editorFind()
	case ('h' & 0x1f), BACKSPACE, DEL_KEY:
		if c == DEL_KEY { editorMoveCursor(ARROW_RIGHT) }
		editorDelChar()
		break
	case PAGE_UP, PAGE_DOWN:
		dir := ARROW_DOWN
		if c == PAGE_UP {
			E.cy = E.rowoff
			dir = ARROW_UP
		} else {
			E.cy = E.rowoff + E.screenRows - 1
			if E.cy > E.numRows { E.cy = E.numRows }
		}
		for times := E.screenRows; times > 0; times-- {
			editorMoveCursor(dir)
		}
	case ARROW_UP, ARROW_DOWN, ARROW_LEFT, ARROW_RIGHT:
		editorMoveCursor(c)
	case ('l' & 0x1f):
		break
	case '\x1b':
		break
	default:
		editorInsertChar(byte(c))
	}
	quitTimes = KILO_QUIT_TIMES
}

/*** output ***/

func editorScroll() {
	E.rx = 0

	if (E.cy < E.numRows) {
		E.rx = editorRowCxToRx(&(E.rows[E.cy]), E.cx)
	}

	if E.cy < E.rowoff {
		E.rowoff = E.cy
	}
	if E.cy >= E.rowoff + E.screenRows {
		E.rowoff = E.cy - E.screenRows + 1
	}
	if E.rx < E.coloff {
		E.coloff = E.rx
	}
	if E.rx >= E.coloff + E.screenCols {
		E.coloff = E.rx - E.screenCols + 1
	}
}

func editorRefreshScreen() {
	editorScroll()
	ab := bytes.NewBufferString("\x1b[25l")
	ab.WriteString("\x1b[H")
	editorDrawRows(ab)
	editorDrawStatusBar(ab)
	editorDrawMessageBar(ab)
	ab.WriteString(fmt.Sprintf("\x1b[%d;%dH", (E.cy - E.rowoff) + 1, (E.rx - E.coloff) + 1))
	ab.WriteString("\x1b[?25h")
	_, e := ab.WriteTo(os.Stdout)
	if e != nil {
		log.Fatal(e)
	}
}

func editorDrawRows(ab *bytes.Buffer) {
	for y := 0; y < E.screenRows; y++ {
		filerow := y + E.rowoff
		if filerow >= E.numRows {
			if E.numRows == 0 && y == E.screenRows/3 {
				w := fmt.Sprintf("Kilo editor -- version %s", KILO_VERSION)
				if len(w) > E.screenCols {
					w = w[0:E.screenCols]
				}
				pad := "~ "
				for padding := (E.screenCols - len(w)) / 2; padding > 0; padding-- {
					ab.WriteString(pad)
					pad = " "
				}
				ab.WriteString(w)
			} else {
				ab.WriteString("~")
			}
		} else {
			len := E.rows[filerow].rsize - E.coloff
			if len < 0 { len = 0 }
			if len > 0 {
				if len > E.screenCols { len = E.screenCols }
				rindex := E.coloff+len
				hl := E.rows[filerow].hl[E.coloff:rindex]
				currentColor := -1
				for j, c := range E.rows[filerow].render[E.coloff:rindex] {
					if hl[j] == HL_NORMAL {
						if currentColor != -1 {
							ab.WriteString("\x1b[39m")
							currentColor = -1
						}
						ab.WriteByte(c)
					} else {
						color := editorSyntaxToColor(hl[j])
						if color != currentColor {
							currentColor = color
							buf := fmt.Sprintf("\x1b[%dm", color)
							ab.WriteString(buf)
						}
						ab.WriteByte(c)
					}
				}
				ab.WriteString("\x1b[39m")
			}
		}
		ab.WriteString("\x1b[K")
		ab.WriteString("\r\n")

	}
}

func editorDrawStatusBar(ab *bytes.Buffer) {
	ab.WriteString("\x1b[7m")
	fname := E.filename
	if len(fname) == 0 {
		fname = "[No Name]"
	}
	modified := ""
	if E.dirty { modified = "(modified)" }
	status := fmt.Sprintf("%.20s - %d lines %s", fname, E.numRows, modified)
	ln := len(status)
	if ln > E.screenCols { ln = E.screenCols }
	rstatus := fmt.Sprintf("%d/%d", E.cy+1, E.numRows)
	rlen := len(rstatus)
	ab.WriteString(status[:ln])
	for ln < E.screenCols {
		if E.screenCols - ln == rlen {
			ab.WriteString(rstatus)
			break
		} else {
			ab.WriteString(" ")
			ln++
		}
	}
	ab.WriteString("\x1b[m")
	ab.WriteString("\r\n")
}

func editorDrawMessageBar(ab *bytes.Buffer) {
	ab.WriteString("\x1b[K")
	msglen := len(E.statusmsg)
	if msglen > E.screenCols { msglen = E.screenCols }
	if msglen > 0 && (time.Now().Sub(E.statusmsg_time) < 5*time.Second) {
		ab.WriteString(E.statusmsg)
	}
}

func editorSetStatusMessage(args...interface{}) {
	E.statusmsg = fmt.Sprintf(args[0].(string), args[1:]...)
	E.statusmsg_time = time.Now()
}

/*** init ***/

func initEditor() {
	// Initialization a la C not necessary.
	if getWindowSize(&E.screenRows, &E.screenCols) == -1 {
		die(fmt.Errorf("couldn't get screen size"))
	}
	E.screenRows -= 2
}

func main() {
	enableRawMode()
	defer disableRawMode()
	initEditor()
	if len(os.Args) > 1 {
		editorOpen(os.Args[1])
	}

	editorSetStatusMessage("HELP: Ctrl-S = save | Ctrl-Q = quit | Ctrl-F = find")

	for {
		editorRefreshScreen()
		editorProcessKeypress()
	}
}
