// P2P Chat mit UDP Hole Punching in Go
// Eine Datei, zwei Modi: introducer (Koordinator) und peer (Client)
// Funktioniert im LAN und kann über NAT via UDP-Hole-Punching arbeiten (außer bei symmetrischem NAT)
//
// Nutzung:
// 1) Introducer auf einem Server/VM mit öffentlicher IP starten (oder im LAN):
//    go run main.go -mode=introducer -listen=:3478
//
// 2) Peer starten und dem Raum beitreten:
//    go run main.go -mode=peer -name=Alice -room=test123 -introducer=SERVER_IP:3478
//    go run main.go -mode=peer -name=Bob   -room=test123 -introducer=SERVER_IP:3478
//
//    Danach einfach tippen und Enter drücken. Befehle: /peers, /quit
//
// 3) Optional, direkte Verbindung ohne Introducer (z. B. bei bekannter öffentlicher IP/Port):
//    # Peer A
//    go run main.go -mode=peer -name=A -listen=:50000
//    # Peer B
//    go run main.go -mode=peer -name=B -manual=A_OEFF_IP:50000
//
// Hinweise:
// - UDP muss vom Router/NAT nicht-blockiert sein; symmetrisches NAT wird u. U. nicht funktionieren.
// - Firewall-Regel: eingehend UDP auf dem gewählten Port erlauben (oder Ephemeral Port bei -listen=:0).
// - Der Introducer speichert nur "beobachtete" Absenderadressen und sendet sie an Raum-Teilnehmer.
// - Keine Persistenz, keine Verschlüsselung (nur Demo). Für Produktion: DTLS/Noise, Auth, Reconnects etc.

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ----------------------------- Introducer -----------------------------

type roomState struct {
	peers map[string]*net.UDPAddr // key: ip:port string
}

type introducer struct {
	mu    sync.Mutex
	rooms map[string]*roomState
	conn  *net.UDPConn
}

func runIntroducer(listen string) error {
	addr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	intr := &introducer{rooms: make(map[string]*roomState), conn: conn}
	fmt.Println("Introducer läuft auf", conn.LocalAddr())

	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		msg := strings.TrimSpace(string(buf[:n]))
		go intr.handle(from, msg)
	}
}

func (in *introducer) handle(from *net.UDPAddr, msg string) {
	// Unterstützte Nachrichten:
	// JOIN <room> <name>
	// LEAVE <room>
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
		in.mu.Lock()
		rs := in.rooms[room]
		if rs == nil {
			rs = &roomState{peers: make(map[string]*net.UDPAddr)}
			in.rooms[room] = rs
		}
		key := from.String()
		rs.peers[key] = from
		// Snapshot der anderen Peers bauen
		peers := make([]string, 0, len(rs.peers))
		for k := range rs.peers {
			if k != key {
				peers = append(peers, k)
			}
		}
		sort.Strings(peers)
		in.mu.Unlock()

		// Dem Beitretenden seine beobachtete Adresse mitteilen
		in.reply(from, fmt.Sprintf("YOUARE %s", from.String()))
		// Liste der Peers schicken
		if len(peers) > 0 {
			in.reply(from, fmt.Sprintf("PEERS %s", strings.Join(peers, ",")))
		}
		// Allen anderen die neue Adresse mitteilen
		in.broadcast(room, from, fmt.Sprintf("PEERJOIN %s %s", name, from.String()))

	case "LEAVE":
		room := parts[1]
		in.mu.Lock()
		if rs, ok := in.rooms[room]; ok {
			delete(rs.peers, from.String())
			if len(rs.peers) == 0 {
				delete(in.rooms, room)
			}
		}
		in.mu.Unlock()
	}
}

func (in *introducer) reply(to *net.UDPAddr, msg string) {
	_, _ = in.conn.WriteToUDP([]byte(msg), to)
}

func (in *introducer) broadcast(room string, except *net.UDPAddr, msg string) {
	in.mu.Lock()
	rs := in.rooms[room]
	var addrs []*net.UDPAddr
	if rs != nil {
		for _, a := range rs.peers {
			if except != nil && a.String() == except.String() {
				continue
			}
			addrs = append(addrs, a)
		}
	}
	in.mu.Unlock()
	for _, a := range addrs {
		in.reply(a, msg)
	}
}

// ------------------------------- Peer ---------------------------------

type peerSet struct {
	mu    sync.RWMutex
	addrs map[string]*net.UDPAddr
}

func newPeerSet() *peerSet { return &peerSet{addrs: make(map[string]*net.UDPAddr)} }

func (ps *peerSet) add(addr *net.UDPAddr) {
	ps.mu.Lock()
	ps.addrs[addr.String()] = addr
	ps.mu.Unlock()
}

