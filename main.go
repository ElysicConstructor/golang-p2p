package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const defaultPort = 5555
const broadcastAddr = "255.255.255.255"

// ---------------- PeerSet ----------------
type peerSet struct {
	addrs map[string]*net.UDPAddr
}

func newPeerSet() *peerSet { return &peerSet{addrs: make(map[string]*net.UDPAddr)} }
func (ps *peerSet) add(addr *net.UDPAddr) {
	ps.addrs[addr.String()] = addr
}
func (ps *peerSet) list() []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, len(ps.addrs))
	for _, a := range ps.addrs {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ---------------- TUI Peer ----------------
func runPeerTUI(name, room, introducerAddr, listen string) error {
	laddr, _ := net.ResolveUDPAddr("udp", listen)
	conn, _ := net.ListenUDP("udp", laddr)
	defer conn.Close()

	app := tview.NewApplication()

	// Sidebar (Räume)
	sidebar := tview.NewList().ShowSecondaryText(false)
	sidebar.SetBorder(true).SetTitle("Räume")
	sidebar.AddItem(room, "", 0, nil)

	// Chatbox
	chatBox := tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	chatBox.SetBorder(true).SetTitle("Chat")

	// Inputbox
	input := tview.NewInputField().SetLabel("Nachricht: ").SetFieldWidth(0)
	input.SetBorder(true)
	peers := newPeerSet()

	input.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			msg := input.GetText()
			if msg != "" {
				line := fmt.Sprintf("%s: %s", name, msg)
				chatBox.Write([]byte(fmt.Sprintf("[yellow]%s\n", line)))
				input.SetText("")
				for _, p := range peers.list() {
					conn.WriteToUDP([]byte("MSG "+line), p)
				}
			}
		}
	})

	// Grid Layout
	grid := tview.NewGrid().
		SetRows(0, 3).
		SetColumns(30, 0).
		SetBorders(true)
	grid.AddItem(sidebar, 0, 0, 2, 1, 0, 0, true)
	grid.AddItem(chatBox, 0, 1, 1, 1, 0, 0, false)
	grid.AddItem(input, 1, 1, 1, 1, 0, 0, true)

	app.SetRoot(grid, true).SetFocus(input)

	// Empfange Nachrichten
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, _ := conn.ReadFromUDP(buf)
			line := strings.TrimSpace(string(buf[:n]))
			if strings.HasPrefix(line, "MSG ") {
				line = strings.TrimPrefix(line, "MSG ")
				chatBox.Write([]byte(fmt.Sprintf("[green]%s\n", line)))
				app.Draw()
			} else if strings.HasPrefix(line, "PUNCH") {
				peers.add(from)
			}
		}
	}()

	return app.Run()
}

// ---------------- Introducer ----------------
type roomState struct {
	peers map[string]*net.UDPAddr
}

type introducer struct {
	rooms map[string]*roomState
	conn  *net.UDPConn
}

func runIntroducer(listen string) error {
	addr, _ := net.ResolveUDPAddr("udp", listen)
	conn, _ := net.ListenUDP("udp", addr)
	defer conn.Close()

	intr := &introducer{rooms: make(map[string]*roomState), conn: conn}
	fmt.Println("Introducer läuft auf", conn.LocalAddr())

	buf := make([]byte, 2048)
	for {
		n, from, _ := conn.ReadFromUDP(buf)
		msg := strings.TrimSpace(string(buf[:n]))
		go intr.handle(from, msg)
	}
}

func (in *introducer) handle(from *net.UDPAddr, msg string) {
	if msg == "HELLO" {
		// Antwort auf Broadcast
		in.reply(from, "I_AM_INTRODUCER")
		return
	}

	parts := strings.Fields(msg)
	if len(parts) < 2 {
		return
	}
	cmd := strings.ToUpper(parts[0])
	switch cmd {
	case "JOIN":
		if len(parts) < 3 {
			return
		}
		room := parts[1]
		name := parts[2]
		rs := in.rooms[room]
		if rs == nil {
			rs = &roomState{peers: make(map[string]*net.UDPAddr)}
			in.rooms[room] = rs
		}
		rs.peers[from.String()] = from
		fmt.Println("Peer beigetreten:", name, from.String())
	case "LEAVE":
		room := parts[1]
		if rs, ok := in.rooms[room]; ok {
			delete(rs.peers, from.String())
			if len(rs.peers) == 0 {
				delete(in.rooms, room)
			}
		}
	}
}

func (in *introducer) reply(to *net.UDPAddr, msg string) {
	_, _ = in.conn.WriteToUDP([]byte(msg), to)
}

// ---------------- Auto-Start ----------------
func detectIntroducer() *net.UDPAddr {
	laddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		fmt.Println("Fehler beim Öffnen des Sockets:", err)
		return nil
	}
	defer conn.Close()

	raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", broadcastAddr, defaultPort))
	_, _ = conn.WriteToUDP([]byte("HELLO"), raddr)

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	n, addr, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil
	}
	if string(buf[:n]) == "I_AM_INTRODUCER" {
		return addr
	}
	return nil
}

func autoStart(name, room string) {
	introducerAddr := detectIntroducer()
	if introducerAddr != nil {
		fmt.Println("Introducer gefunden:", introducerAddr.String())
		err := runPeerTUI(name, room, introducerAddr.String(), fmt.Sprintf(":%d", defaultPort))
		if err != nil {
			fmt.Println("Peer Fehler:", err)
		}
	} else {
		fmt.Println("Kein Introducer gefunden. Starte selbst als Introducer.")
		err := runIntroducer(fmt.Sprintf(":%d", defaultPort))
		if err != nil {
			fmt.Println("Introducer Fehler:", err)
		}
	}
}

// ---------------- Main ----------------
func main() {
	// Name / Raum
	name := fmt.Sprintf("peer-%d", time.Now().Unix()%10000)
	room := "default"

	autoStart(name, room)
}
