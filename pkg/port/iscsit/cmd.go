package iscsit

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/gostor/gotgt/pkg/util"
)

type OpCode int

const (
	// Defined on the initiator.
	OpNoopOut     OpCode = 0x00
	OpSCSICmd            = 0x01
	OpSCSITaskReq        = 0x02
	OpLoginReq           = 0x03
	OpTextReq            = 0x04
	OpSCSIOut            = 0x05
	OpLogoutReq          = 0x06
	OpSNACKReq           = 0x10
	// Defined on the target.
	OpNoopIn       OpCode = 0x20
	OpSCSIResp            = 0x21
	OpSCSITaskResp        = 0x22
	OpLoginResp           = 0x23
	OpTextResp            = 0x24
	OpSCSIIn              = 0x25
	OpLogoutResp          = 0x26
	OpReady               = 0x31
	OpAsync               = 0x32
	OpReject              = 0x3f
)

var opCodeMap = map[OpCode]string{
	OpNoopOut:      "NOP-Out",
	OpSCSICmd:      "SCSI Command",
	OpSCSITaskReq:  "SCSI Task Management FunctionRequest",
	OpLoginReq:     "Login Request",
	OpTextReq:      "Text Request",
	OpSCSIOut:      "SCSI Data-Out (write)",
	OpLogoutReq:    "Logout Request",
	OpSNACKReq:     "SNACK Request",
	OpNoopIn:       "NOP-In",
	OpSCSIResp:     "SCSI Response",
	OpSCSITaskResp: "SCSI Task Management Function Response",
	OpLoginResp:    "Login Response",
	OpTextResp:     "Text Response",
	OpSCSIIn:       "SCSI Data-In (read)",
	OpLogoutResp:   "Logout Response",
	OpReady:        "Ready To Transfer (R2T)",
	OpAsync:        "Asynchronous Message",
	OpReject:       "Reject",
}

const DataPadding = 4

type ISCSICommand struct {
	OpCode             OpCode
	RawHeader          []byte
	DataLen            int
	RawData            []byte
	Final              bool
	Immediate          bool
	TaskTag            uint32
	ExpCmdSN, MaxCmdSN uint32
	AHSLen             int

	ConnID    uint16 // Connection ID.
	CmdSN     uint32 // Command serial number.
	ExpStatSN uint32 // Expected status serial.

	Read, Write bool
	LUN         [8]uint8
	Transit     bool   // Transit bit.
	Cont        bool   // Continue bit.
	CSG, NSG    Stage  // Current Stage, Next Stage.
	ISID        uint64 // Initiator part of the SSID.
	TSIH        uint16 // Target-assigned Session Identifying Handle.
	StatSN      uint32 // Status serial number.

	// For login response.
	StatusClass  uint8
	StatusDetail uint8

	// SCSI commands
	ExpectedDataLen uint32
	CDB             []byte
	Status          byte
	SCSIResponse    byte

	// Data-In
	HasStatus    bool
	DataSN       uint32
	BufferOffset uint32
}

func (cmd *ISCSICommand) Bytes() []byte {
	switch cmd.OpCode {
	case OpLoginResp:
		return cmd.loginRespBytes()
	case OpLogoutResp:
		return cmd.logoutRespBytes()
	case OpSCSIResp:
		return cmd.scsiCmdRespBytes()
	case OpSCSIIn:
		return cmd.dataInBytes()
	case OpTextResp:
		return cmd.textRespBytes()
	case OpNoopIn:
		return cmd.noopInBytes()
	}
	return nil
}

