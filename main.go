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
func autoStart(name, room string) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", defaultPort))
	if err != nil {
		panic(err)
	}

	// Versuch den Port zu binden
	conn, err := net.ListenUDP("udp", addr)
	if err == nil {
		// Port frei → Introducer
		conn.Close()
		fmt.Println("Port frei → Rolle: Introducer")
		if err := runIntroducer(fmt.Sprintf(":%d", defaultPort)); err != nil {
			fmt.Println("Introducer Fehler:", err)
		}
	} else {
		// Port belegt → Peer
		fmt.Println("Port belegt → Rolle: Peer")
		introAddr := fmt.Sprintf("127.0.0.1:%d", defaultPort) // standardmäßig lokale Verbindung, ggf. anpassen
		if err := runPeerTUI(name, room, introAddr, fmt.Sprintf(":%d", defaultPort)); err != nil {
			fmt.Println("Peer Fehler:", err)
		}
	}
}

// ---------------- Main ----------------
func main() {
	name := fmt.Sprintf("peer-%d", time.Now().Unix()%10000)
	room := "default"

	autoStart(name, room)
}
