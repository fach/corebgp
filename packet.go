package corebgp

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"time"
)

const (
	openMessageType         = 1
	updateMessageType       = 2
	notificationMessageType = 3
	keepAliveMessageType    = 4
)

type message interface {
	messageType() uint8
}

const (
	headerLength = 19
)

func messageFromBytes(b []byte, messageType uint8) (message, error) {
	switch messageType {
	case openMessageType:
		o := &openMessage{}
		err := o.decode(b)
		if err != nil {
			return nil, err
		}
		return o, nil
	case updateMessageType:
		u := make([]byte, len(b))
		copy(u, b)
		return updateMessage(u), nil
	case notificationMessageType:
		n := &Notification{}
		err := n.decode(b)
		if err != nil {
			return nil, err
		}
		return n, nil
	case keepAliveMessageType:
		k := &keepAliveMessage{}
		return k, nil
	default:
		badType := make([]byte, 1)
		badType[0] = messageType
		n := newNotification(NotifCodeMessageHeaderErr, NotifSubcodeBadType,
			badType)
		return nil, newNotificationError(n, true)
	}
}

func prependHeader(m []byte, t uint8) []byte {
	b := make([]byte, headerLength)
	for i := 0; i < 16; i++ {
		b[i] = 0xFF
	}
	msgLen := uint16(len(m) + headerLength)
	binary.BigEndian.PutUint16(b[16:], msgLen)
	b[18] = t
	b = append(b, m...)
	return b
}

// Notification is a Notification message.
type Notification struct {
	Code    uint8
	Subcode uint8
	Data    []byte
}

func newNotification(code, subcode uint8, data []byte) *Notification {
	return &Notification{
		Code:    code,
		Subcode: subcode,
		Data:    data,
	}
}

func (n *Notification) messageType() uint8 {
	return notificationMessageType
}

func (n *Notification) decode(b []byte) error {
	/*
		   If a peer sends a NOTIFICATION message, and the receiver of the
			 message detects an error in that message, the receiver cannot use a
			 NOTIFICATION message to report this error back to the peer.  Any such
			 error (e.g., an unrecognized Error Code or Error Subcode) SHOULD be
			 noticed, logged locally, and brought to the attention of the
			 administration of the peer.  The means to do this, however, lies
			 outside the scope of this document.
	*/
	if len(b) < 2 {
		return errors.New("notification message too short")
	}
	n.Code = b[0]
	n.Subcode = b[1]
	if len(b) > 2 {
		n.Data = make([]byte, len(b)-2)
		copy(n.Data, b[2:])
	}
	return nil
}

func (n *Notification) encode() ([]byte, error) {
	b := make([]byte, 2)
	b[0] = n.Code
	b[1] = n.Subcode
	if len(n.Data) > 1 {
		b = append(b, n.Data...)
	}
	return prependHeader(b, notificationMessageType), nil
}

// Notification code values
const (
	NotifCodeMessageHeaderErr uint8 = 1
	NotifCodeOpenMessageErr   uint8 = 2
	NotifCodeUpdateMessageErr uint8 = 3
	NotifCodeHoldTimerExpired uint8 = 4
	NotifCodeFSMErr           uint8 = 5
	NotifCodeCease            uint8 = 6
)

// message header Notification subcode values
const (
	NotifSubcodeConnNotSync uint8 = 1
	NotifSubcodeBadLength   uint8 = 2
	NotifSubcodeBadType     uint8 = 3
)

// open message Notification subcode values
const (
	NotifSubcodeUnsupportedVersionNumber uint8 = 1
	NotifSubcodeBadPeerAS                uint8 = 2
	NotifSubcodeBadBgpID                 uint8 = 3
	NotifSubcodeUnsupportedOptionalParam uint8 = 4
	NotifSubcodeUnacceptableHoldTime     uint8 = 5
	NotifSubcodeUnsupportedCapability    uint8 = 6
)

// update message Notification subcode values
const (
	NotifSubcodeMalformedAttr             uint8 = 1
	NotifSubcodeUnrecognizedWellKnownAttr uint8 = 2
	NotifSubcodeMissingWellKnownAttr      uint8 = 3
	NotifSubcodeAttrFlagsError            uint8 = 4
	NotifSubcodeAttrLenError              uint8 = 5
	NotifSubcodeInvalidOrigin             uint8 = 6
	NotifSubcodeInvalidNextHop            uint8 = 8
	NotifSubcodeOptionalAttrError         uint8 = 9
	NotifSubcodeInvalidNetworkField       uint8 = 10
	NotifSubcodeMalformedASPath           uint8 = 11
)