func (m *ISCSICommand) String() string {
	var s []string
	s = append(s, fmt.Sprintf("Op: %v", opCodeMap[m.OpCode]))
	s = append(s, fmt.Sprintf("Final = %v", m.Final))
	s = append(s, fmt.Sprintf("Immediate = %v", m.Immediate))
	s = append(s, fmt.Sprintf("Data Segment Length = %d", m.DataLen))
	s = append(s, fmt.Sprintf("Task Tag = %x", m.TaskTag))
	s = append(s, fmt.Sprintf("AHS Length = %d", m.AHSLen))
	switch m.OpCode {
	case OpLoginReq:
		s = append(s, fmt.Sprintf("ISID = %x", m.ISID))
		s = append(s, fmt.Sprintf("Transit = %v", m.Transit))
		s = append(s, fmt.Sprintf("Continue = %v", m.Cont))
		s = append(s, fmt.Sprintf("Current Stage = %v", m.CSG))
		s = append(s, fmt.Sprintf("Next Stage = %v", m.NSG))
	case OpLoginResp:
		s = append(s, fmt.Sprintf("ISID = %x", m.ISID))
		s = append(s, fmt.Sprintf("Transit = %v", m.Transit))
		s = append(s, fmt.Sprintf("Continue = %v", m.Cont))
		s = append(s, fmt.Sprintf("Current Stage = %v", m.CSG))
		s = append(s, fmt.Sprintf("Next Stage = %v", m.NSG))
		s = append(s, fmt.Sprintf("Status Class = %d", m.StatusClass))
		s = append(s, fmt.Sprintf("Status Detail = %d", m.StatusDetail))
	case OpSCSICmd:
		s = append(s, fmt.Sprintf("LUN = %d", m.LUN))
		s = append(s, fmt.Sprintf("ExpectedDataLen = %d", m.ExpectedDataLen))
		s = append(s, fmt.Sprintf("CmdSN = %d", m.CmdSN))
		s = append(s, fmt.Sprintf("ExpStatSN = %d", m.ExpStatSN))
		s = append(s, fmt.Sprintf("Read = %v", m.Read))
		s = append(s, fmt.Sprintf("Write = %v", m.Write))
		s = append(s, fmt.Sprintf("CDB = %x", m.CDB))
	case OpSCSIResp:
		s = append(s, fmt.Sprintf("StatSN = %d", m.StatSN))
		s = append(s, fmt.Sprintf("ExpCmdSN = %d", m.ExpCmdSN))
		s = append(s, fmt.Sprintf("MaxCmdSN = %d", m.MaxCmdSN))
	}
	return strings.Join(s, "\n")
}

// parseUint parses the given slice as a network-byte-ordered integer.  If
// there are more than 8 bytes in data, it overflows.
func ParseUint(data []byte) uint64 {
	var out uint64
	for i := 0; i < len(data); i++ {
		out += uint64(data[len(data)-i-1]) << uint(8*i)
	}
	return out
}

func parseHeader(data []byte) (*ISCSICommand, error) {
	if len(data) != BHS_SIZE {
		return nil, fmt.Errorf("garbled header")
	}
	// TODO: sync.Pool
	m := &ISCSICommand{}
	m.Immediate = 0x40&data[0] == 0x40
	m.OpCode = OpCode(data[0] & 0x3f)
	m.Final = 0x80&data[1] == 0x80
	m.AHSLen = int(data[4]) * 4
	m.DataLen = int(ParseUint(data[5:8]))
	m.TaskTag = uint32(ParseUint(data[16:20]))
	switch m.OpCode {
	case OpSCSICmd:
		m.LUN = [8]uint8{data[9]}
		m.ExpectedDataLen = uint32(ParseUint(data[20:24]))
		m.CmdSN = uint32(ParseUint(data[24:28]))
		m.Read = data[1]&0x40 == 0x40
		m.Write = data[1]&0x20 == 0x20
		m.CDB = data[32:48]
		m.ExpStatSN = uint32(ParseUint(data[28:32]))
	case OpSCSIResp:
	case OpLoginReq, OpTextReq, OpNoopOut, OpLogoutReq:
		m.Transit = m.Final
		m.Cont = data[1]&0x40 == 0x40
		if m.Cont && m.Transit {
			// rfc7143 11.12.2
			return nil, fmt.Errorf("transit and continue bits set in same login request")
		}
		m.CSG = Stage(data[1]&0xc) >> 2
		m.NSG = Stage(data[1] & 0x3)
		m.ISID = uint64(ParseUint(data[8:14]))
		m.TSIH = uint16(ParseUint(data[14:16]))
		m.ConnID = uint16(ParseUint(data[20:22]))
		m.CmdSN = uint32(ParseUint(data[24:28]))
		m.ExpStatSN = uint32(ParseUint(data[28:32]))
	case OpLoginResp:
		m.Transit = m.Final
		m.Cont = data[1]&0x40 == 0x40
		if m.Cont && m.Transit {
			// rfc7143 11.12.2
			return nil, fmt.Errorf("transit and continue bits set in same login request")
		}
		m.CSG = Stage(data[1]&0xc) >> 2
		m.NSG = Stage(data[1] & 0x3)
		m.StatSN = uint32(ParseUint(data[24:28]))
		m.ExpCmdSN = uint32(ParseUint(data[28:32]))
		m.MaxCmdSN = uint32(ParseUint(data[32:36]))
		m.StatusClass = uint8(data[36])
		m.StatusDetail = uint8(data[37])
	}
	return m, nil
}

