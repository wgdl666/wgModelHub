package arkvideo

import "testing"

func TestNormalizeParamsForcesAdaptiveAndClampsDuration(t *testing.T) {
	res, dur, aspect := NormalizeParams("4k", 40, "3:4", true)
	if res != "1080p" || dur != 30 || aspect != "adaptive" {
		t.Fatalf("res=%s dur=%d aspect=%s", res, dur, aspect)
	}
}

func TestNormalizeParamsKeepsAdaptiveDuration(t *testing.T) {
	_, dur, aspect := NormalizeParams("720p", -1, "9:16", true)
	if dur != -1 || aspect != "adaptive" {
		t.Fatalf("dur=%d aspect=%s", dur, aspect)
	}
}

func TestNormalizeParamsDefaults(t *testing.T) {
	res, dur, aspect := NormalizeParams("", 0, "", true)
	if res != "720p" || dur != 5 || aspect != "adaptive" {
		t.Fatalf("res=%s dur=%d aspect=%s", res, dur, aspect)
	}
}

func TestNormalizeParamsTextToVideoKeepsRatio(t *testing.T) {
	res, dur, aspect := NormalizeParams("720p", 12, "9:16", false)
	if res != "720p" || dur != 12 || aspect != "9:16" {
		t.Fatalf("res=%s dur=%d aspect=%s", res, dur, aspect)
	}
}

func TestNormalizeParamsTextToVideoDefaultRatio(t *testing.T) {
	_, _, aspect := NormalizeParams("720p", 5, "", false)
	if aspect != "16:9" {
		t.Fatalf("aspect=%s", aspect)
	}
}