// finite state machine error subcode values [RFC6608]
const (
	NotifSubcodeUnexpectedMessageOpenSent    uint8 = 1
	NotifSubcodeUnexpectedMessageOpenConfirm uint8 = 2
	NotifSubcodeUnexpectedMessageEstablished uint8 = 3
)

type openMessage struct {
	version        uint8
	asn            uint16
	holdTime       uint16
	bgpID          uint32
	optionalParams []optionalParam
}

func (o *openMessage) messageType() uint8 {
	return openMessageType
}

// https://tools.ietf.org/html/rfc4271#section-6.2
func (o *openMessage) validate(localID, localAS, remoteAS uint32) error {
	if o.version != 4 {
		version := make([]byte, 2)
		binary.BigEndian.PutUint16(version, uint16(4))
		n := newNotification(NotifCodeOpenMessageErr,
			NotifSubcodeUnsupportedVersionNumber, version)
		return newNotificationError(n, true)
	}
	var fourOctetAS, fourOctetASFound bool
	if o.asn == asTrans {
		fourOctetAS = true
	} else if uint32(o.asn) != remoteAS {
		n := newNotification(NotifCodeOpenMessageErr, NotifSubcodeBadPeerAS,
			nil)
		return newNotificationError(n, true)
	}
	if o.holdTime < 3 && o.holdTime != 0 {
		n := newNotification(NotifCodeOpenMessageErr,
			NotifSubcodeUnacceptableHoldTime, nil)
		return newNotificationError(n, true)
	}
	id := net.IP(make([]byte, 4))
	binary.BigEndian.PutUint32(id, o.bgpID)
	if !id.IsGlobalUnicast() {
		n := newNotification(NotifCodeOpenMessageErr, NotifSubcodeBadBgpID, nil)
		return newNotificationError(n, true)
	}
	// https://tools.ietf.org/html/rfc6286#section-2.2
	if localAS == remoteAS && localID == o.bgpID {
		n := newNotification(NotifCodeOpenMessageErr, NotifSubcodeBadBgpID, nil)
		return newNotificationError(n, true)
	}
	caps := o.getCapabilities()
	for _, c := range caps {
		if c.Code == capCodeFourOctetAS {
			fourOctetASFound = true
			if len(c.Value) != 4 {
				n := newNotification(NotifCodeOpenMessageErr, 0, nil)
				return newNotificationError(n, true)
			}
			if binary.BigEndian.Uint32(c.Value) != remoteAS {
				n := newNotification(NotifCodeOpenMessageErr,
					NotifSubcodeBadPeerAS, nil)
				return newNotificationError(n, true)
			}
		}
	}
	if fourOctetAS && !fourOctetASFound {
		n := newNotification(NotifCodeOpenMessageErr, NotifSubcodeBadPeerAS,
			nil)
		return newNotificationError(n, true)
	}
	return nil
}

func (o *openMessage) getCapabilities() []*Capability {
	caps := make([]*Capability, 0)
	for _, param := range o.optionalParams {
		p, isCap := param.(*capabilityOptionalParam)
		if isCap {
			caps = append(caps, p.capabilities...)
		}
	}
	return caps
}

func (o *openMessage) decode(b []byte) error {
	if len(b) < 10 {
		n := newNotification(NotifCodeMessageHeaderErr, NotifSubcodeBadLength,
			b)
		return newNotificationError(n, true)
	}
	o.version = b[0]
	o.asn = binary.BigEndian.Uint16(b[1:3])
	o.holdTime = binary.BigEndian.Uint16(b[3:5])
	o.bgpID = binary.BigEndian.Uint32(b[5:9])
	optionalParamsLen := int(b[9])
	if optionalParamsLen != len(b)-10 {
		n := newNotification(NotifCodeOpenMessageErr, 0, nil)
		return newNotificationError(n, true)
	}
	optionalParams, err := decodeOptionalParams(b[10:])
	if err != nil {
		return err
	}
	o.optionalParams = optionalParams
	return nil
}

func decodeOptionalParams(b []byte) ([]optionalParam, error) {
	params := make([]optionalParam, 0)
	for {
		if len(b) < 2 {
			n := newNotification(NotifCodeOpenMessageErr, 0, nil)
			return nil, newNotificationError(n, true)
		}
		paramCode := b[0]
		paramLen := b[1]
		if len(b) < int(paramLen)+2 {
			n := newNotification(NotifCodeOpenMessageErr, 0, nil)
			return nil, newNotificationError(n, true)
		}
		paramToDecode := make([]byte, 0)
		if paramLen > 0 {
			paramToDecode = b[2 : paramLen+2]
		}
		nextParam := 2 + int(paramLen)
		b = b[nextParam:]
		switch paramCode {
		case capabilityOptionalParamType:
			cap := &capabilityOptionalParam{}
			err := cap.decode(paramToDecode)
			if err != nil {
				return nil, err
			}
			params = append(params, cap)
		default:
			n := newNotification(NotifCodeOpenMessageErr,
				NotifSubcodeUnsupportedOptionalParam, nil)
			return nil, newNotificationError(n, true)
		}
		if len(b) == 0 {
			break
		}
	}
	return params, nil
}

