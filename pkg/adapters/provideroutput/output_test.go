package provideroutput

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTailRetainsLatestBytesWithoutRejectingDiagnostics(t *testing.T) {
	var tail Tail
	if n, err := tail.Write(nil); n != 0 || err != nil || tail.String() != "" {
		t.Fatalf("empty write = %d, %v, %q", n, err, tail.String())
	}
	chunks := [][]byte{
		[]byte("initial diagnostic"),
		bytes.Repeat([]byte("old"), MaxDiagnosticBytes),
		[]byte("latest diagnostic"),
		bytes.Repeat([]byte("x"), MaxDiagnosticBytes),
	}
	var history []byte
	for _, chunk := range chunks {
		history = append(history, chunk...)
		if n, err := tail.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("diagnostic write = %d, %v; want %d, nil", n, err, len(chunk))
		}
		want := history[max(0, len(history)-MaxDiagnosticBytes):]
		if !bytes.Equal(tail.data, want) || tail.String() != string(want) {
			t.Fatal("diagnostic tail lost the latest bounded bytes")
		}
		if len(tail.data) > MaxDiagnosticBytes {
			t.Fatal("unbounded diagnostic storage")
		}
	}
}

func TestTailCopiesCallerInput(t *testing.T) {
	for _, size := range []int{16, MaxDiagnosticBytes + 1} {
		var tail Tail
		input := bytes.Repeat([]byte("x"), size)
		_, _ = tail.Write(input)
		input[len(input)-1] = 'z'
		if !strings.HasSuffix(tail.String(), "x") {
			t.Fatal("tail retained mutable caller storage")
		}
	}
}

func TestLimitedBufferExactLimitAndOverflow(t *testing.T) {
	cancellations := 0
	buffer := NewLimitedBuffer(func() { cancellations++ })
	first := bytes.Repeat([]byte("x"), MaxBytes-3)
	if n, err := buffer.Write(first); n != len(first) || err != nil {
		t.Fatalf("initial write = %d, %v", n, err)
	}
	if n, err := buffer.Write([]byte("end")); n != 3 || err != nil {
		t.Fatalf("exact-limit write = %d, %v", n, err)
	}
	if cancellations != 0 || LimitError("fixture", nil, buffer) != nil {
		t.Fatal("exact-limit output was rejected")
	}
	for i := 1; i <= 2; i++ {
		if n, err := buffer.Write([]byte("overflow")); n != 0 || !errors.Is(err, ErrTooLarge) {
			t.Fatalf("overflow write = %d, %v", n, err)
		}
		if cancellations != i {
			t.Fatalf("overflow cancellation count = %d, want %d", cancellations, i)
		}
	}
	if len(buffer.String()) != MaxBytes || !strings.HasSuffix(buffer.String(), "end") {
		t.Fatal("overflow changed the bounded retained prefix")
	}
	if n, err := buffer.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write after overflow = %d, %v", n, err)
	}
	want := fmt.Sprintf("fixture output exceeded %d bytes", MaxBytes)
	if err := LimitError("fixture", nil, NewLimitedBuffer(nil), buffer); err == nil || err.Error() != want {
		t.Fatalf("overflow diagnostic = %v, want %q", err, want)
	}
}

func TestLimitedBufferPreservesPartialOverflowWrite(t *testing.T) {
	buffer := NewLimitedBuffer(nil)
	_, _ = buffer.Write([]byte("prefix"))
	if n, err := buffer.Write(bytes.Repeat([]byte("z"), MaxBytes)); n != 0 || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("partly retained overflow write = %d, %v", n, err)
	}
	if got := buffer.String(); len(got) != MaxBytes || !strings.HasPrefix(got, "prefix") || !strings.HasSuffix(got, "z") {
		t.Fatal("partial overflow did not retain exactly the permitted prefix")
	}
	if ErrTooLarge.Error() != "provider output exceeded limit" {
		t.Fatal("output sentinel changed")
	}
}

func TestBoundedTextKeepsRuneAndFallbackSemantics(t *testing.T) {
	for _, test := range []struct {
		name, value, fallback, want string
		maximum                     int
	}{
		{"trim", " \n value \t", "unused", "value", 20},
		{"fallback", " \t", "no diagnostic output", "no diagnostic output", 30},
		{"fallback not trimmed", "", " fallback ", " fall", 5},
		{"unicode", " 中🙂文字 ", "unused", "中🙂", 2},
		{"zero", "text", "unused", "", 0},
		{"empty", "", "", "", 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := BoundedText(test.value, test.maximum, test.fallback); got != test.want {
				t.Fatalf("BoundedText = %q, want %q", got, test.want)
			}
		})
	}
}
