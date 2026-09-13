package gos7

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// szlTransporter answers each Send with the next canned frame.
type szlTransporter struct {
	frames   [][]byte
	requests [][]byte
}

func (t *szlTransporter) Send(request []byte) ([]byte, error) {
	t.requests = append(t.requests, append([]byte(nil), request...))
	if len(t.frames) == 0 {
		return nil, errNoFrames
	}
	frame := t.frames[0]
	t.frames = t.frames[1:]
	return frame, nil
}

type szlPackager struct{}

func (szlPackager) Verify(request []byte, response []byte) error { return nil }

var errNoFrames = &S7Error{High: 0xFF, Low: 0xFF}

// szlSlice builds a "read SZL" userdata response frame (TPKT header included).
// retCode is the data return code (0xFF = ok), errCode the parameter error code,
// dataLen the value of the data length word, and payload the bytes that follow it.
func szlSlice(seq byte, last bool, retCode byte, errCode uint16, dataLen uint16, payload []byte) []byte {
	lastUnit := byte(0x01)
	if last {
		lastUnit = 0x00
	}
	params := []byte{0x00, 0x01, 0x12, 0x08, 0x12, 0x84, 0x01, seq, 0x00, lastUnit, 0, 0}
	binary.BigEndian.PutUint16(params[10:], errCode)
	data := []byte{retCode, 0x09, 0, 0}
	binary.BigEndian.PutUint16(data[2:], dataLen)
	data = append(data, payload...)
	header := []byte{0x32, 0x07, 0, 0, 0, 1, 0, byte(len(params)), 0, 0}
	binary.BigEndian.PutUint16(header[8:], uint16(len(data)))
	frame := []byte{0x03, 0x00, 0, 0, 0x02, 0xF0, 0x80}
	frame = append(frame, header...)
	frame = append(frame, params...)
	frame = append(frame, data...)
	binary.BigEndian.PutUint16(frame[2:], uint16(len(frame)))
	return frame
}

// szlFirst builds the first slice: SZL ID, index, LENTHDR, N_DR, records.
func szlFirst(seq byte, last bool, id uint16, lenthdr uint16, ndr uint16, records []byte) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint16(payload[0:], id)
	binary.BigEndian.PutUint16(payload[2:], 0x0000)
	binary.BigEndian.PutUint16(payload[4:], lenthdr)
	binary.BigEndian.PutUint16(payload[6:], ndr)
	payload = append(payload, records...)
	return szlSlice(seq, last, 0xFF, 0, uint16(len(payload)), payload)
}

// szlNext builds a continuation slice: records only.
func szlNext(seq byte, last bool, records []byte) []byte {
	return szlSlice(seq, last, 0xFF, 0, uint16(len(records)), records)
}

// szl0011Record builds one 28 byte module identification record.
func szl0011Record(index uint16, mlfb string, bgtyp, ausbg, ausbe uint16) []byte {
	rec := make([]byte, 28)
	binary.BigEndian.PutUint16(rec[0:], index)
	copy(rec[2:22], []byte(mlfb + strings.Repeat(" ", 20))[:20])
	binary.BigEndian.PutUint16(rec[22:], bgtyp)
	binary.BigEndian.PutUint16(rec[24:], ausbg)
	binary.BigEndian.PutUint16(rec[26:], ausbe)
	return rec
}

func newSzlClient(frames ...[]byte) (*client, *szlTransporter) {
	tr := &szlTransporter{frames: frames}
	return &client{packager: szlPackager{}, transporter: tr}, tr
}

func TestGetOrderCode(t *testing.T) {
	// Records as returned by an IM151-8 PN/DP CPU (plcscan README): the boot
	// loader record 0x0081 comes last, so the version must be taken from the
	// basic firmware record 0x0007 and not from the end of the list.
	records := append(append(append(
		szl0011Record(0x0001, "6ES7 151-8AB01-0AB0", 0x00C0, 0x0002, 0x0001),
		szl0011Record(0x0006, "6ES7 151-8AB01-0AB0", 0x00C0, 0x0002, 0x0001)...),
		szl0011Record(0x0007, "", 0x00C0, 0x5603, 0x0206)...), // V3.2.6
		szl0011Record(0x0081, "Boot Loader", 0x0000, 0x4120, 0x0909)...)
	c, tr := newSzlClient(szlFirst(0, true, 0x0011, 28, 4, records))

	info, err := c.GetOrderCode()
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != "6ES7 151-8AB01-0AB0 " {
		t.Errorf("unexpected order code %q", info.Code)
	}
	if info.V1 != 3 || info.V2 != 2 || info.V3 != 6 {
		t.Errorf("unexpected version %d.%d.%d", info.V1, info.V2, info.V3)
	}
	if len(tr.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(tr.requests))
	}
	if id := binary.BigEndian.Uint16(tr.requests[0][29:]); id != 0x0011 {
		t.Errorf("requested SZL 0x%04X, want 0x0011", id)
	}
}

