package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func legacyBuildStackKeyWithLines(frames []string, lines []uint32) string {
	parts := make([]string, len(frames))
	for i := range frames {
		if lines[i] > 0 {
			parts[i] = fmt.Sprintf("%s:%d", frames[i], lines[i])
		} else {
			parts[i] = frames[i]
		}
	}
	return strings.Join(parts, ";")
}

func TestDigitsUint32(t *testing.T) {
	tests := []struct {
		n    uint32
		want int
	}{
		{0, 1},
		{9, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{999, 3},
		{1000, 4},
		{9999, 4},
		{10000, 5},
		{99999, 5},
		{100000, 6},
		{999999, 6},
		{1000000, 7},
		{9999999, 7},
		{10000000, 8},
		{99999999, 8},
		{100000000, 9},
		{999999999, 9},
		{1000000000, 10},
		{4294967295, 10},
	}

	for _, tt := range tests {
		if got := digitsUint32(tt.n); got != tt.want {
			t.Fatalf("digitsUint32(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestBuildStackKeyWithLinesMatchesLegacy(t *testing.T) {
	r := rand.New(rand.NewSource(42))

	// Include edge cases first.
	edge := []struct {
		frames []string
		lines  []uint32
	}{
		{nil, nil},
		{[]string{""}, []uint32{0}},
		{[]string{"A.a"}, []uint32{123}},
		{[]string{"A:a", "B.b"}, []uint32{7, 0}},
		{[]string{"A.a", "B.b", "C.c"}, []uint32{1, 2147483647, 0}},
	}

	for _, tc := range edge {
		got := buildStackKeyWithLines(tc.frames, tc.lines)
		want := legacyBuildStackKeyWithLines(tc.frames, tc.lines)
		if got != want {
			t.Fatalf("edge mismatch: got=%q want=%q", got, want)
		}
	}

	for i := 0; i < 300; i++ {
		n := r.Intn(30)
		frames := make([]string, n)
		lines := make([]uint32, n)

		for j := 0; j < n; j++ {
			size := r.Intn(20)
			b := make([]byte, size)
			for k := 0; k < size; k++ {
				switch r.Intn(30) {
				case 0:
					b[k] = '/'
				case 1:
					b[k] = '.'
				case 2:
					b[k] = ':'
				case 3:
					b[k] = ';'
				default:
					b[k] = byte('a' + r.Intn(26))
				}
			}
			frames[j] = string(b)
			switch r.Intn(5) {
			case 0:
				lines[j] = 0
			case 1:
				lines[j] = uint32(r.Intn(10))
			case 2:
				lines[j] = uint32(r.Intn(100000))
			default:
				lines[j] = uint32(r.Int31())
			}
		}

		got := buildStackKeyWithLines(frames, lines)
		want := legacyBuildStackKeyWithLines(frames, lines)
		if got != want {
			t.Fatalf("random mismatch at iter=%d: got=%q want=%q", i, got, want)
		}
	}
}
