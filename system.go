package gos7

// Copyright 2018 Trung Hieu Le. All rights reserved.
// This software may be modified and distributed under the terms
// of the BSD license. See the LICENSE file for details.
import (
	"encoding/binary"
	"fmt"
	"strings"
)

//SZLHeader See §33.1 of "System Software for S7-300/400 System and Standard Functions" and see SFC51 description too
type SZLHeader struct {
	LengthHeader       uint16
	NumberOfDataRecord uint16
}

//S7SZL constains header and data
type S7SZL struct {
	Header SZLHeader
	Data   []byte
}

// S7SZLList of available SZL IDs : same as SZL but List items are big-endian adjusted
type S7SZLList struct {
	Header SZLHeader
	Data   []uint16
}

// S7Protection See §33.19 of "System Software for S7-300/400 System and Standard Functions"
type S7Protection struct {
	schSchal uint // sch_schal: Protection level set with the mode selector (1, 2, 3)
	schPar   uint // sch_par: Protection level set in parameters (0, 1, 2, 3; 0: no password,protection level invalid)
	schRel   uint // sch_rel: Valid protection level of the CPU
	bartSch  uint // bart_sch: Mode selector setting (1:RUN, 2:RUN-P, 3:STOP, 4:MRES,0:undefined or cannot be determined)
	anlSch   uint // anl_sch:Startup switch setting (1:CRST, 2:WRST, 0:undefined, does not exist of cannot be determined)
}

//S7OrderCode Order Code + Version
type S7OrderCode struct {
	Code string // such as "6ES7 151-8AB01-0AB0"
	V1   byte   // Version 1st digit
	V2   byte   // Version 2nd digit
	V3   byte   // Version 3th digit
}

//S7CpuInfo CPU Info
type S7CpuInfo struct {
	ModuleTypeName string
	SerialNumber   string
	ASName         string
	Copyright      string
	ModuleName     string
}

//S7CpInfo cp info
type S7CpInfo struct {
	MaxPduLength   int
	MaxConnections int
	MaxMpiRate     int
	MaxBusRate     int
}

//implement GetCPUInfo
func (mb *client) GetCPUInfo() (info S7CpuInfo, err error) {

	szl, _, err := mb.readSzl(0x001C, 0x000)
	if err == nil && len(szl.Data) < 204 {
		err = fmt.Errorf(ErrorText(errCliInvalidPlcAnswer))
	}
	if err == nil {
		moduleTypeName := string(szl.Data[172 : 172+32])
		serialNumber := string(szl.Data[138 : 138+24])
		asName := string(szl.Data[2 : 2+24])
		copyRight := string(szl.Data[104 : 104+26])
		moduleName := string(szl.Data[36 : 36+24])

		info.ModuleTypeName = strings.TrimSpace(moduleTypeName)
		info.SerialNumber = strings.TrimSpace(serialNumber)
		info.ASName = strings.TrimSpace(asName)
		info.Copyright = strings.TrimSpace(copyRight)
		info.ModuleName = strings.TrimSpace(moduleName)
	}
	return
}

//implement of GetCPInfo
func (mb *client) GetCPInfo() (info S7CpInfo, err error) {
	szl, _, err := mb.readSzl(0x0131, 0x000)
	if err == nil && len(szl.Data) < 12 {
		err = fmt.Errorf(ErrorText(errCliInvalidPlcAnswer))
	}
	if err == nil {
		info.MaxPduLength = int(binary.BigEndian.Uint16(szl.Data[2:]))
		info.MaxConnections = int(binary.BigEndian.Uint16(szl.Data[4:]))
		info.MaxMpiRate = int(binary.BigEndian.Uint16(szl.Data[6:]))
		info.MaxBusRate = int(binary.BigEndian.Uint16(szl.Data[10:]))
	}
	return
}

