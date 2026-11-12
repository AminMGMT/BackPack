//go:build linux && cgo

package paqet

// Derived from paqet's internal/socket (MIT); see doc.go. Changed: the filter
// takes a port range and a peer, the path comes from network.DiscoverPckPath,
// bare-IP links are handled, and the TCP flag cycle is Backpack's.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcap"
)

// PcapAvailable reports whether this build can capture: it was built with cgo
// and libpcap. A release always is; a plain `CGO_ENABLED=0 go build` is not,
// and says so where PCK is started.
const PcapAvailable = true

// MaxSenders bounds the send handles of one engine. See OpenPcap.
const MaxSenders = 4

// OpenPcap opens the capture and the injector for a path.
//
// filter is the BPF expression for the capture (see CaptureFilter), ring the
// capture buffer in bytes, senders the number of send handles (1 to
// MaxSenders). Each send handle has a 256 KiB ring and a filter that matches
// nothing, as in paqet: it exists only to inject, and capturing what it sends
// would be wasted work.
//
// paqet sends through one handle, and pcap_sendpacket is one system call per
// segment, made under a lock because libpcap does not promise it is safe from
// two threads. That call is where an engine on a fast path spends its time: a
// profile of a bulk upload on a veth pair had one core inside it for 87% of the
// run on each end, the kharej mostly sending acknowledgements, and nothing
// else in the engine near that. With several handles the sends of different
// KCP sessions go out in parallel. Each flow (a source and destination port)
// always takes the same handle, so the segments of one session never overtake
// each other on the way out — KCP would read that as loss and retransmit.
func OpenPcap(path *network.PckPath, filter string, ring int, flags []network.TCPFlags, senders int) (Capture, Injector, error) {
	senders = max(1, min(senders, MaxSenders))
	rxh, err := newHandle(path.Iface, ring, 65536, 100*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}
	if err := rxh.SetDirection(pcap.DirectionIn); err != nil {
		rxh.Close()
		return nil, nil, fmt.Errorf("failed to set pcap direction in: %w", err)
	}
	if err := rxh.SetBPFFilter(filter); err != nil {
		rxh.Close()
		return nil, nil, fmt.Errorf("failed to set the capture filter %q: %w", filter, err)
	}
	link := rxh.LinkType()
	if link != layers.LinkTypeEthernet && link != layers.LinkTypeRaw && link != layers.LinkTypeIPv4 {
		rxh.Close()
		return nil, nil, fmt.Errorf("pck: %s has link type %v, which the packet carrier cannot read", path.Iface, link)
	}
	var txs []packetWriter
	fail := func(err error) (Capture, Injector, error) {
		rxh.Close()
		for _, h := range txs {
			h.Close()
		}
		return nil, nil, err
	}
	for range senders {
		txh, err := newHandle(path.Iface, 256*1024, 128, pcap.BlockForever)
		if err != nil {
			return fail(err)
		}
		txs = append(txs, txh)
		if err := txh.SetDirection(pcap.DirectionOut); err != nil {
			return fail(fmt.Errorf("failed to set pcap direction out: %w", err))
		}
		if err := txh.SetBPFFilter("less 0"); err != nil {
			return fail(fmt.Errorf("failed to set the send filter: %w", err))
		}
	}
	return newPcapCapture(rxh, link), newPcapInjector(txs, path, link, flags), nil
}

