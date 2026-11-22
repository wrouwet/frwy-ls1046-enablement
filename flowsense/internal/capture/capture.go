// Package capture reads packets from a Linux interface with an AF_PACKET raw
// socket (no libpcap) and decodes the headers flowsense needs.
package capture

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
	"unsafe"
)

// TCP flag bits.
const (
	FIN = 0x01
	SYN = 0x02
	RST = 0x04
	PSH = 0x08
	ACK = 0x10
)

// Packet is one decoded IPv4 TCP/UDP packet.
type Packet struct {
	TS         time.Time
	Len        int // IP total length (bytes on the wire above L2)
	Segs       int // >1 when the kernel handed us a GRO/TSO super-packet
	Proto      uint8
	Src, Dst   netip.Addr
	SPort      uint16
	DPort      uint16
	Flags      uint8 // TCP flags
	PayloadLen int
	Payload    []byte // first bytes of L4 payload (copied, may be truncated)
	Outgoing   bool   // PACKET_OUTGOING: sent by this host on the interface
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// Source is an open capture socket.
type Source struct {
	fd    int
	iface string
	buf   []byte
	Drops uint64
}

// Open binds a raw socket to iface (all ethertypes).
func Open(iface string) (*Source, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("socket: %w (needs root/CAP_NET_RAW)", err)
	}
	sll := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: ifi.Index}
	if err := syscall.Bind(fd, sll); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 8<<20)
	tv := syscall.Timeval{Sec: 1}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	return &Source{fd: fd, iface: iface, buf: make([]byte, 65536)}, nil
}

// Close closes the socket.
func (s *Source) Close() error { return syscall.Close(s.fd) }

type tpacketStats struct{ Packets, Drops uint32 }

// KernelDrops returns packets dropped by the kernel since the last call.
func (s *Source) KernelDrops() uint64 {
	var st tpacketStats
	l := uint32(unsafe.Sizeof(st))
	_, _, e := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(s.fd), 263 /*SOL_PACKET*/, 6, /*PACKET_STATISTICS*/
		uintptr(unsafe.Pointer(&st)), uintptr(unsafe.Pointer(&l)), 0)
	if e != 0 {
		return 0
	}
	s.Drops += uint64(st.Drops)
	return uint64(st.Drops)
}

// Next blocks (≤1 s) for the next IPv4 TCP/UDP packet. ok=false on timeout or
// for frames that are not interesting.
func (s *Source) Next(snap int) (p Packet, ok bool, err error) {
	n, from, err := syscall.Recvfrom(s.fd, s.buf, 0)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EINTR {
			return p, false, nil
		}
		return p, false, err
	}
	if ll, isLL := from.(*syscall.SockaddrLinklayer); isLL {
		p.Outgoing = ll.Pkttype == 4 // PACKET_OUTGOING
	}
	p.TS = time.Now()
	return decode(s.buf[:n], snap, p)
}

func decode(f []byte, snap int, p Packet) (Packet, bool, error) {
	if len(f) < 14 {
		return p, false, nil
	}
	et := binary.BigEndian.Uint16(f[12:])
	off := 14
	for et == 0x8100 || et == 0x88a8 { // VLAN tags
		if len(f) < off+4 {
			return p, false, nil
		}
		et = binary.BigEndian.Uint16(f[off+2:])
		off += 4
	}
	if et != 0x0800 {
		return p, false, nil
	}
	ip := f[off:]
	if len(ip) < 20 || ip[0]>>4 != 4 {
		return p, false, nil
	}
	ihl := int(ip[0]&0x0f) * 4
	tot := int(binary.BigEndian.Uint16(ip[2:]))
	if tot == 0 { // TSO super-packets may carry 0
		tot = len(ip)
	}
	if ihl < 20 || len(ip) < ihl {
		return p, false, nil
	}
	if binary.BigEndian.Uint16(ip[6:])&0x1fff != 0 { // non-first fragment
		return p, false, nil
	}
	p.Proto = ip[9]
	p.Src = netip.AddrFrom4([4]byte(ip[12:16]))
	p.Dst = netip.AddrFrom4([4]byte(ip[16:20]))
	p.Len = tot
	l4 := ip[ihl:]
	if tot < len(ip) {
		l4 = ip[ihl:tot]
	}
	var hl int
	switch p.Proto {
	case 6:
		if len(l4) < 20 {
			return p, false, nil
		}
		p.SPort = binary.BigEndian.Uint16(l4)
		p.DPort = binary.BigEndian.Uint16(l4[2:])
		p.Flags = l4[13]
		hl = int(l4[12]>>4) * 4
	case 17:
		if len(l4) < 8 {
			return p, false, nil
		}
		p.SPort = binary.BigEndian.Uint16(l4)
		p.DPort = binary.BigEndian.Uint16(l4[2:])
		hl = 8
	default:
		return p, false, nil
	}
	if hl > len(l4) {
		hl = len(l4)
	}
	p.PayloadLen = tot - ihl - hl
	if p.PayloadLen < 0 {
		p.PayloadLen = 0
	}
	// GRO/TSO: estimate how many wire packets this represents.
	p.Segs = 1
	if tot > 1514 {
		mss := 1448
		if p.Proto == 17 {
			mss = 1472
		}
		p.Segs = (p.PayloadLen + mss - 1) / mss
	}
	if snap > 0 {
		pl := l4[hl:]
		if len(pl) > snap {
			pl = pl[:snap]
		}
		p.Payload = append([]byte(nil), pl...)
	}
	return p, true, nil
}