func (m *ISCSICommand) scsiCmdRespBytes() []byte {
	// rfc7143 11.4
	buf := &bytes.Buffer{}
	buf.WriteByte(byte(OpSCSIResp))
	buf.WriteByte(0x80) // 11.4.1 = wtf
	buf.WriteByte(byte(m.SCSIResponse))
	buf.WriteByte(byte(m.Status))

	// Skip through to byte 16
	for i := 0; i < 3*4; i++ {
		buf.WriteByte(0x00)
	}
	buf.Write(util.MarshalUint64(uint64(m.TaskTag))[4:])
	for i := 0; i < 4; i++ {
		buf.WriteByte(0x00)
	}
	buf.Write(util.MarshalUint64(uint64(m.StatSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.ExpCmdSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.MaxCmdSN))[4:])
	for i := 0; i < 3*4; i++ {
		buf.WriteByte(0x00)
	}

	return buf.Bytes()
}

func (m *ISCSICommand) dataInBytes() []byte {
	// rfc7143 11.7
	buf := &bytes.Buffer{}
	buf.WriteByte(byte(OpSCSIIn))
	var b byte
	b = 0x80
	if m.HasStatus {
		b |= 0x01
	}
	buf.WriteByte(b)
	buf.WriteByte(0x00)
	if m.HasStatus {
		b = byte(m.Status)
	}
	buf.WriteByte(b)

	buf.WriteByte(0x00)                                       // 4
	buf.Write(util.MarshalUint64(uint64(len(m.RawData)))[5:]) // 5-8
	// Skip through to byte 16
	for i := 0; i < 8; i++ {
		buf.WriteByte(byte(m.LUN[i]))
	}
	buf.Write(util.MarshalUint64(uint64(m.TaskTag))[4:])
	for i := 0; i < 4; i++ {
		// 11.7.4
		buf.WriteByte(0xff)
	}
	buf.Write(util.MarshalUint64(uint64(m.StatSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.ExpCmdSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.MaxCmdSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.DataSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.BufferOffset))[4:])
	for i := 0; i < 4; i++ {
		buf.WriteByte(0x00)
	}
	buf.Write(m.RawData)
	dl := len(m.RawData)
	for dl%4 > 0 {
		dl++
		buf.WriteByte(0x00)
	}

	return buf.Bytes()
}

func (m *ISCSICommand) textRespBytes() []byte {
	buf := &bytes.Buffer{}

	buf.WriteByte(byte(OpTextResp))
	var b byte
	if m.Final {
		b |= 0x80
	}
	if m.Cont {
		b |= 0x40
	}
	// byte 1
	buf.WriteByte(b)

	b = 0
	buf.WriteByte(b)
	buf.WriteByte(b)
	buf.WriteByte(b)
	buf.Write(util.MarshalUint64(uint64(len(m.RawData)))[5:]) // 5-8
	// Skip through to byte 12
	for i := 0; i < 2*4; i++ {
		buf.WriteByte(0x00)
	}
	buf.Write(util.MarshalUint64(uint64(m.TaskTag))[4:])
	for i := 0; i < 4; i++ {
		buf.WriteByte(0xff)
	}
	buf.Write(util.MarshalUint64(uint64(m.StatSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.ExpCmdSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.MaxCmdSN))[4:])
	for i := 0; i < 3*4; i++ {
		buf.WriteByte(0x00)
	}
	rd := m.RawData
	for len(rd)%4 != 0 {
		rd = append(rd, 0)
	}
	buf.Write(rd)
	return buf.Bytes()
}

func (m *ISCSICommand) noopInBytes() []byte {
	buf := &bytes.Buffer{}

	buf.WriteByte(byte(OpNoopIn))
	var b byte
	b |= 0x80
	// byte 1
	buf.WriteByte(b)

	b = 0
	buf.WriteByte(b)
	buf.WriteByte(b)
	buf.WriteByte(b)
	buf.Write(util.MarshalUint64(uint64(len(m.RawData)))[5:]) // 5-8
	// Skip through to byte 12
	for i := 0; i < 2*4; i++ {
		buf.WriteByte(0x00)
	}
	buf.Write(util.MarshalUint64(uint64(m.TaskTag))[4:])
	for i := 0; i < 4; i++ {
		buf.WriteByte(0xff)
	}
	buf.Write(util.MarshalUint64(uint64(m.StatSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.ExpCmdSN))[4:])
	buf.Write(util.MarshalUint64(uint64(m.MaxCmdSN))[4:])
	for i := 0; i < 3*4; i++ {
		buf.WriteByte(0x00)
	}
	rd := m.RawData
	for len(rd)%4 != 0 {
		rd = append(rd, 0)
	}
	buf.Write(rd)
	return buf.Bytes()
}