func newHandle(iface string, buf, snap int, timeout time.Duration) (*pcap.Handle, error) {
	inactive, err := pcap.NewInactiveHandle(iface)
	if err != nil {
		return nil, fmt.Errorf("failed to create a pcap handle for %s: %w", iface, err)
	}
	defer inactive.CleanUp()
	if err = inactive.SetBufferSize(buf); err != nil {
		return nil, fmt.Errorf("failed to set the pcap buffer to %d: %w", buf, err)
	}
	if err = inactive.SetSnapLen(snap); err != nil {
		return nil, fmt.Errorf("failed to set the pcap snap length: %w", err)
	}
	if err = inactive.SetPromisc(false); err != nil {
		return nil, fmt.Errorf("failed to clear promiscuous mode: %w", err)
	}
	if err = inactive.SetTimeout(timeout); err != nil {
		return nil, fmt.Errorf("failed to set the pcap timeout: %w", err)
	}
	if err = inactive.SetImmediateMode(true); err != nil {
		return nil, fmt.Errorf("failed to enable immediate mode: %w", err)
	}
	h, err := inactive.Activate()
	if err != nil {
		return nil, fmt.Errorf("failed to activate pcap on %s (it needs root or CAP_NET_RAW): %w", iface, err)
	}
	return h, nil
}

// ---------------------------------------------------------------- capture

type pcapCapture struct {
	h    *pcap.Handle
	raw  bool
	p4   *gopacket.DecodingLayerParser // ip4-first, for bare-IP links
	p6   *gopacket.DecodingLayerParser
	pe   *gopacket.DecodingLayerParser // ethernet-first
	eth  layers.Ethernet
	ip4  layers.IPv4
	ip6  layers.IPv6
	tcp  layers.TCP
	dec  []gopacket.LayerType
	once sync.Once
}

func newPcapCapture(h *pcap.Handle, link layers.LinkType) *pcapCapture {
	c := &pcapCapture{h: h, raw: link != layers.LinkTypeEthernet, dec: make([]gopacket.LayerType, 0, 4)}
	mk := func(first gopacket.LayerType) *gopacket.DecodingLayerParser {
		p := gopacket.NewDecodingLayerParser(first, &c.eth, &c.ip4, &c.ip6, &c.tcp)
		p.IgnoreUnsupported = true
		return p
	}
	if c.raw {
		c.p4, c.p6 = mk(layers.LayerTypeIPv4), mk(layers.LayerTypeIPv6)
	} else {
		c.pe = mk(layers.LayerTypeEthernet)
	}
	return c
}

func (c *pcapCapture) Next() ([]byte, *net.UDPAddr, uint16, net.HardwareAddr, error) {
	data, _, err := c.h.ZeroCopyReadPacketData()
	if err != nil {
		if errors.Is(err, pcap.NextErrorTimeoutExpired) {
			return nil, nil, 0, nil, errNoFrame
		}
		return nil, nil, 0, nil, err
	}
	p := c.pe
	if c.raw {
		p = c.p4
		if len(data) > 0 && data[0]>>4 == 6 {
			p = c.p6
		}
	}
	if p == nil || p.DecodeLayers(data, &c.dec) != nil {
		return nil, nil, 0, nil, errNoFrame
	}
	src := &net.UDPAddr{}
	var payload []byte
	for _, t := range c.dec {
		switch t {
		case layers.LayerTypeIPv4:
			src.IP = slices.Clone(c.ip4.SrcIP)
		case layers.LayerTypeIPv6:
			src.IP = slices.Clone(c.ip6.SrcIP)
		case layers.LayerTypeTCP:
			src.Port = int(c.tcp.SrcPort)
			payload = c.tcp.Payload
		}
	}
	if src.IP == nil || len(payload) == 0 {
		return nil, nil, 0, nil, errNoFrame
	}
	var mac net.HardwareAddr
	if !c.raw && len(c.eth.SrcMAC) == 6 {
		mac = c.eth.SrcMAC // valid until the next read; LearnNextHop copies it
	}
	return payload, src, uint16(c.tcp.DstPort), mac, nil
}

func (c *pcapCapture) Close() error {
	c.once.Do(func() { c.h.Close() })
	return nil
}

// ---------------------------------------------------------------- injector

var errNoNextHop = errors.New("pck: no next hop for that peer yet: nothing has arrived from it and the host has no gateway")

