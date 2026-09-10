package preview

import (
	"net/http/httptest"
	"testing"
)

func TestTSTimestampPointsRequireProgramTableAndVideoPTS(t *testing.T) {
	data := make([]byte, 188*3)
	for i := 0; i < len(data); i += 188 {
		for j := 0; j < 188; j++ {
			data[i+j] = 0xff
		}
		data[i] = 0x47
		data[i+3] = 0x10
	}
	data[1] = 0
	data[2] = 0
	at := 188
	data[at+1] = 0x41
	data[at+2] = 0
	pes := data[at+4:]
	copy(pes, []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5})
	pts := int64(600 * 90000)
	copy(pes[9:], []byte{byte((pts>>29)&14) | 0x21, byte(pts >> 22), byte(pts>>14)&0xfe | 1, byte(pts >> 7), byte(pts<<1) | 1})
	points := tsTimestampPoints(data, 1880)
	if len(points) != 1 || points[0].offset != 1880 || points[0].seconds != 600 {
		t.Fatalf("points=%v", points)
	}
	data[2] = 10
	if got := tsTimestampPoints(data, 0); len(got) != 0 {
		t.Fatal("accepted timestamp without PAT")
	}
}

func TestTSOffsetRangeValidatesAndTranslates(t *testing.T) {
	for _, tc := range []struct {
		offset, request, want string
		bad                   bool
	}{
		{"1880", "", "bytes=1880-", false}, {"1880", "bytes=10-99", "bytes=1890-1979", false},
		{"-1", "", "", true}, {"1880", "bytes=-10", "", true}, {"1880", "bytes=1-0", "", true},
		{"9223372036854775807", "bytes=1-", "", true},
	} {
		r := httptest.NewRequest("GET", "http://localhost/stream/id?offset="+tc.offset, nil)
		r.Header.Set("Range", tc.request)
		got, err := tsOffsetRange(r)
		if (err != nil) != tc.bad || !tc.bad && got != tc.want {
			t.Fatalf("%+v got=%q err=%v", tc, got, err)
		}
	}
}
