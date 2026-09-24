package cli

import (
	"strconv"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

func TestComma(t *testing.T) {
	for in, want := range map[any]string{
		jsonorder.Number("0"): "0", jsonorder.Number("999"): "999", jsonorder.Number("1000"): "1,000",
		jsonorder.Number("123456"): "123,456", jsonorder.Number("-1234567"): "-1,234,567",
		jsonorder.Number("1234.5"): "1,234.5", int64(23456): "23,456",
	} {
		if got := comma(in); got != want {
			t.Errorf("comma(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFixLocalZonePOSIX(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	for tz, off := range map[string]int{"JST-9": 9 * 3600, "<+0530>-5:30": 5*3600 + 1800, "ABC5": -5 * 3600} {
		time.Local = time.UTC
		fixLocalZone(env.FromMap(map[string]string{"TZ": tz}))
		if _, got := time.Unix(0, 0).In(time.Local).Zone(); got != off {
			t.Errorf("TZ=%s: offset %d, want %d", tz, got, off)
		}
	}
}

func TestSortUsageStages(t *testing.T) {
	mk := func(at string, id int) *jsonorder.Object {
		return jsonorder.NewObject().Set("at", at).Set("id", jsonorder.Number(strconv.Itoa(id)))
	}
	s := []*jsonorder.Object{mk("2024-05-02T03:00:00.5Z", 1), mk("2024-05-02T03:00:00.123456Z", 3), mk("2024-05-02T03:00:00Z", 9), mk("2024-05-02T03:00:00.123456Z", 2)}
	sortUsageStages(s)
	var got []string
	for _, st := range s {
		got = append(got, st.String("at")+"#"+jsonorder.Str(get(st, "id", nil)))
	}
	want := []string{"2024-05-02T03:00:00Z#9", "2024-05-02T03:00:00.123456Z#2", "2024-05-02T03:00:00.123456Z#3", "2024-05-02T03:00:00.5Z#1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("順が違います: %v", got)
		}
	}
}