//implement of GetOrderCode
//
// SZL 0x0011 is a list of 28 byte records: index (2), MlfB (20), BGTyp (2),
// Ausbg1 (2), Ausbe1 (2). Index 0x0001 carries the order number of the module
// and index 0x0007 the basic firmware, whose Ausbg1 low byte and Ausbe1 hold
// the version as <major>.<minor>.<patch>. CPUs may append further records
// (e.g. 0x0081, firmware extension / boot loader), so the version is taken
// from the 0x0007 record rather than from the last bytes of the list.
func (mb *client) GetOrderCode() (info S7OrderCode, err error) {
	const recordLen = 28
	szl, _, err := mb.readSzl(0x0011, 0x000)
	if err != nil {
		return
	}
	if len(szl.Data) < recordLen {
		err = fmt.Errorf(ErrorText(errCliInvalidPlcAnswer))
		return
	}
	step := int(szl.Header.LengthHeader)
	if step < recordLen {
		step = recordLen
	}
	var module, firmware, last []byte
	for off := 0; off+recordLen <= len(szl.Data); off += step {
		rec := szl.Data[off : off+recordLen]
		switch binary.BigEndian.Uint16(rec) {
		case 0x0001:
			if module == nil {
				module = rec
			}
		case 0x0007:
			if firmware == nil {
				firmware = rec
			}
		}
		last = rec
	}
	if module == nil {
		module = szl.Data[:recordLen]
	}
	if firmware == nil {
		firmware = last
	}
	info.Code = string(module[2:22])
	info.V1 = firmware[25]
	info.V2 = firmware[26]
	info.V3 = firmware[27]
	return
}

//internal function readSZL
//
// Response layout (offsets into the raw frame, TPKT header included):
//
//	24 sequence, 26 last data unit (0x00 = last), 27-28 error code,
//	29 return code (0xFF = ok), 31-32 data length,
//	first slice:  33-34 SZL ID, 35-36 index, 37-38 LENTHDR, 39-40 N_DR, 41.. records
//	next slices:  33.. records
func (mb *client) readSzl(id int, index int) (szl S7SZL, size int, err error) {
	const (
		szlFirstDataOffset = 41 // records start after ID, index and the SZL header
		szlNextDataOffset  = 33 // continuation slices carry records only
	)
	offset := 0
	var done bool
	first := true
	var seqIn byte = 0x00
	var seqOut uint16 = 0x0000
	s7SZLFirst := make([]byte, len(s7SZLFirstTelegram))
	copy(s7SZLFirst, s7SZLFirstTelegram)
	s7SZLNext := make([]byte, len(s7SZLNextTelegram))
	copy(s7SZLNext, s7SZLNextTelegram)
	for !done && err == nil {
		res := &ProtocolDataUnit{}
		if first {
			binary.BigEndian.PutUint16(s7SZLFirst[11:], seqOut+1)
			binary.BigEndian.PutUint16(s7SZLFirst[29:], uint16(id))
			binary.BigEndian.PutUint16(s7SZLFirst[31:], uint16(index))
			request := NewProtocolDataUnit(s7SZLFirst)
			//send
			res, err = mb.send(&request)
		} else {
			binary.BigEndian.PutUint16(s7SZLNext[11:], seqOut+1)
			s7SZLNext[24] = byte(seqIn)
			request := NewProtocolDataUnit(s7SZLNext)
			//send
			res, err = mb.send(&request)
		}
		if err != nil {
			return
		}
		if length := len(res.Data); length <= 32 {
			err = fmt.Errorf(ErrorText(errIsoInvalidPDU))
			return
		}
		if binary.BigEndian.Uint16(res.Data[27:]) != 0 {
			err = fmt.Errorf(ErrorText(errCliInvalidPlcAnswer))
			return
		}
		if res.Data[29] != byte(0xFF) {
			code := CPUError(uint(res.Data[29]))
			if code == 0 {
				code = errCliInvalidPlcAnswer
			}
			err = fmt.Errorf(ErrorText(code))
			return
		}
		// Amount of this slice
		dataSZL := int(binary.BigEndian.Uint16(res.Data[31:]))
		dataOffset := szlNextDataOffset
		if first {
			dataSZL -= 8 // Skips extra params (ID, Index, LENTHDR, N_DR)
			dataOffset = szlFirstDataOffset
		}
		if dataSZL < 0 || dataOffset+dataSZL > len(res.Data) {
			err = fmt.Errorf(ErrorText(errIsoInvalidPDU))
			return
		}
		done = res.Data[26] == 0x00
		seqIn = byte(res.Data[24]) // Slice sequence
		if first {
			szl.Header.LengthHeader = binary.BigEndian.Uint16(res.Data[37:])
			szl.Header.NumberOfDataRecord = binary.BigEndian.Uint16(res.Data[39:])
		}
		szl.Data = append(szl.Data, res.Data[dataOffset:dataOffset+dataSZL]...)
		offset += dataSZL
		first = false
	}
	size = offset
	return szl, size, err
}
