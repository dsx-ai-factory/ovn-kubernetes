package util

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	TCA_UNSPEC = iota
	TCA_KIND
	TCA_OPTIONS
)

const (
	TCA_ACT_UNSPEC = iota
	TCA_ACT_KIND
	TCA_ACT_OPTIONS
	TCA_ACT_INDEX
	TCA_ACT_STATS
)

const (
	TCA_STATS_UNSPEC = iota
	TCA_STATS_BASIC
	TCA_STATS_RATE_EST
	TCA_STATS_QUEUE
	TCA_STATS_APP
	TCA_STATS_RATE_EST64
	TCA_STATS_PAD
	TCA_STATS_BASIC_HW
)

const (
	TCA_FLOWER_UNSPEC = iota
	TCA_FLOWER_CLASSID
	TCA_FLOWER_INDEV
	TCA_FLOWER_ACT
)

var errInvalidAttribute = errors.New("invalid attribute; length too short or too large")

// A Header is sent and received with each Message to indicate metadata regarding a Message.
type NetlinkHeader struct {
	Length   uint32 // Length of a Message, including this Header.
	Type     uint16 // Contents of a Message.
	Flags    uint16 // Flags which may be used to modify a request or response.
	Sequence uint32 // The sequence number of a Message.
	PID      uint32 // The port ID of the sending process.
}

// Contains a Header and an arbitrary byte payload, which may be decoded using information from the Header
type NetlinkMessage struct {
	Header NetlinkHeader
	Data   []byte
}

type Msg struct {
	Family  uint32
	Ifindex uint32
	Handle  uint32
	Parent  uint32
	Info    uint32
}

type GenBasic struct {
	Bytes   uint64
	Packets uint32
}

type FilterInfo struct {
	Msg           Msg
	Kind          string
	SentSwBytes   uint64
	SentSwPackets uint32
	SentHwBytes   uint64
	SentHwPackets uint32
}

type Attribute struct {
	Length uint16 // Length of an Attribute, including this field and Type
	Type   uint16 // Determines the way we unmarshal Data
	Data   []byte // An arbitrary payload which is specified by Type
}

type AttributeDecoder struct {
	ByteOrder binary.ByteOrder
	a         Attribute
	b         []byte
	i         int
	length    int
	err       error
}

var nlaHeaderLen = 4

func nlaAlign(l int) int {
	return ((l) + 3) & ^3
}

var nlmsgHeaderLen = nlaAlign(int(unsafe.Sizeof(NetlinkHeader{})))

func decUint16(b []byte) uint16 {
	return *(*uint16)(unsafe.Pointer(&b[0]))
}

func putUint16(b []byte, v uint16) {
	*(*uint16)(unsafe.Pointer(&b[0])) = v
}

func putUint32(b []byte, v uint32) {
	*(*uint32)(unsafe.Pointer(&b[0])) = v
}