func TestGetOrderCodeFallsBackToLastRecord(t *testing.T) {
	// No 0x0007 record: keep Snap7's behaviour and use the last record.
	records := append(
		szl0011Record(0x0001, "6ES7 214-1AG40-0XB0", 0x0000, 0x0004, 0x0000),
		szl0011Record(0x0006, "6ES7 214-1AG40-0XB0", 0x0000, 0x5604, 0x0100)...) // V4.1.0
	c, _ := newSzlClient(szlFirst(0, true, 0x0011, 28, 2, records))

	info, err := c.GetOrderCode()
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != "6ES7 214-1AG40-0XB0 " {
		t.Errorf("unexpected order code %q", info.Code)
	}
	if info.V1 != 4 || info.V2 != 1 || info.V3 != 0 {
		t.Errorf("unexpected version %d.%d.%d", info.V1, info.V2, info.V3)
	}
}

func TestGetOrderCodeHonoursRecordLength(t *testing.T) {
	// LENTHDR larger than 28: records are stepped by the header value.
	rec1 := append(szl0011Record(0x0001, "6ES7 516-3AN01-0AB0", 0x0000, 0x0001, 0x0000), 0xAA, 0xBB)
	rec2 := append(szl0011Record(0x0007, "", 0x0000, 0x5602, 0x0901), 0xCC, 0xDD) // V2.9.1
	c, _ := newSzlClient(szlFirst(0, true, 0x0011, 30, 2, append(rec1, rec2...)))

	info, err := c.GetOrderCode()
	if err != nil {
		t.Fatal(err)
	}
	if info.V1 != 2 || info.V2 != 9 || info.V3 != 1 {
		t.Errorf("unexpected version %d.%d.%d", info.V1, info.V2, info.V3)
	}
}

func TestReadSzlTwoSlices(t *testing.T) {
	rec1 := szl0011Record(0x0001, "6ES7 315-2EH14-0AB0", 0x00C0, 0x0003, 0x0002)
	rec2 := szl0011Record(0x0006, "6ES7 315-2EH14-0AB0", 0x00C0, 0x0003, 0x0002)
	rec3 := szl0011Record(0x0007, "", 0x00C0, 0x5603, 0x0206)
	c, tr := newSzlClient(
		szlFirst(1, false, 0x0011, 28, 3, append(append([]byte(nil), rec1...), rec2...)),
		szlNext(2, true, rec3),
	)

	szl, size, err := c.readSzl(0x0011, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte(nil), rec1...), rec2...), rec3...)
	if !bytes.Equal(szl.Data, want) {
		t.Errorf("reassembled data mismatch:\n got %x\nwant %x", szl.Data, want)
	}
	if size != len(want) {
		t.Errorf("size = %d, want %d", size, len(want))
	}
	if szl.Header.LengthHeader != 28 || szl.Header.NumberOfDataRecord != 3 {
		t.Errorf("unexpected header %+v", szl.Header)
	}
	if len(tr.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(tr.requests))
	}
	if tr.requests[1][24] != 1 {
		t.Errorf("continuation request carries sequence %d, want 1", tr.requests[1][24])
	}
}

func TestReadSzlRejectsBadAnswers(t *testing.T) {
	rec := szl0011Record(0x0001, "6ES7 214-1AG40-0XB0", 0x0000, 0x0004, 0x0000)
	cases := []struct {
		name  string
		frame []byte
	}{
		{"return code not 0xFF, error code 0", szlSlice(0, true, 0x0A, 0, 4, []byte{0, 0x11, 0, 0})},
		{"error code set, return code 0xFF", szlSlice(0, true, 0xFF, 0xD401, 4, []byte{0, 0x11, 0, 0})},
		{"declared length below SZL header", szlSlice(0, true, 0xFF, 0, 4, []byte{0, 0x11, 0, 0})},
		{"declared length exceeds frame", szlSlice(0, true, 0xFF, 0, 500, append([]byte{0, 0x11, 0, 0, 0, 28, 0, 1}, rec...))},
		{"frame too short", []byte{0x03, 0x00, 0x00, 0x07, 0x02, 0xF0, 0x80}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newSzlClient(tc.frame)
			if _, _, err := c.readSzl(0x0011, 0); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestReadSzlRejectsOverlongContinuation(t *testing.T) {
	rec := szl0011Record(0x0001, "6ES7 214-1AG40-0XB0", 0x0000, 0x0004, 0x0000)
	c, _ := newSzlClient(
		szlFirst(1, false, 0x0011, 28, 2, rec),
		szlSlice(2, true, 0xFF, 0, 300, rec),
	)
	if _, _, err := c.readSzl(0x0011, 0); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSzlConsumersRejectShortLists(t *testing.T) {
	empty := func() *client {
		c, _ := newSzlClient(szlFirst(0, true, 0x0011, 28, 0, nil))
		return c
	}
	if _, err := empty().GetOrderCode(); err == nil {
		t.Error("GetOrderCode: expected an error for an empty list")
	}
	if _, err := empty().GetCPUInfo(); err == nil {
		t.Error("GetCPUInfo: expected an error for an empty list")
	}
	if _, err := empty().GetCPInfo(); err == nil {
		t.Error("GetCPInfo: expected an error for an empty list")
	}
}
