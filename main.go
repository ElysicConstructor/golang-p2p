package ssx
package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/crypto/nacl/box"
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
func runPeerTUI(name, room string, introducerAddr *net.UDPAddr, priv, pub *[32]byte) error {
	laddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", defaultPort))
	conn, _ := net.ListenUDP("udp", laddr)
	defer conn.Close()

	app := tview.NewApplication()
	sidebar := tview.NewList().ShowSecondaryText(false)
	sidebar.SetBorder(true).SetTitle("Räume").AddItem(room, "", 0, nil)
	chatBox := tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	chatBox.SetBorder(true).SetTitle("Chat")
	input := tview.NewInputField().SetLabel("Nachricht: ").SetFieldWidth(0)
	input.SetBorder(true)
	peers := newPeerSet()
	if introducerAddr != nil {
		peers.add(introducerAddr)
	}

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

	grid := tview.NewGrid().SetRows(0, 3).SetColumns(30, 0).SetBorders(true)
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
				// Push Notification
				execNotify(line)
			} else if strings.HasPrefix(line, "HELLO") {
				peers.add(from)
			}
		}
	}()

	return app.Run()
}

// ---------------- Introducer ----------------
func runIntroducer(pub, priv *[32]byte) {
	laddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", defaultPort))
	conn, _ := net.ListenUDP("udp", laddr)
	defer conn.Close()
	fmt.Println("Introducer läuft auf", conn.LocalAddr())

	buf := make([]byte, 2048)
	for {
		n, from, _ := conn.ReadFromUDP(buf)
		msg := strings.TrimSpace(string(buf[:n]))
		if msg == "HELLO" {
			conn.WriteToUDP([]byte("HELLO"), from)
		}
	}
}

// ---------------- Push Notification ----------------
func execNotify(msg string) {
	// Nutzt Linux notify-send
	go func() {
		_ = exec.Command("notify-send", "P2P Chat", msg).Run()
	}()
}

// ---------------- Auto-Start ----------------
func autoStart(name, room, targetIP string) {
	pub, priv, _ := box.GenerateKey(rand.Reader)
	var introducerAddr *net.UDPAddr
	if targetIP != "" {
		laddr, _ := net.ResolveUDPAddr("udp", ":0")
		conn, _ := net.ListenUDP("udp", laddr)
		defer conn.Close()
		raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", targetIP, defaultPort))
		conn.WriteToUDP([]byte("HELLO"), raddr)
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		buf := make([]byte, 1024)
		n, _, err := conn.ReadFromUDP(buf)
		if err == nil && string(buf[:n]) == "HELLO" {
			introducerAddr = raddr
			fmt.Println("Ziel antwortet → Peer")
		} else {
			fmt.Println("Keine Antwort → wir werden Introducer")
			runIntroducer(pub, priv)
			return
		}
	}
	runPeerTUI(name, room, introducerAddr, priv, pub)
}

// ---------------- Main ----------------
func main() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Ziel-IP eingeben (leer → warten auf Broadcast): ")
	targetIP, _ := reader.ReadString('\n')
	targetIP = strings.TrimSpace(targetIP)
	name := fmt.Sprintf("peer-%d", time.Now().Unix()%10000)
	room := "default"
	autoStart(name, room, targetIP)
}
