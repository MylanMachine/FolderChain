package engine

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Announcements are small UDP broadcasts that let devices in the same share
// find each other on the LAN, whatever IP addresses they have today.
type announcement struct {
	M string `json:"m"` // magic
	S string `json:"s"` // share ID (public, derived from the key)
	I string `json:"i"` // device ID
	N string `json:"n"` // device name
	P int    `json:"p"` // TCP port
	R bool   `json:"r,omitempty"`
	H string `json:"h"` // MAC proving the sender holds the share key
}

func (a *announcement) mac(key []byte) string {
	return announceMAC(key, a.M, a.S, a.I, a.N, strconv.Itoa(a.P), strconv.FormatBool(a.R))
}

func (s *session) discoveryLoop() {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: s.e.opts.DiscoveryPort})
	if err != nil {
		// Another program has the port; we can still announce and hear
		// direct replies on a random port.
		s.e.log.Printf("discovery port busy (%v); using a random port", err)
		if conn, err = net.ListenUDP("udp4", &net.UDPAddr{}); err != nil {
			s.e.log.Printf("discovery disabled: %v", err)
			return
		}
	}
	go func() {
		<-s.ctx.Done()
		conn.Close()
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.discoveryRead(conn)
	}()

	announce := time.NewTicker(5 * time.Second)
	netCheck := time.NewTicker(2 * time.Second)
	defer announce.Stop()
	defer netCheck.Stop()
	targets, sig := broadcastTargets(s.e.opts.DiscoveryPort)
	s.announce(conn, targets, nil)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-announce.C:
		case <-netCheck.C:
			t, ns := broadcastTargets(s.e.opts.DiscoveryPort)
			if ns == sig {
				continue
			}
			targets, sig = t, ns
			s.networkChanged()
		}
		s.announce(conn, targets, nil)
	}
}

func (s *session) makeAnnouncement(reply bool) []byte {
	s.e.mu.Lock()
	a := announcement{M: "FC1", S: s.keys.shareID, I: s.e.cfg.DeviceID, N: s.e.cfg.DeviceName, P: s.listenPort, R: reply}
	s.e.mu.Unlock()
	a.H = a.mac(s.keys.disco)
	b, _ := json.Marshal(a)
	return b
}

func (s *session) announce(conn *net.UDPConn, targets []*net.UDPAddr, to *net.UDPAddr) {
	if to != nil {
		conn.WriteToUDP(s.makeAnnouncement(true), to)
		return
	}
	b := s.makeAnnouncement(false)
	for _, t := range targets {
		conn.WriteToUDP(b, t)
	}
}

func (s *session) discoveryRead(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	lastReply := map[string]time.Time{}
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var a announcement
		if json.Unmarshal(buf[:n], &a) != nil || a.M != "FC1" || a.S != s.keys.shareID {
			continue
		}
		if a.H != a.mac(s.keys.disco) || a.P <= 0 || a.P > 65535 || len(a.I) > 64 {
			continue
		}
		s.e.mu.Lock()
		self := a.I == s.e.cfg.DeviceID
		s.e.mu.Unlock()
		if self {
			continue
		}
		ip := from.IP.String()
		s.discovered(a.I, a.N, net.JoinHostPort(ip, strconv.Itoa(a.P)))
		// Answer broadcasts directly. Windows Firewall allows unicast replies
		// to a broadcast even when inbound traffic is blocked, so a device on
		// a "Public" network can still learn about us and dial out.
		if !a.R && time.Since(lastReply[ip]) > time.Second {
			lastReply[ip] = time.Now()
			s.announce(conn, nil, from)
		}
	}
}

// broadcastTargets lists the broadcast address of every active IPv4
// network plus 255.255.255.255, and a signature of local addresses used
// to notice network changes (Wi-Fi rejoin, dock/undock, VPN).
func broadcastTargets(port int) ([]*net.UDPAddr, string) {
	seen := map[string]bool{"255.255.255.255": true}
	var local []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || len(ipn.Mask) != net.IPv4len {
				continue
			}
			local = append(local, ip4.String())
			if ifc.Flags&net.FlagBroadcast == 0 {
				continue
			}
			bc := make(net.IP, 4)
			for i := range bc {
				bc[i] = ip4[i] | ^ipn.Mask[i]
			}
			seen[bc.String()] = true
		}
	}
	var out []*net.UDPAddr
	for ip := range seen {
		out = append(out, &net.UDPAddr{IP: net.ParseIP(ip), Port: port})
	}
	sort.Strings(local)
	return out, strings.Join(local, ",")
}

// LocalIPs returns this computer's LAN addresses for display.
func LocalIPs() []string {
	_, sig := broadcastTargets(0)
	if sig == "" {
		return nil
	}
	var out []string
	for _, ip := range strings.Split(sig, ",") {
		if !strings.HasPrefix(ip, "169.254.") {
			out = append(out, ip)
		}
	}
	return out
}