func (o *openMessage) encode() ([]byte, error) {
	b := make([]byte, 9)
	b[0] = o.version
	binary.BigEndian.PutUint16(b[1:3], o.asn)
	binary.BigEndian.PutUint16(b[3:5], o.holdTime)
	binary.BigEndian.PutUint32(b[5:9], o.bgpID)
	params := make([]byte, 0)
	for _, param := range o.optionalParams {
		p, err := param.encode()
		if err != nil {
			return nil, err
		}
		params = append(params, p...)
	}
	b = append(b, uint8(len(params)))
	b = append(b, params...)
	return prependHeader(b, openMessageType), nil
}

const (
	capCodeFourOctetAS uint8 = 65
)

const (
	asTrans uint16 = 23456
)

func newOpenMessage(asn uint32, holdTime time.Duration, bgpID uint32,
	caps []*Capability) (*openMessage, error) {
	allCaps := make([]*Capability, 0)
	fourOctetAS := &Capability{
		Code:  capCodeFourOctetAS,
		Value: make([]byte, 4),
	}
	binary.BigEndian.PutUint32(fourOctetAS.Value, asn)
	allCaps = append(allCaps, fourOctetAS)
	for _, cap := range caps {
		// ignore four octet as capability as we include this implicitly above
		if cap.Code != capCodeFourOctetAS {
			allCaps = append(allCaps, cap)
		}
	}
	o := &openMessage{
		version:  4,
		holdTime: uint16(holdTime.Truncate(time.Second).Seconds()),
		bgpID:    bgpID,
		optionalParams: []optionalParam{
			&capabilityOptionalParam{
				capabilities: allCaps,
			},
		},
	}
	if asn > math.MaxUint16 {
		o.asn = asTrans
	} else {
		o.asn = uint16(asn)
	}
	return o, nil
}

const (
	capabilityOptionalParamType uint8 = 2
)

type optionalParam interface {
	paramType() uint8
	encode() ([]byte, error)
	decode(b []byte) error
}

type capabilityOptionalParam struct {
	capabilities []*Capability
}

func (c *capabilityOptionalParam) paramType() uint8 {
	return capabilityOptionalParamType
}

func (c *capabilityOptionalParam) decode(b []byte) error {
	for {
		if len(b) < 2 {
			n := newNotification(NotifCodeOpenMessageErr, 0, nil)
			return newNotificationError(n, true)
		}
		capCode := b[0]
		capLen := b[1]
		if len(b) < int(capLen)+2 {
			n := newNotification(NotifCodeOpenMessageErr, 0, nil)
			return newNotificationError(n, true)
		}
		capValue := make([]byte, 0)
		if capLen > 0 {
			capValue = b[2 : capLen+2]
		}
		cap := &Capability{
			Code:  capCode,
			Value: capValue,
		}
		c.capabilities = append(c.capabilities, cap)
		nextCap := 2 + int(capLen)
		b = b[nextCap:]
		if len(b) == 0 {
			return nil
		}
	}
}

func (c *capabilityOptionalParam) encode() ([]byte, error) {
	b := make([]byte, 0)
	caps := make([]byte, 0)
	if len(c.capabilities) > 0 {
		for _, cap := range c.capabilities {
			caps = append(caps, cap.Code)
			caps = append(caps, uint8(len(cap.Value)))
			caps = append(caps, cap.Value...)
		}
	} else {
		return nil, errors.New("empty capabilities in capability optional param")
	}
	b = append(b, capabilityOptionalParamType)
	b = append(b, uint8(len(caps)))
	b = append(b, caps...)
	return b, nil
}

// Capability is a BGP capability as defined by RFC5492.
type Capability struct {
	Code  uint8
	Value []byte
}

type updateMessage []byte

func (u updateMessage) messageType() uint8 {
	return updateMessageType
}

type keepAliveMessage struct{}

func (k keepAliveMessage) messageType() uint8 {
	return keepAliveMessageType
}

func (k keepAliveMessage) encode() ([]byte, error) {
	return prependHeader(nil, keepAliveMessageType), nil
}