func (ps *peerSet) list() []*net.UDPAddr {
	ps.mu.RLock()
	out := make([]*net.UDPAddr, 0, len(ps.addrs))
	for _, a := range ps.addrs {
		out = append(out, a)
	}
	ps.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func runPeer(name, room, introducerAddr, manual, listen string) error {
	if name == "" {
		name = fmt.Sprintf("peer-%d", time.Now().Unix()%10000)
	}

	// UDP Socket öffnen (wichtig: derselbe Socket für JOIN und Daten, damit NAT-Mapping gleich bleibt)
	laddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	fmt.Println("Peer läuft als", name, "auf", conn.LocalAddr())

	peers := newPeerSet()

	// Introducer kontaktieren (optional)
	var introducer *net.UDPAddr
	if introducerAddr != "" {
		introducer, err = net.ResolveUDPAddr("udp", introducerAddr)
		if err != nil {
			return err
		}
		join := fmt.Sprintf("JOIN %s %s", room, name)
		if _, err := conn.WriteToUDP([]byte(join), introducer); err != nil {
			return fmt.Errorf("JOIN fehlgeschlagen: %w", err)
		}
	}

	// Manuellen Peer hinzufügen (direkte Verbindung)
	if manual != "" {
		if a, err := net.ResolveUDPAddr("udp", manual); err == nil {
			peers.add(a)
			go punch(conn, a)
		} else {
			fmt.Println("Manual-Adresse ungültig:", err)
		}
	}

	// Reader: eingehende Pakete
	errs := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				errs <- err
				return
			}
			line := strings.TrimSpace(string(buf[:n]))

			// Nachrichten vom Introducer
			if introducer != nil && from.String() == introducer.String() {
				handleIntroducer(line, peers, conn)
				continue
			}

			// Daten von Peers
			if strings.HasPrefix(line, "PUNCH") {
				// Gegenseitig merken und kurz antworten
				peers.add(from)
				_ = sendLine(conn, from, fmt.Sprintf("PUNCH-ACK %s %d", name, time.Now().UnixNano()))
				continue
			}
			if strings.HasPrefix(line, "PUNCH-ACK") {
				peers.add(from)
				continue
			}
			if strings.HasPrefix(line, "MSG ") {
				msg := strings.TrimPrefix(line, "MSG ")
				fmt.Printf("[%s] %s\n", from, msg)
				continue
			}
			// Debug-Ausgabe
			fmt.Printf("[RECV %s] %s\n", from, line)
		}
	}()

	// Eingabe lesen
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return errors.New("stdin geschlossen")
		}
		text := scanner.Text()
		if text == "/quit" {
			return nil
		}
		if text == "/peers" {
			ls := peers.list()
			if len(ls) == 0 {
				fmt.Println("(keine)")
			} else {
				for _, a := range ls {
					fmt.Println("-", a)
				}
			}
			continue
		}
		// normale Nachricht: an alle bekannten Peers senden
		payload := fmt.Sprintf("MSG %s: %s", name, text)
		for _, a := range peers.list() {
			_ = sendLine(conn, a, payload)
		}
		// Falls noch niemand da ist, kleinen Hinweis
		if len(peers.list()) == 0 {
			fmt.Println("(Noch keine Peers verbunden; warte auf Introducer/Manual oder öffne Port & teile IP:Port)")
		}
		// Nebenbei prüfen, ob der Reader abgestürzt ist
		select {
		case err := <-errs:
			return err
		default:
		}
	}
}

func handleIntroducer(line string, peers *peerSet, conn *net.UDPConn) {
	switch {
	case strings.HasPrefix(line, "YOUARE "):
		fmt.Println("Öffentliche Sicht auf dich:", strings.TrimPrefix(line, "YOUARE "))
	case strings.HasPrefix(line, "PEERS "):
		list := strings.TrimPrefix(line, "PEERS ")
		for _, s := range strings.Split(list, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if a, err := net.ResolveUDPAddr("udp", s); err == nil {
				peers.add(a)
				go punch(conn, a)
			}
		}
	case strings.HasPrefix(line, "PEERJOIN "):
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			addr := parts[2]
			if a, err := net.ResolveUDPAddr("udp", addr); err == nil {
				peers.add(a)
				go punch(conn, a)
			}
			fmt.Println("Neuer Peer im Raum:", strings.Join(parts[1:], " "))
		}
	}
}

func punch(conn *net.UDPConn, to *net.UDPAddr) {
	// Mehrere schnelle Punch-Pakete + Keepalive
	for i := 0; i < 8; i++ {
		_ = sendLine(conn, to, fmt.Sprintf("PUNCH hello %d", i))
		time.Sleep(120 * time.Millisecond)
	}
	// Kurzes Keepalive, damit NAT-Mapping warm bleibt
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_ = sendLine(conn, to, "PUNCH keepalive")
		}
	}()
}

func sendLine(conn *net.UDPConn, to *net.UDPAddr, line string) error {
	_, err := conn.WriteToUDP([]byte(line), to)
	return err
}

// ------------------------------- main ---------------------------------

func main() {
	mode := flag.String("mode", "peer", "peer|introducer")
	listen := flag.String("listen", ":0", "Listen-UDP Adresse, z. B. :50000 oder 0.0.0.0:0")
	// peer
	name := flag.String("name", "", "Dein Anzeigename")
	room := flag.String("room", "default", "Raum/Channel-Name")
	introducer := flag.String("introducer", "", "Introducer-Adresse ip:port (optional)")
	manual := flag.String("manual", "", "Direkte Peer-Adresse ip:port (optional)")
	flag.Parse()

	switch *mode {
	case "introducer":
		if err := runIntroducer(*listen); err != nil {
			fmt.Fprintln(os.Stderr, "Introducer Fehler:", err)
			os.Exit(1)
		}
	case "peer":
		if err := runPeer(*name, *room, *introducer, *manual, *listen); err != nil {
			fmt.Fprintln(os.Stderr, "Peer Fehler:", err)
			os.Exit(1)
		}
	default:
		fmt.Println("Unbekannter Modus:", *mode)
		os.Exit(2)
	}
}
