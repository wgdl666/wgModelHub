package arkvideo

import "testing"

func TestNormalizeParamsForcesAdaptiveAndClampsDuration(t *testing.T) {
	res, dur, aspect := NormalizeParams("4k", 40, "3:4")
	if res != "1080p" || dur != 30 || aspect != "adaptive" {
		t.Fatalf("res=%s dur=%d aspect=%s", res, dur, aspect)
	}
}

func TestNormalizeParamsKeepsAdaptiveDuration(t *testing.T) {
	_, dur, aspect := NormalizeParams("720p", -1, "9:16")
	if dur != -1 || aspect != "adaptive" {
		t.Fatalf("dur=%d aspect=%s", dur, aspect)
	}
}

func TestNormalizeParamsDefaults(t *testing.T) {
	res, dur, aspect := NormalizeParams("", 0, "")
	if res != "720p" || dur != 5 || aspect != "adaptive" {
		t.Fatalf("res=%s dur=%d aspect=%s", res, dur, aspect)
	}
}