type encoder struct {
	eth layers.Ethernet
	ip4 layers.IPv4
	tcp layers.TCP

	opts [5]layers.TCPOption
	ts   [8]byte
	mss  [2]byte
	ws   [1]byte

	buf gopacket.SerializeBuffer
}

// packetWriter is what the injector needs of a pcap handle.
type packetWriter interface {
	WritePacketData([]byte) error
	Close()
}

// sender is one send handle.
type sender struct {
	h packetWriter
	// mu serialises the sends on this handle and its close. gopacket's
	// WritePacketData takes no lock of the handle's, and Close frees the
	// libpcap handle and clears its pointer, so a send racing the close — a KCP
	// flush on its own goroutine while the generation ends — would hand libpcap
	// a freed or nil handle and take the whole process down. closed is read
	// under it.
	mu     sync.Mutex
	closed bool
}

type pcapInjector struct {
	tx      []*sender
	path    *network.PckPath
	link    bool // frames carry an Ethernet header
	flags   []network.TCPFlags
	flagRot atomic.Uint32
	time    uint32
	counter atomic.Uint32
	pool    sync.Pool
	once    sync.Once

	// learned holds next hops taken from arriving segments, by peer address,
	// used only when the path has no gateway of its own. See NextHopLearner.
	learned sync.Map // [4]byte -> net.HardwareAddr
}

func (j *pcapInjector) WantsNextHops() bool { return j.link && j.path.NextHop == nil }

func (j *pcapInjector) LearnNextHop(ip net.IP, mac net.HardwareAddr) {
	ip4 := ip.To4()
	if ip4 == nil {
		return
	}
	var k [4]byte
	copy(k[:], ip4)
	if cur, ok := j.learned.Load(k); ok && string(cur.(net.HardwareAddr)) == string(mac) {
		return
	}
	j.learned.Store(k, append(net.HardwareAddr(nil), mac...))
}

// nextHop is where a frame for dst goes: the path's own gateway when it has
// one, else what arrived from dst.
func (j *pcapInjector) nextHop(dst net.IP) net.HardwareAddr {
	if j.path.NextHop != nil {
		return j.path.NextHop
	}
	var k [4]byte
	copy(k[:], dst.To4())
	if mac, ok := j.learned.Load(k); ok {
		return mac.(net.HardwareAddr)
	}
	return nil
}

func newPcapInjector(hs []packetWriter, path *network.PckPath, link layers.LinkType, flags []network.TCPFlags) *pcapInjector {
	if len(flags) == 0 {
		flags = []network.TCPFlags{network.FlagPSH | network.FlagACK}
	}
	j := &pcapInjector{
		path: path, link: link == layers.LinkTypeEthernet, flags: flags,
		time: uint32(time.Now().UnixNano() / int64(time.Millisecond)),
	}
	for _, h := range hs {
		j.tx = append(j.tx, &sender{h: h})
	}
	j.pool.New = func() any {
		return &encoder{
			eth: layers.Ethernet{SrcMAC: path.SrcMAC},
			mss: [2]byte{0x05, 0xb4},
			ws:  [1]byte{8},
			buf: gopacket.NewSerializeBuffer(),
		}
	}
	return j
}

