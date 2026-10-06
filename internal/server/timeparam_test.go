// SPDX-License-Identifier: MIT

package server

import "testing"

func TestMediaSeconds(t *testing.T) {
	for _, value := range []string{"", "invalid", "NaN", "Inf", "-Inf", "-1", "10000001", "1e999"} {
		if got := mediaSeconds(value); got != 0 {
			t.Errorf("mediaSeconds(%q) = %v, want zero", value, got)
		}
	}
	if got := mediaSeconds("123.75"); got != 123.75 {
		t.Fatal(got)
	}
	if _, ok := firstPTS("NaN", 50); ok {
		t.Fatal("NaN became a keyframe position")
	}
}