func nativeEndian() binary.ByteOrder {
	b := uint16(0xff) // one byte
	if *(*byte)(unsafe.Pointer(&b)) == 0 {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// count scans the input slice to count the number of netlink attributes
// that could be decoded by adNext().
func adAvailable(ad *AttributeDecoder) (int, error) {
	var count int
	for i := 0; i < len(ad.b); {

		// Make sure there's at least a header's worth
		// of data to read on each iteration.
		if len(ad.b[i:]) < nlaHeaderLen {
			return 0, errInvalidAttribute
		}

		// Extract the length of the attribute.
		l := int(decUint16(ad.b[i : i+2]))

		// Ignore zero-length attributes.
		if l != 0 {
			count++
		}

		// Advance by at least a header's worth of bytes.
		if l < nlaHeaderLen {
			l = nlaHeaderLen
		}

		// Align to size 4
		i += nlaAlign(l)
	}

	return count, nil
}

// unmarshal unmarshals the contents of a byte slice into an Attribute.
func adUnmarshal(a *Attribute, b []byte) error {
	if len(b) < nlaHeaderLen {
		return errInvalidAttribute
	}

	a.Length = decUint16(b[0:2])
	a.Type = decUint16(b[2:4])

	if int(a.Length) > len(b) {
		return errInvalidAttribute
	}

	switch {
	// No length, no data
	case a.Length == 0:
		a.Data = make([]byte, 0)
	// Not enough length for any data
	case int(a.Length) < nlaHeaderLen:
		return errInvalidAttribute
	// Data present
	case int(a.Length) >= nlaHeaderLen:
		a.Data = make([]byte, len(b[nlaHeaderLen:a.Length]))
		copy(a.Data, b[nlaHeaderLen:a.Length])
	}

	return nil
}

// Bytes returns the raw bytes of the current Attribute's data.
func adBytes(ad *AttributeDecoder) []byte {
	src := ad.a.Data
	dest := make([]byte, len(src))
	copy(dest, src)
	return dest
}

// String returns a string with the contents of b from a null-terminated
// byte slice.
func adString(ad *AttributeDecoder) string {
	return string(bytes.TrimRight(ad.a.Data, "\x00"))
}

// adNext advances the decoder to the next netlink attribute.  It returns false
// when no more attributes are present, or an error was encountered.
func adNext(ad *AttributeDecoder) bool {
	if ad.err != nil {
		// Hit an error, stop iteration.
		return false
	}

	// Exit if array pointer is at or beyond the end of the slice.
	if ad.i >= len(ad.b) {
		return false
	}

	if err := adUnmarshal(&ad.a, ad.b[ad.i:]); err != nil {
		ad.err = err
		return false
	}

	// Advance the pointer by at least one header's length.
	if int(ad.a.Length) < nlaHeaderLen {
		ad.i += nlaHeaderLen
	} else {
		ad.i += nlaAlign(int(ad.a.Length))
	}

	return true
}

// Mask off any flags stored in the high bits.
func adType(ad *AttributeDecoder) uint16 {
	var attrTypeMask uint16 = 0x3fff
	return ad.a.Type & attrTypeMask
}

func newAttributeDecoder(b []byte) (*AttributeDecoder, error) {
	ad := &AttributeDecoder{
		ByteOrder: nativeEndian(),
		b:         b,
	}

	var err error
	ad.length, err = adAvailable(ad)
	if err != nil {
		return nil, err
	}

	return ad, nil
}

func marshalStruct(s interface{}) ([]byte, error) {
	var buf bytes.Buffer
	err := binary.Write(&buf, nativeEndian(), s)
	return buf.Bytes(), err
}

func unmarshalStruct(data []byte, s interface{}) error {
	b := bytes.NewReader(data)
	return binary.Read(b, nativeEndian(), s)
}

// marshals a Message into a byte slice.
func netlinkMarshalBinary(m NetlinkMessage) ([]byte, error) {
	ml := nlaAlign(int(m.Header.Length))
	if ml < nlmsgHeaderLen || ml != int(m.Header.Length) {
		return nil, errors.New("incorrect message length")
	}

	b := make([]byte, ml)

	putUint32(b[0:4], m.Header.Length)
	putUint16(b[4:6], uint16(m.Header.Type))
	putUint16(b[6:8], uint16(m.Header.Flags))
	putUint32(b[8:12], m.Header.Sequence)
	putUint32(b[12:16], m.Header.PID)
	copy(b[16:], m.Data)

	return b, nil
}

// Receive receives one or more Messages from netlink.
func netlinkReceive(fd int) ([]NetlinkMessage, error) {
	b := make([]byte, os.Getpagesize())
	for {
		// Peek at the buffer to see how many bytes are available.
		n, _, _, _, err := unix.Recvmsg(fd, b, nil, unix.MSG_PEEK)
		if err != nil {
			return nil, err
		}

		// Break when we can read all messages
		if n < len(b) {
			break
		}

		// Double in size if not enough bytes
		b = make([]byte, len(b)*2)
	}

	// Read out all available messages
	n, _, _, _, err := unix.Recvmsg(fd, b, nil, 0)
	if err != nil {
		return nil, err
	}

	raw, err := syscall.ParseNetlinkMessage(b[:nlaAlign(n)])
	if err != nil {
		return nil, err
	}

	msgs := make([]NetlinkMessage, 0, len(raw))
	for _, r := range raw {
		m := NetlinkMessage{
			Header: *(*NetlinkHeader)(unsafe.Pointer(&(r.Header))),
			Data:   r.Data,
		}

		msgs = append(msgs, m)
	}

	return msgs, nil
}

var netlinkMu sync.RWMutex

func netlinkSendRecv(fd int, m NetlinkMessage, seq uint32) ([]NetlinkMessage, error) {
	// Acquire the write lock and invoke the internal implementations of Send
	// and Receive which require the lock already be held.
	netlinkMu.Lock()
	defer netlinkMu.Unlock()

	ml := len(m.Data) + nlmsgHeaderLen
	if m.Header.Length == 0 {
		m.Header.Length = uint32(nlaAlign(ml))
	}

	if m.Header.Sequence == 0 {
		m.Header.Sequence = seq + 1
	}

	b, err := netlinkMarshalBinary(m)
	if err != nil {
		return nil, err
	}

	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Sendmsg(fd, b, nil, sa, 0); err != nil {
		return nil, err
	}

	var msgs []NetlinkMessage
	for {
		fdMsgs, err := netlinkReceive(fd)
		if err != nil {
			return nil, err
		}

		// If this message is multi-part, we will need to continue looping to
		// drain all the messages from the socket.
		var multi bool

		for _, m := range fdMsgs {
			// Does this message indicate a multi-part message?
			if (m.Header.Flags & 0x2) == 0 {
				// No, check the next messages.
				continue
			}

			// Does this message indicate the last message in a series of
			// multi-part messages from a single read?
			multi = m.Header.Type != 0x3
		}

		msgs = append(msgs, fdMsgs...)

		if !multi {
			break
		}
	}

	// When using nltest, it's possible for zero messages to be returned by receive.
	if len(msgs) == 0 {
		return msgs, nil
	}

	// Trim the final message with multi-part done indicator if
	// present.
	if r := msgs[len(msgs)-1]; (r.Header.Flags&0x2) != 0 && r.Header.Type == 0x3 {
		return msgs[:len(msgs)-1], nil
	}

	// Validate
	for _, r := range msgs {
		if r.Header.Sequence != m.Header.Sequence && m.Header.Sequence != 0 {
			return nil, errors.New("mismatched sequence")
		}

		if r.Header.PID != m.Header.PID && m.Header.PID != 0 && r.Header.PID != 0 {
			return nil, errors.New("mismatched pid")
		}
	}

	return msgs, nil
}

func getFilterMsgs(ifn string, fd int) ([]NetlinkMessage, error) {
	req := NetlinkMessage{
		Header: NetlinkHeader{
			Flags: unix.NLM_F_REQUEST | unix.NLM_F_DUMP,
			Type:  46, // RTM_GETTFILTER
		},
	}

	intf, err := net.InterfaceByName(ifn)
	if err != nil {
		return nil, fmt.Errorf("could not get ifIndex of p0: %v\n", err)
	}

	tcminfo, _ := marshalStruct(Msg{
		Family:  unix.AF_UNSPEC,
		Ifindex: uint32(intf.Index),
		Parent:  0xFFFFFFF2,
		Info:    0,
	})

	var data []byte
	data = append(data, tcminfo...)

	req.Data = data

	// Perform a request, receive replies, and validate the replies
	msgs, err := netlinkSendRecv(fd, req, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %v", err)
	}

	return msgs, nil
}

func unmarshalActStats(data []byte, info *FilterInfo) error {
	ad, err := newAttributeDecoder(data)
	if err != nil {
		return err
	}
	stat := &GenBasic{}
	statHw := &GenBasic{}
	for adNext(ad) {
		switch adType(ad) {
		case TCA_STATS_BASIC:
			if err = unmarshalStruct(adBytes(ad), stat); err != nil {
				return err
			}
		case TCA_STATS_BASIC_HW:
			if err = unmarshalStruct(adBytes(ad), statHw); err != nil {
				return err
			}
		}
	}

	if statHw.Bytes == 0 && statHw.Packets == 0 {
		return nil
	}

	if stat.Bytes >= statHw.Bytes && stat.Packets >= statHw.Packets {
		info.SentSwBytes = stat.Bytes - statHw.Bytes
		info.SentSwPackets = stat.Packets - statHw.Packets
		info.SentHwBytes = statHw.Bytes
		info.SentHwPackets = statHw.Packets
	}

	return ad.err
}

func unmarshalAction(data []byte, info *FilterInfo) error {
	ad, err := newAttributeDecoder(data)
	if err != nil {
		return err
	}
	for adNext(ad) {
		switch adType(ad) {
		case TCA_ACT_STATS:
			if err := unmarshalActStats(adBytes(ad), info); err != nil {
				return err
			}
		}
	}

	return ad.err
}

func unmarshalActions(data []byte, info *FilterInfo) error {
	ad, err := newAttributeDecoder(data)
	if err != nil {
		return err
	}
	for adNext(ad) {
		if err := unmarshalAction(adBytes(ad), info); err != nil {
			return err
		}
	}
	return ad.err
}

// See https://tools.ietf.org/html/rfc3549#section-3.1.3
func parseMessage(msg NetlinkMessage) (FilterInfo, error) {
	var m FilterInfo

	if err := unmarshalStruct(msg.Data[:20], &m.Msg); err != nil {
		return m, err
	}

	ad, err := newAttributeDecoder(msg.Data[20:])
	if err != nil {
		return m, err
	}

	var opts []byte
	for adNext(ad) {
		switch adType(ad) {
		case TCA_KIND:
			m.Kind = adString(ad)
		case TCA_OPTIONS:
			opts = adBytes(ad)
		default:
		}
	}

	if len(opts) == 0 || m.Kind != "flower" {
		return m, err
	}

	adf, err := newAttributeDecoder(opts)
	if err != nil {
		return m, err
	}
	for adNext(adf) {
		switch adType(adf) {
		case TCA_FLOWER_ACT:
			err = unmarshalActions(adBytes(adf), &m)
			if err != nil {
				return m, err
			}
		}
	}

	return m, err
}

func NetlinkFilterGet(ifn string, fd int) ([]FilterInfo, error) {
	var res []FilterInfo

	msgs, err := getFilterMsgs(ifn, fd)
	if err != nil {
		return nil, err
	}

	for _, msg := range msgs {
		m, err := parseMessage(msg)
		if err != nil {
			return nil, err
		}

		res = append(res, m)
	}

	return res, nil
}

func NetlinkFilterOpen() (int, error) {
	var fd int
	var err error

	for {
		fd, err = unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
		if err == nil {
			// No error, prepare the Conn.
			break
		}
		if err == unix.EAGAIN || err == unix.EINPROGRESS || err == unix.EINTR {
			// System call interrupted or not ready, try again.
			continue
		}
		// Unhandled error.
		return -1, err
	}

	addr := &unix.SockaddrNetlink{
		Family: unix.AF_NETLINK,
		Groups: 0,
		Pid:    0,
	}

	if err := unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return -1, err
	}

	return fd, nil
}

func NetlinkFilterClose(fd int) {
	unix.Close(fd)
}