func (j *pcapInjector) buildTCP(e *encoder, srcPort, dstPort uint16, f network.TCPFlags) {
	e.tcp = layers.TCP{
		SrcPort: layers.TCPPort(srcPort), DstPort: layers.TCPPort(dstPort),
		FIN: f&network.FlagFIN != 0, SYN: f&network.FlagSYN != 0, RST: f&network.FlagRST != 0,
		PSH: f&network.FlagPSH != 0, ACK: f&network.FlagACK != 0, URG: f&network.FlagURG != 0,
		ECE: f&network.FlagECE != 0, CWR: f&network.FlagCWR != 0,
		Window: 65535,
	}
	counter := j.counter.Add(1)
	tsVal := j.time + (counter >> 3)
	opts := e.opts[:0]
	if f&network.FlagSYN != 0 {
		binary.BigEndian.PutUint32(e.ts[0:4], tsVal)
		binary.BigEndian.PutUint32(e.ts[4:8], 0)
		opts = append(opts,
			layers.TCPOption{OptionType: layers.TCPOptionKindMSS, OptionLength: 4, OptionData: e.mss[:]},
			layers.TCPOption{OptionType: layers.TCPOptionKindSACKPermitted, OptionLength: 2},
			layers.TCPOption{OptionType: layers.TCPOptionKindTimestamps, OptionLength: 10, OptionData: e.ts[:]},
			layers.TCPOption{OptionType: layers.TCPOptionKindNop},
			layers.TCPOption{OptionType: layers.TCPOptionKindWindowScale, OptionLength: 3, OptionData: e.ws[:]},
		)
		e.tcp.Seq = 1 + (counter & 0x7)
		if f&network.FlagACK != 0 {
			e.tcp.Ack = e.tcp.Seq + 1
		}
	} else {
		binary.BigEndian.PutUint32(e.ts[0:4], tsVal)
		binary.BigEndian.PutUint32(e.ts[4:8], tsVal-(counter%200+50))
		opts = append(opts,
			layers.TCPOption{OptionType: layers.TCPOptionKindNop},
			layers.TCPOption{OptionType: layers.TCPOptionKindNop},
			layers.TCPOption{OptionType: layers.TCPOptionKindTimestamps, OptionLength: 10, OptionData: e.ts[:]},
		)
		seq := j.time + (counter << 7)
		e.tcp.Seq = seq
		e.tcp.Ack = seq - (counter & 0x3FF) + 1400
	}
	e.tcp.Options = opts
}

func (j *pcapInjector) Inject(srcPort uint16, payload []byte, dst *net.UDPAddr) error {
	e := j.pool.Get().(*encoder)
	defer func() {
		e.buf.Clear()
		j.pool.Put(e)
	}()
	f := j.flags[(j.flagRot.Add(1)-1)%uint32(len(j.flags))]
	j.buildTCP(e, srcPort, uint16(dst.Port), f)

	var ip gopacket.SerializableLayer
	if d4 := dst.IP.To4(); d4 != nil {
		e.ip4 = layers.IPv4{
			Version: 4, IHL: 5, TOS: 184, TTL: 64, Flags: layers.IPv4DontFragment,
			Protocol: layers.IPProtocolTCP, SrcIP: j.path.LocalIP, DstIP: d4,
		}
		ip = &e.ip4
		e.tcp.SetNetworkLayerForChecksum(&e.ip4)
		e.eth.EthernetType = layers.EthernetTypeIPv4
	} else {
		return errors.New("pck: only IPv4 peers are supported")
	}
	if j.link {
		if e.eth.DstMAC = j.nextHop(dst.IP); e.eth.DstMAC == nil {
			return errNoNextHop
		}
	}

	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	var err error
	if j.link {
		err = gopacket.SerializeLayers(e.buf, opts, &e.eth, ip, &e.tcp, gopacket.Payload(payload))
	} else {
		err = gopacket.SerializeLayers(e.buf, opts, ip, &e.tcp, gopacket.Payload(payload))
	}
	if err != nil {
		return err
	}
	// One flow, one handle: see OpenPcap. pcap_sendpacket is not guaranteed
	// thread-safe, and must not meet Close.
	tx := j.tx[(int(srcPort)+dst.Port)%len(j.tx)]
	tx.mu.Lock()
	if tx.closed {
		err = net.ErrClosed
	} else {
		err = tx.h.WritePacketData(e.buf.Bytes())
	}
	tx.mu.Unlock()
	return err
}

func (j *pcapInjector) Close() error {
	j.once.Do(func() {
		for _, tx := range j.tx {
			tx.mu.Lock()
			tx.closed = true
			tx.h.Close()
			tx.mu.Unlock()
		}
	})
	return nil
}
